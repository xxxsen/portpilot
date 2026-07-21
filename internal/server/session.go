package server

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/xtaci/smux"
)

type outboundControl struct {
	message protocol.Message
	ack     chan error
}

type Session struct {
	ClientID    string
	ID          string
	Epoch       uint64
	Agent       protocol.Agent
	AgentStatus string
	SourceAddr  string
	SourceIP    string
	ConnectedAt int64

	app              *App
	conn             *websocket.Conn
	ctx              context.Context
	cancel           context.CancelFunc
	send             chan outboundControl
	sequence         atomic.Uint64
	muxMu            sync.RWMutex
	muxes            map[string]*MuxSession
	pendingMux       map[string]struct{}
	closed           atomic.Bool
	lastSeenAt       atomic.Int64
	controlRTTMillis atomic.Int64
	lastSnapshotAt   atomic.Int64
	closeReason      atomic.Pointer[string]
	offlineSnapshot  atomic.Bool
	pingMu           sync.Mutex
	pendingPings     map[string]time.Time
}

type MuxSession struct {
	ID         string
	Session    *smux.Session
	owner      *Session
	draining   atomic.Bool
	streams    atomic.Int64
	createdAt  time.Time
	lastActive atomic.Int64
	closed     atomic.Bool
}

type SessionManager struct {
	app      *App
	mu       sync.RWMutex
	active   map[string]*Session
	epochs   map[string]uint64
	muxTotal atomic.Int64
}

func NewSessionManager(app *App) *SessionManager {
	return &SessionManager{
		app: app, active: make(map[string]*Session), epochs: make(map[string]uint64),
	}
}

func (m *SessionManager) Install(
	clientID, sessionID, source string,
	agent protocol.Agent,
	agentStatus string,
	conn *websocket.Conn,
) *Session {
	m.mu.Lock()
	m.epochs[clientID]++
	epoch := m.epochs[clientID]
	old := m.active[clientID]
	ctx, cancel := context.WithCancel(context.Background())
	now := time.Now().UTC().UnixMilli()
	session := &Session{
		ClientID: clientID, ID: sessionID, Epoch: epoch,
		Agent: agent, AgentStatus: agentStatus,
		SourceAddr: source, SourceIP: source, ConnectedAt: now,
		app: m.app, conn: conn, ctx: ctx, cancel: cancel,
		send: make(chan outboundControl, 256), muxes: make(map[string]*MuxSession),
		pendingMux:   make(map[string]struct{}),
		pendingPings: make(map[string]time.Time),
	}
	session.lastSeenAt.Store(now)
	session.lastSnapshotAt.Store(now)
	m.active[clientID] = session
	m.mu.Unlock()
	if old != nil {
		message, _ := protocol.NewMessage(protocol.TypeSessionReplaced, protocol.ErrorPayload{Code: protocol.ErrSessionStale})
		_ = old.SendAndWait(message, time.Second)
		old.Drain("session_replaced")
	}
	m.app.enqueueAgentSnapshot(agentSnapshot(session, nil))
	return session
}

func (m *SessionManager) Current(clientID string) *Session {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[clientID]
}

func (m *SessionManager) IsCurrent(session *Session) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.active[session.ClientID] == session
}

func (m *SessionManager) Remove(session *Session) {
	m.mu.Lock()
	if m.active[session.ClientID] == session {
		delete(m.active, session.ClientID)
	}
	m.mu.Unlock()
	session.Drain(session.CloseReason())
	session.persistOffline()
}

func (m *SessionManager) Epoch(clientID string) uint64 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.epochs[clientID]
}

func (m *SessionManager) Snapshot() []SessionStatus {
	m.mu.RLock()
	sessions := make([]*Session, 0, len(m.active))
	for _, session := range m.active {
		sessions = append(sessions, session)
	}
	m.mu.RUnlock()
	result := make([]SessionStatus, 0, len(sessions))
	for _, session := range sessions {
		result = append(result, session.Status())
	}
	return result
}

func (m *SessionManager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*Session, 0, len(m.active))
	for _, session := range m.active {
		sessions = append(sessions, session)
	}
	m.active = make(map[string]*Session)
	m.mu.Unlock()
	for _, session := range sessions {
		session.Close("server_shutdown")
	}
}

func (m *SessionManager) ReconcileClients(file config.ClientFile) {
	enabled := make(map[string]bool, len(file.ClientConfig))
	for _, client := range file.ClientConfig {
		enabled[client.ClientID] = client.Enabled
	}
	m.mu.Lock()
	var closeItems []*Session
	for clientID, session := range m.active {
		value, exists := enabled[clientID]
		if exists && value {
			continue
		}
		delete(m.active, clientID)
		closeItems = append(closeItems, session)
	}
	m.mu.Unlock()
	for _, session := range closeItems {
		reason := "client_disabled"
		if _, exists := enabled[session.ClientID]; !exists {
			reason = "client_disabled"
		}
		session.Close(reason)
	}
}

