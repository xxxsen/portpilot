package server

import (
	"time"

	"github.com/portpilot/portpilot/internal/protocol"
	"go.uber.org/zap"
)

func (s *Session) ReserveMux(id string) bool {
	if s.closed.Load() || !s.app.Sessions.IsCurrent(s) {
		return false
	}
	if s.app.Sessions.muxTotal.Add(1) > int64(s.app.Config.Limits.MaxMuxSessionsTotal) {
		s.app.Sessions.muxTotal.Add(-1)
		return false
	}
	s.muxMu.Lock()
	defer s.muxMu.Unlock()
	if len(s.muxes)+len(s.pendingMux) >= s.app.Config.DataPlane.SMux.MaxSessions {
		s.app.Sessions.muxTotal.Add(-1)
		return false
	}
	if _, exists := s.muxes[id]; exists {
		s.app.Sessions.muxTotal.Add(-1)
		return false
	}
	if _, exists := s.pendingMux[id]; exists {
		s.app.Sessions.muxTotal.Add(-1)
		return false
	}
	s.pendingMux[id] = struct{}{}
	return true
}

func (s *Session) ReleaseMux(id string) {
	s.muxMu.Lock()
	if _, exists := s.pendingMux[id]; exists {
		delete(s.pendingMux, id)
		s.app.Sessions.muxTotal.Add(-1)
	}
	s.muxMu.Unlock()
}

func (s *Session) MuxCapacity() (active, pending int) {
	s.muxMu.RLock()
	defer s.muxMu.RUnlock()
	return len(s.muxes), len(s.pendingMux)
}

func (a *App) ensureMux(session *Session, desired int) {
	if session == nil || session.closed.Load() || !a.Sessions.IsCurrent(session) {
		return
	}
	if desired > a.Config.DataPlane.SMux.MaxSessions {
		desired = a.Config.DataPlane.SMux.MaxSessions
	}
	for {
		active, pending := session.MuxCapacity()
		if active+pending >= desired {
			return
		}
		if err := a.requestMux(session); err != nil {
			a.Logger.Warn(
				"cannot request smux session",
				zap.String("client_id", session.ClientID),
				zap.Error(err),
			)
			return
		}
	}
}

func (a *App) monitorMux(mux *MuxSession) {
	maxAge := a.Config.Duration(a.Config.DataPlane.SMux.SessionMaxAge)
	idleShrink := maxAge / 4
	if idleShrink > time.Minute {
		idleShrink = time.Minute
	}
	if idleShrink < 5*time.Second {
		idleShrink = 5 * time.Second
	}
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-mux.Session.CloseChan():
			return
		case now := <-ticker.C:
			age := now.Sub(mux.createdAt)
			idle := now.Sub(time.Unix(0, mux.lastActive.Load()))
			active, _ := mux.owner.MuxCapacity()
			if age >= maxAge || (active > a.Config.DataPlane.SMux.MinSessions && mux.streams.Load() == 0 && idle >= idleShrink) {
				if age >= maxAge {
					a.ensureMux(mux.owner, active+1)
				}
				message, _ := protocol.NewMessage(protocol.TypeMuxDrain, protocol.MuxReady{MuxID: mux.ID})
				_ = mux.owner.Send(message)
				mux.BeginDrain()
				a.ensureMux(mux.owner, a.Config.DataPlane.SMux.MinSessions)
				return
			}
		}
	}
}
