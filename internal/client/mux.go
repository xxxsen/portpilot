package client

import (
	"context"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/transport"
	"github.com/xtaci/smux"
	"go.uber.org/zap"
)

type clientMux struct {
	id       string
	session  *smux.Session
	active   atomic.Int64
	draining atomic.Bool
}

type MuxPool struct {
	control  *controlSession
	params   protocol.DataPlane
	mu       sync.RWMutex
	sessions map[string]*clientMux
	closed   atomic.Bool
}

func NewMuxPool(control *controlSession, params protocol.DataPlane) *MuxPool {
	return &MuxPool{control: control, params: params, sessions: make(map[string]*clientMux)}
}

func (p *MuxPool) Open(parent context.Context, request protocol.CreateMux) {
	if p.closed.Load() {
		return
	}
	ctx, cancel := context.WithTimeout(parent, 15*time.Second)
	defer cancel()
	conn, err := transport.DialUpgrade(
		ctx, p.control.app.Config.Server,
		joinPath(p.control.app.Config.Path, "data/mux"),
		p.control.app.Config.EnableTLS, p.control.app.Config.TLSCAFile,
		15*time.Second,
	)
	if err != nil {
		p.control.app.Logger.Warn("mux upgrade failed", zap.Error(err))
		return
	}
	handshake := protocol.MuxAuth{
		Version: protocol.Version, Role: "mux",
		ClientID:  p.control.app.Config.ClientID,
		SessionID: p.control.id, SessionEpoch: p.control.epoch,
		MuxID: request.MuxID, MuxTicket: request.MuxTicket,
	}
	if err := protocol.WriteJSONFrame(conn, handshake); err != nil {
		_ = conn.Close()
		return
	}
	var result protocol.HandshakeResult
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &result); err != nil || result.Type != "MUX_OK" {
		_ = conn.Close()
		return
	}
	session, err := smux.Client(conn, clientSMuxConfig(p.params.SMux))
	if err != nil {
		_ = conn.Close()
		return
	}
	mux := &clientMux{id: request.MuxID, session: session}
	p.mu.Lock()
	if p.closed.Load() || len(p.sessions) >= p.params.MaxSessions {
		p.mu.Unlock()
		_ = session.Close()
		return
	}
	p.sessions[mux.id] = mux
	p.mu.Unlock()
	ready, _ := protocol.NewMessage(protocol.TypeMuxReady, protocol.MuxReady{MuxID: mux.id})
	_ = p.control.send(ready)
	p.control.app.Logger.Info("smux session ready", zap.String("mux_id", mux.id))
	go func() {
		<-session.CloseChan()
		p.mu.Lock()
		if p.sessions[mux.id] == mux {
			delete(p.sessions, mux.id)
		}
		p.mu.Unlock()
		closed, _ := protocol.NewMessage(protocol.TypeMuxClosed, protocol.MuxReady{MuxID: mux.id})
		_ = p.control.send(closed)
	}()
}

func (p *MuxPool) Select() *clientMux {
	p.mu.RLock()
	defer p.mu.RUnlock()
	var selected *clientMux
	for _, candidate := range p.sessions {
		if candidate.draining.Load() || candidate.session.IsClosed() ||
			candidate.active.Load() >= int64(p.params.MaxStreamsPerSession) {
			continue
		}
		if selected == nil || candidate.active.Load() < selected.active.Load() {
			selected = candidate
		}
	}
	return selected
}

func (p *MuxPool) Drain(id string) {
	p.mu.RLock()
	mux := p.sessions[id]
	p.mu.RUnlock()
	if mux == nil || !mux.draining.CompareAndSwap(false, true) {
		return
	}
	go func() {
		timeout := durationOr(p.params.SMux.DrainTimeout, 5*time.Minute)
		timer := time.NewTimer(timeout)
		ticker := time.NewTicker(50 * time.Millisecond)
		defer timer.Stop()
		defer ticker.Stop()
		for {
			if mux.active.Load() == 0 {
				_ = mux.session.Close()
				return
			}
			select {
			case <-timer.C:
				_ = mux.session.Close()
				return
			case <-ticker.C:
			}
		}
	}()
}

func (p *MuxPool) BeginDrain() {
	p.closed.Store(true)
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, mux := range p.sessions {
		mux.draining.Store(true)
	}
}

func (p *MuxPool) Close() {
	p.closed.Store(true)
	p.mu.Lock()
	sessions := p.sessions
	p.sessions = make(map[string]*clientMux)
	p.mu.Unlock()
	for _, session := range sessions {
		_ = session.session.Close()
	}
}

func (p *MuxPool) String() string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return fmt.Sprintf("mux-pool{%d}", len(p.sessions))
}

var _ net.Conn = (*trackedConn)(nil)