func (s *Session) Send(message protocol.Message) error {
	return s.enqueue(message, nil)
}

func (s *Session) SendAndWait(message protocol.Message, timeout time.Duration) error {
	ack := make(chan error, 1)
	if err := s.enqueue(message, ack); err != nil {
		return err
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-ack:
		return err
	case <-s.ctx.Done():
		return errors.New("session closed")
	case <-timer.C:
		return errors.New("control write acknowledgement timeout")
	}
}

func (s *Session) enqueue(message protocol.Message, ack chan error) error {
	if s.closed.Load() {
		return errors.New("session closed")
	}
	message.SessionID = s.ID
	message.SessionEpoch = s.Epoch
	select {
	case s.send <- outboundControl{message: message, ack: ack}:
		return nil
	case <-s.ctx.Done():
		return errors.New("session closed")
	default:
		return errors.New("control write queue full")
	}
}

func (s *Session) writeLoop() error {
	heartbeat := s.app.Config.Duration(s.app.Config.Timeouts.HeartbeatInterval)
	ticker := time.NewTicker(heartbeat)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return nil
		case outbound := <-s.send:
			outbound.message.Sequence = s.sequence.Add(1)
			err := s.conn.WriteJSON(outbound.message)
			if outbound.ack != nil {
				outbound.ack <- err
			}
			if err != nil {
				return err
			}
		case <-ticker.C:
			requestID, err := auth.RandomToken(12)
			if err != nil {
				return fmt.Errorf("create heartbeat request id: %w", err)
			}
			sentAt := time.Now()
			message, _ := protocol.NewMessage(protocol.TypePing, map[string]string{
				"time": sentAt.UTC().Format(time.RFC3339Nano),
			})
			message.RequestID = requestID
			message.SessionID = s.ID
			message.SessionEpoch = s.Epoch
			message.Sequence = s.sequence.Add(1)
			s.rememberPing(requestID, sentAt)
			if err := s.conn.WriteJSON(message); err != nil {
				return err
			}
		}
	}
}

func (s *Session) AddMux(mux *MuxSession) bool {
	if s.closed.Load() || !s.app.Sessions.IsCurrent(s) {
		return false
	}
	s.muxMu.Lock()
	defer s.muxMu.Unlock()
	if len(s.muxes) >= s.app.Config.DataPlane.SMux.MaxSessions {
		return false
	}
	if _, exists := s.muxes[mux.ID]; exists {
		return false
	}
	if _, reserved := s.pendingMux[mux.ID]; !reserved {
		return false
	}
	delete(s.pendingMux, mux.ID)
	now := time.Now()
	mux.createdAt = now
	mux.lastActive.Store(now.UnixNano())
	s.muxes[mux.ID] = mux
	return true
}

func (s *Session) RemoveMux(id string) {
	s.muxMu.Lock()
	if _, exists := s.muxes[id]; exists {
		delete(s.muxes, id)
		s.app.Sessions.muxTotal.Add(-1)
	}
	s.muxMu.Unlock()
}

func (s *Session) MuxCount() int {
	s.muxMu.RLock()
	defer s.muxMu.RUnlock()
	return len(s.muxes)
}

func (s *Session) Drain(reason string) {
	s.setCloseReason(reason)
	if !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.cancel()
	s.app.Tickets.InvalidateSession(s.ClientID, s.ID, s.Epoch)
	s.app.Tunnels.CancelSession(s)
	s.app.UDP.CloseSession(s, "session_draining")
	s.muxMu.Lock()
	muxes := make([]*MuxSession, 0, len(s.muxes))
	for _, mux := range s.muxes {
		muxes = append(muxes, mux)
	}
	pending := len(s.pendingMux)
	s.pendingMux = make(map[string]struct{})
	s.muxMu.Unlock()
	if pending > 0 {
		s.app.Sessions.muxTotal.Add(-int64(pending))
	}
	for _, mux := range muxes {
		mux.BeginDrain()
	}
	_ = s.conn.Close()
}

func (s *Session) Close(reason string) {
	s.Drain(reason)
	s.app.Tunnels.CloseSession(s)
	s.muxMu.RLock()
	muxes := make([]*MuxSession, 0, len(s.muxes))
	for _, mux := range s.muxes {
		muxes = append(muxes, mux)
	}
	s.muxMu.RUnlock()
	for _, mux := range muxes {
		mux.Close()
	}
	s.persistOffline()
}

