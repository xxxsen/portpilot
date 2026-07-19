package server

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/relay"
	"github.com/portpilot/portpilot/internal/store"
)

type pendingTunnel struct {
	ID        string
	ClientID  string
	ProxyID   string
	SessionID string
	Epoch     uint64
	External  net.Conn
	Data      net.Conn
	CreatedAt time.Time
	done      chan struct{}
	closed    atomic.Bool
}

type TunnelManager struct {
	app            *App
	mu             sync.Mutex
	pending        map[string]*pendingTunnel
	activeTunnels  map[string]*pendingTunnel
	activeByClient map[string]int
	active         atomic.Int64
}

func NewTunnelManager(app *App) *TunnelManager {
	return &TunnelManager{
		app: app, pending: make(map[string]*pendingTunnel),
		activeTunnels:  make(map[string]*pendingTunnel),
		activeByClient: make(map[string]int),
	}
}

func (m *TunnelManager) HandleExternal(runtime ProxyRuntime, conn net.Conn) {
	session := m.app.Sessions.Current(runtime.ClientID)
	if session == nil || session.closed.Load() {
		_ = conn.Close()
		m.record(runtime, "", "client_offline", 0, 0)
		return
	}
	m.mu.Lock()
	if len(m.pending) >= m.app.Config.Limits.MaxPendingTunnels ||
		m.clientTunnelCountLocked(runtime.ClientID) >= m.app.Config.Limits.MaxTunnelsPerClient {
		m.mu.Unlock()
		_ = conn.Close()
		m.record(runtime, "", "resource_limit", 0, 0)
		return
	}
	connectionID, err := auth.RandomToken(18)
	if err != nil {
		m.mu.Unlock()
		_ = conn.Close()
		return
	}
	ticket, err := m.app.Tickets.Issue(auth.TicketBinding{
		ClientID: runtime.ClientID, SessionID: session.ID,
		SessionEpoch: session.Epoch, Kind: "tcp", ObjectID: connectionID,
	})
	if err != nil {
		m.mu.Unlock()
		_ = conn.Close()
		return
	}
	pending := &pendingTunnel{
		ID: connectionID, ClientID: runtime.ClientID, ProxyID: runtime.Proxy.ProxyID,
		SessionID: session.ID, Epoch: session.Epoch, External: conn,
		CreatedAt: time.Now(), done: make(chan struct{}),
	}
	m.pending[connectionID] = pending
	m.mu.Unlock()

	message, err := protocol.NewMessage(protocol.TypeOpenTCP, protocol.OpenTCP{
		ConnectionID: connectionID, ProxyID: runtime.Proxy.ProxyID,
		Connect: runtime.Data.Connect, Transport: runtime.Data.Transport,
		DataTicket: ticket, AllowDirectFallback: m.app.Config.DataPlane.DirectFallback,
		Deadline: time.Now().Add(m.app.Config.Duration(m.app.Config.Timeouts.DataConnect)).UTC().Format(time.RFC3339Nano),
	})
	if err != nil || session.Send(message) != nil {
		m.remove(connectionID, "control_send_failed")
		return
	}
	go func() {
		timer := time.NewTimer(m.app.Config.Duration(m.app.Config.Timeouts.DataConnect))
		defer timer.Stop()
		select {
		case <-timer.C:
			m.remove(connectionID, "data_connect_timeout")
			cancel, _ := protocol.NewMessage(protocol.TypeCancelTunnel, map[string]string{"connection_id": connectionID})
			_ = session.Send(cancel)
		case <-pending.done:
		}
	}()
}

func (m *TunnelManager) Pair(open protocol.StreamOpen, dataConn net.Conn) (*pendingTunnel, bool) {
	binding := auth.TicketBinding{
		ClientID: open.ClientID, SessionID: open.SessionID,
		SessionEpoch: open.SessionEpoch, Kind: "tcp", ObjectID: open.ConnectionID,
	}
	if !m.app.Tickets.Consume(open.DataTicket, binding) {
		m.app.recordSecurity(open.ClientID, "tcp_ticket_rejected", map[string]any{"connection_id": open.ConnectionID, "session_epoch": open.SessionEpoch})
		return nil, false
	}
	m.mu.Lock()
	pending := m.pending[open.ConnectionID]
	if pending == nil || pending.ClientID != open.ClientID ||
		pending.SessionID != open.SessionID || pending.Epoch != open.SessionEpoch {
		m.app.recordSecurity(open.ClientID, "tcp_pair_rejected", map[string]any{"connection_id": open.ConnectionID, "session_epoch": open.SessionEpoch})
		m.mu.Unlock()
		return nil, false
	}
	delete(m.pending, open.ConnectionID)
	pending.Data = dataConn
	m.activeTunnels[pending.ID] = pending
	close(pending.done)
	m.active.Add(1)
	m.activeByClient[pending.ClientID]++
	m.mu.Unlock()
	return pending, true
}

