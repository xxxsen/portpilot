package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/gorilla/websocket"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

var controlUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	CheckOrigin: func(r *http.Request) bool {
		return r.Header.Get("Origin") == ""
	},
}

func (a *App) handleControl(w http.ResponseWriter, r *http.Request) {
	source := a.requestSource(r)
	if a.ControlLimiter != nil && !a.ControlLimiter.Allow(source) {
		a.recordSecurity(r.Context(), "", "control_rate_limited", map[string]any{"source": source})
		http.Error(w, "rate limited", http.StatusTooManyRequests)
		return
	}
	conn, err := controlUpgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	defer func() { _ = conn.Close() }()
	conn.SetReadLimit(int64(a.Config.Limits.MaxControlMessageBytes))
	_ = conn.SetReadDeadline(time.Now().Add(a.Config.Duration(a.Config.Timeouts.ControlHandshake)))

	clientID, err := a.authenticateControl(conn)
	if err != nil {
		a.recordSecurity(r.Context(), clientID, "client_auth_failed", map[string]any{"source": source})
		a.writeAuthFailure(conn)
		return
	}
	sessionID, err := auth.RandomToken(18)
	if err != nil {
		a.writeAuthFailure(conn)
		return
	}
	session := a.Sessions.Install(clientID, sessionID, conn)
	defer a.Sessions.Remove(session)
	_ = conn.SetReadDeadline(time.Time{})
	if err := conn.WriteJSON(a.authOKMessage(session)); err != nil {
		return
	}
	a.writeAuditBestEffort(r.Context(), clientSessionAudit(
		session, source, "success", "online", "客户端会话上线",
	))
	a.runControlSession(session)
	a.writeAuditBestEffort(context.WithoutCancel(r.Context()), clientSessionAudit(
		session, source, "success", "offline", "客户端会话下线",
	))
}

func clientSessionAudit(
	session *Session,
	source, outcome, reasonCode, summary string,
) store.AuditEvent {
	sessionID := session.ID
	if len(sessionID) > 8 {
		sessionID = sessionID[:8]
	}
	return store.AuditEvent{
		Category: "client", EventType: "client_session", Outcome: outcome,
		ActorType: "client", ActorID: session.ClientID, SourceAddr: source,
		ClientID: session.ClientID, Summary: summary, ReasonCode: reasonCode,
		Details: map[string]any{
			"epoch": session.Epoch, "session_id_prefix": sessionID,
		},
	}
}

func (a *App) authenticateControl(conn *websocket.Conn) (string, error) {
	var initMessage protocol.Message
	if err := conn.ReadJSON(&initMessage); err != nil {
		return "", err
	}
	if initMessage.Version != protocol.Version || initMessage.Type != protocol.TypeAuthInit {
		return "", errors.New(protocol.ErrAuthFailed)
	}
	init, err := protocol.DecodePayload[protocol.AuthInit](initMessage)
	if err != nil || init.ClientID == "" || init.Role != "control" || init.ClientNonce == "" {
		return "", errors.New(protocol.ErrAuthFailed)
	}
	client, ok := a.findClient(init.ClientID)
	if !ok || !client.Enabled {
		return "", errors.New(protocol.ErrAuthFailed)
	}
	challenge, err := a.Challenges.Create(init.ClientID, init.Role, init.ClientNonce)
	if err != nil {
		return "", fmt.Errorf("create authentication challenge: %w", err)
	}
	challengeMessage, _ := protocol.NewMessage(protocol.TypeAuthChallenge, protocol.AuthChallenge{
		ChallengeID: challenge.ID, Challenge: challenge.Value,
		ExpiresAt: challenge.ExpiresAt.UTC().Format(time.RFC3339Nano),
	})
	if err := conn.WriteJSON(challengeMessage); err != nil {
		return "", err
	}
	var proofMessage protocol.Message
	if err := conn.ReadJSON(&proofMessage); err != nil {
		return "", err
	}
	if proofMessage.Version != protocol.Version || proofMessage.Type != protocol.TypeAuthProof {
		return "", errors.New(protocol.ErrAuthFailed)
	}
	proof, err := protocol.DecodePayload[protocol.AuthProof](proofMessage)
	if err != nil || proof.ChallengeID != challenge.ID {
		return "", errors.New(protocol.ErrAuthFailed)
	}
	consumed, ok := a.Challenges.Consume(proof.ChallengeID)
	if !ok || !auth.VerifySignature(client.ClientToken, consumed, proof.Signature) {
		return "", errors.New(protocol.ErrAuthFailed)
	}
	return init.ClientID, nil
}