func (s *Session) Status() SessionStatus {
	s.muxMu.RLock()
	defer s.muxMu.RUnlock()
	streams := int64(0)
	for _, mux := range s.muxes {
		streams += mux.streams.Load()
	}
	state := "ACTIVE"
	if len(s.muxes) < s.app.Config.DataPlane.SMux.MinSessions {
		state = "DEGRADED"
	}
	if s.closed.Load() {
		state = "DRAINING"
	}
	return SessionStatus{
		ClientID: s.ClientID, SessionID: s.ID, Epoch: s.Epoch,
		State: state, MuxSessions: len(s.muxes), ActiveStreams: streams,
		Agent: s.Agent, AgentStatus: s.AgentStatus,
		SourceAddr: s.SourceAddr, SourceIP: s.SourceIP,
		ConnectedAt: s.ConnectedAt, LastSeenAt: s.lastSeenAt.Load(),
		ControlRTTMillis: s.controlRTTMillis.Load(),
		CloseReason:      s.CloseReason(),
	}
}

func (s *Session) setCloseReason(reason string) {
	if reason == "" {
		reason = "remote_closed"
	}
	copy := reason
	s.closeReason.CompareAndSwap(nil, &copy)
}

func (s *Session) CloseReason() string {
	value := s.closeReason.Load()
	if value == nil {
		return "remote_closed"
	}
	return *value
}

func (s *Session) persistOffline() {
	if !s.offlineSnapshot.CompareAndSwap(false, true) {
		return
	}
	offlineAt := time.Now().UTC().UnixMilli()
	s.app.enqueueAgentSnapshot(agentSnapshot(s, &offlineAt))
}

func (s *Session) Touch() {
	now := time.Now().UTC().UnixMilli()
	s.lastSeenAt.Store(now)
	previous := s.lastSnapshotAt.Load()
	if now-previous < int64(time.Minute/time.Millisecond) ||
		!s.lastSnapshotAt.CompareAndSwap(previous, now) {
		return
	}
	s.app.enqueueAgentSnapshot(agentSnapshot(s, nil))
}

func (s *Session) rememberPing(requestID string, sentAt time.Time) {
	s.pingMu.Lock()
	for id, timestamp := range s.pendingPings {
		if sentAt.Sub(timestamp) > 2*s.app.Config.Duration(s.app.Config.Timeouts.HeartbeatTimeout) {
			delete(s.pendingPings, id)
		}
	}
	s.pendingPings[requestID] = sentAt
	s.pingMu.Unlock()
}

func (s *Session) ObservePong(requestID string) {
	if requestID == "" {
		return
	}
	s.pingMu.Lock()
	sentAt, ok := s.pendingPings[requestID]
	if ok {
		delete(s.pendingPings, requestID)
	}
	s.pingMu.Unlock()
	if ok {
		s.controlRTTMillis.Store(max(time.Since(sentAt).Milliseconds(), 1))
	}
}

func (m *MuxSession) BeginDrain() {
	if !m.draining.CompareAndSwap(false, true) {
		return
	}
	timeout := m.owner.app.Config.Duration(m.owner.app.Config.DataPlane.SMux.DrainTimeout)
	go func() {
		deadline := time.NewTimer(timeout)
		ticker := time.NewTicker(100 * time.Millisecond)
		defer deadline.Stop()
		defer ticker.Stop()
		for {
			select {
			case <-deadline.C:
				m.Close()
				return
			case <-ticker.C:
				if m.streams.Load() == 0 {
					m.Close()
					return
				}
			}
		}
	}()
}

func (m *MuxSession) Close() {
	if m.closed.CompareAndSwap(false, true) {
		_ = m.Session.Close()
		m.owner.RemoveMux(m.ID)
		if m.owner.app.Sessions.IsCurrent(m.owner) && !m.owner.closed.Load() {
			go m.owner.app.ensureMux(m.owner, m.owner.app.Config.DataPlane.SMux.MinSessions)
		}
	}
}

type SessionStatus struct {
	ClientID         string         `json:"client_id"`
	SessionID        string         `json:"session_id"`
	Epoch            uint64         `json:"epoch"`
	State            string         `json:"state"`
	MuxSessions      int            `json:"mux_sessions"`
	ActiveStreams    int64          `json:"active_streams"`
	Agent            protocol.Agent `json:"agent"`
	AgentStatus      string         `json:"agent_status"`
	SourceAddr       string         `json:"source_addr"`
	SourceIP         string         `json:"source_ip"`
	ConnectedAt      int64          `json:"connected_at"`
	LastSeenAt       int64          `json:"last_seen_at"`
	ControlRTTMillis int64          `json:"control_rtt_millis"`
	CloseReason      string         `json:"close_reason"`
}