func (m *TunnelManager) Relay(ctx context.Context, pending *pendingTunnel, dataConn net.Conn) {
	defer func() {
		m.active.Add(-1)
		m.mu.Lock()
		delete(m.activeTunnels, pending.ID)
		m.activeByClient[pending.ClientID]--
		if m.activeByClient[pending.ClientID] <= 0 {
			delete(m.activeByClient, pending.ClientID)
		}
		m.mu.Unlock()
	}()
	relayCtx, cancel := context.WithTimeout(ctx, m.app.Config.Duration(m.app.Config.Timeouts.TCPMaxLifetime))
	defer cancel()
	result := relay.Bidirectional(
		relayCtx, pending.External, dataConn,
		m.app.Config.Duration(m.app.Config.Timeouts.TCPIdle),
	)
	reason := "closed"
	if result.Err != nil && !errors.Is(result.Err, net.ErrClosed) {
		reason = "relay_error"
	}
	m.app.Events.Record(store.Event{
		Kind: "tcp_tunnel", ClientID: pending.ClientID, ProxyID: pending.ProxyID,
		ConnectionID: pending.ID, UpBytes: result.UpBytes, DownBytes: result.DownBytes,
		Reason: reason,
	})
}

func (m *TunnelManager) HandleOpenResult(result protocol.OpenResult) {
	if !result.Success {
		m.remove(result.ConnectionID, result.ErrorCode)
	}
}

func (m *TunnelManager) CancelSession(session *Session) {
	m.mu.Lock()
	var remove []*pendingTunnel
	for id, pending := range m.pending {
		if pending.ClientID == session.ClientID && pending.SessionID == session.ID && pending.Epoch == session.Epoch {
			delete(m.pending, id)
			remove = append(remove, pending)
		}
	}
	m.mu.Unlock()
	for _, pending := range remove {
		if pending.closed.CompareAndSwap(false, true) {
			close(pending.done)
			_ = pending.External.Close()
		}
	}
}

func (m *TunnelManager) CloseSession(session *Session) {
	m.mu.Lock()
	active := make([]*pendingTunnel, 0)
	for _, tunnel := range m.activeTunnels {
		if tunnel.ClientID == session.ClientID && tunnel.SessionID == session.ID && tunnel.Epoch == session.Epoch {
			active = append(active, tunnel)
		}
	}
	m.mu.Unlock()
	for _, tunnel := range active {
		_ = tunnel.External.Close()
		if tunnel.Data != nil {
			_ = tunnel.Data.Close()
		}
	}
}

func (m *TunnelManager) remove(connectionID, reason string) {
	m.mu.Lock()
	pending := m.pending[connectionID]
	if pending != nil {
		delete(m.pending, connectionID)
	}
	m.mu.Unlock()
	if pending != nil && pending.closed.CompareAndSwap(false, true) {
		close(pending.done)
		_ = pending.External.Close()
		m.app.Events.Record(store.Event{
			Kind: "tcp_tunnel", ClientID: pending.ClientID, ProxyID: pending.ProxyID,
			ConnectionID: pending.ID, Reason: reason,
		})
	}
}

func (m *TunnelManager) clientTunnelCountLocked(clientID string) int {
	count := m.activeByClient[clientID]
	for _, pending := range m.pending {
		if pending.ClientID == clientID {
			count++
		}
	}
	return count
}

func (m *TunnelManager) Counts() (pending int, active int64) {
	m.mu.Lock()
	pending = len(m.pending)
	m.mu.Unlock()
	return pending, m.active.Load()
}

func (m *TunnelManager) record(runtime ProxyRuntime, connectionID, reason string, up, down int64) {
	m.app.Events.Record(store.Event{
		Kind: "tcp_tunnel", ClientID: runtime.ClientID, ProxyID: runtime.Proxy.ProxyID,
		ConnectionID: connectionID, UpBytes: up, DownBytes: down, Reason: reason,
	})
}