func (a *App) authOKMessage(session *Session) protocol.Message {
	message, _ := protocol.NewMessage(protocol.TypeAuthOK, protocol.AuthOK{
		SessionID: session.ID, SessionEpoch: session.Epoch,
		Capabilities: protocol.Capabilities{
			DataTransports:      []string{"smux", "direct", "smux-datagram"},
			SMuxProtocolVersion: a.Config.DataPlane.SMux.ProtocolVersion,
		},
		DataPlane: protocol.DataPlane{
			MinSessions: a.Config.DataPlane.SMux.MinSessions, MaxSessions: a.Config.DataPlane.SMux.MaxSessions,
			MaxStreamsPerSession: a.Config.DataPlane.SMux.MaxStreamsPerSession,
			DirectFallback:       a.Config.DataPlane.DirectFallback,
			TargetDial:           a.Config.Timeouts.TargetDial,
			TCPIdle:              a.Config.Timeouts.TCPIdle,
			TCPMaxLifetime:       a.Config.Timeouts.TCPMaxLifetime,
			SMux: protocol.SMuxParameters{
				ProtocolVersion: a.Config.DataPlane.SMux.ProtocolVersion, ScaleUpStreams: a.Config.DataPlane.SMux.ScaleUpStreams,
				SessionMaxAge: a.Config.DataPlane.SMux.SessionMaxAge, DrainTimeout: a.Config.DataPlane.SMux.DrainTimeout,
				KeepAliveInterval: a.Config.DataPlane.SMux.KeepAliveInterval, KeepAliveTimeout: a.Config.DataPlane.SMux.KeepAliveTimeout,
				MaxFrameSize: a.Config.DataPlane.SMux.MaxFrameSize, MaxReceiveBuffer: a.Config.DataPlane.SMux.MaxReceiveBuffer,
				MaxStreamBuffer: a.Config.DataPlane.SMux.MaxStreamBuffer,
			},
			UDP: protocol.UDPParameters{
				ChannelsPerProxy: a.Config.DataPlane.UDP.ChannelsPerProxy, FlowIdleTimeout: a.Config.DataPlane.UDP.FlowIdleTimeout,
				MaxFlowsPerProxy: a.Config.DataPlane.UDP.MaxFlowsPerProxy, MaxDatagramSize: a.Config.DataPlane.UDP.MaxDatagramSize,
				ChannelQueueDatagrams: a.Config.DataPlane.UDP.ChannelQueueDatagrams,
			},
		},
	})
	message.SessionID = session.ID
	message.SessionEpoch = session.Epoch
	return message
}

func (a *App) runControlSession(session *Session) {
	writerErr := make(chan error, 1)
	go func() { writerErr <- session.writeLoop() }()
	a.ensureMux(session, a.Config.DataPlane.SMux.MinSessions)
	readErr := make(chan error, 1)
	go func() { readErr <- a.controlReadLoop(session) }()
	select {
	case err := <-writerErr:
		if err != nil {
			a.Logger.Info(
				"control writer stopped",
				zap.String("client_id", session.ClientID),
				zap.Error(err),
			)
		}
	case err := <-readErr:
		if err != nil && !errors.Is(err, websocket.ErrCloseSent) {
			a.Logger.Info(
				"control reader stopped",
				zap.String("client_id", session.ClientID),
				zap.Error(err),
			)
		}
	case <-session.ctx.Done():
	}
}

func (a *App) controlReadLoop(session *Session) error {
	timeout := a.Config.Duration(a.Config.Timeouts.HeartbeatTimeout)
	for {
		_ = session.conn.SetReadDeadline(time.Now().Add(timeout))
		var message protocol.Message
		if err := session.conn.ReadJSON(&message); err != nil {
			return err
		}
		if message.Version != protocol.Version || message.SessionID != session.ID ||
			message.SessionEpoch != session.Epoch || !a.Sessions.IsCurrent(session) {
			return errors.New(protocol.ErrSessionStale)
		}
		switch message.Type {
		case protocol.TypePong:
		case protocol.TypeMuxReady:
			a.UDP.EnsureChannels(session)
		case protocol.TypeMuxClosed:
			a.ensureMux(session, a.Config.DataPlane.SMux.MinSessions)
		case protocol.TypeUDPChannelClosed:
			a.UDP.EnsureChannels(session)
		case protocol.TypeOpenResult:
			result, err := protocol.DecodePayload[protocol.OpenResult](message)
			if err != nil {
				return err
			}
			a.Tunnels.HandleOpenResult(result)
		case protocol.TypeUDPChannelReady, protocol.TypeTunnelClosed:
		default:
			errorMessage, _ := protocol.NewMessage(protocol.TypeError, protocol.ErrorPayload{Code: protocol.ErrInternal, Message: "unknown control message"})
			_ = session.Send(errorMessage)
		}
	}
}

func (a *App) requestMux(session *Session) error {
	muxID, err := auth.RandomToken(18)
	if err != nil {
		return fmt.Errorf("create mux identifier: %w", err)
	}
	if !session.ReserveMux(muxID) {
		return errors.New(protocol.ErrMuxSessionLimit)
	}
	release := true
	defer func() {
		if release {
			session.ReleaseMux(muxID)
		}
	}()
	ticket, err := a.Tickets.Issue(auth.TicketBinding{
		ClientID: session.ClientID, SessionID: session.ID,
		SessionEpoch: session.Epoch, Kind: "mux", ObjectID: muxID,
	})
	if err != nil {
		return fmt.Errorf("issue mux ticket: %w", err)
	}
	message, _ := protocol.NewMessage(protocol.TypeCreateMux, protocol.CreateMux{
		MuxID: muxID, MuxTicket: ticket,
		ExpiresAt: time.Now().Add(a.Config.Duration(a.Config.Timeouts.DataConnect)).UTC().Format(time.RFC3339Nano),
	})
	if err := session.Send(message); err != nil {
		return err
	}
	release = false
	go func() {
		timer := time.NewTimer(a.Config.Duration(a.Config.Timeouts.DataConnect))
		defer timer.Stop()
		select {
		case <-session.ctx.Done():
		case <-timer.C:
			session.ReleaseMux(muxID)
			a.ensureMux(session, a.Config.DataPlane.SMux.MinSessions)
		}
	}()
	return nil
}

func (a *App) writeAuthFailure(conn *websocket.Conn) {
	message, _ := protocol.NewMessage(protocol.TypeAuthError, protocol.ErrorPayload{Code: protocol.ErrAuthFailed})
	_ = conn.WriteJSON(message)
}

func (a *App) findClient(clientID string) (config.ClientConfig, bool) {
	for _, client := range a.ConfigManager.Snapshot().ClientConfig {
		if client.ClientID == clientID {
			return client, true
		}
	}
	return config.ClientConfig{}, false
}
