package server

import (
	"context"
	"net"
	"net/http"
	"time"

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/transport"
	"github.com/xtaci/smux"
)

func (a *App) handleMuxUpgrade(w http.ResponseWriter, r *http.Request) {
	if !a.allowDataRequest(w, r) {
		return
	}
	conn, err := transport.AcceptUpgrade(w, r)
	if err != nil {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(a.Config.Duration(a.Config.Timeouts.ControlHandshake)))
	var handshake protocol.MuxAuth
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &handshake); err != nil {
		return
	}
	session := a.Sessions.Current(handshake.ClientID)
	binding := auth.TicketBinding{
		ClientID: handshake.ClientID, SessionID: handshake.SessionID,
		SessionEpoch: handshake.SessionEpoch, Kind: "mux", ObjectID: handshake.MuxID,
	}
	if handshake.Version != protocol.Version || handshake.Role != "mux" ||
		session == nil || session.ID != handshake.SessionID || session.Epoch != handshake.SessionEpoch ||
		!a.Tickets.Consume(handshake.MuxTicket, binding) {
		a.recordSecurity(r.Context(), handshake.ClientID, "mux_ticket_rejected", map[string]any{"mux_id": handshake.MuxID, "session_epoch": handshake.SessionEpoch})
		_ = protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "MUX_ERROR", Code: protocol.ErrMuxTicketInvalid})
		return
	}
	if err := protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "MUX_OK"}); err != nil {
		return
	}
	_ = conn.SetDeadline(time.Time{})
	smuxSession, err := smux.Server(conn, a.smuxConfig())
	if err != nil {
		return
	}
	mux := &MuxSession{ID: handshake.MuxID, Session: smuxSession, owner: session}
	if !session.AddMux(mux) {
		session.ReleaseMux(handshake.MuxID)
		_ = smuxSession.Close()
		return
	}
	defer mux.Close()
	go a.monitorMux(mux)
	a.acceptMuxStreams(r.Context(), mux)
}

func (a *App) acceptMuxStreams(ctx context.Context, mux *MuxSession) {
	for {
		stream, err := mux.Session.AcceptStream()
		if err != nil {
			return
		}
		if mux.draining.Load() || !a.Sessions.IsCurrent(mux.owner) {
			_ = stream.Close()
			continue
		}
		mux.streams.Add(1)
		mux.lastActive.Store(time.Now().UnixNano())
		if mux.streams.Load() >= int64(a.Config.DataPlane.SMux.ScaleUpStreams) {
			active, _ := mux.owner.MuxCapacity()
			go a.ensureMux(mux.owner, active+1)
		}
		go func(conn net.Conn) {
			defer func() {
				mux.streams.Add(-1)
				mux.lastActive.Store(time.Now().UnixNano())
			}()
			defer func() { _ = conn.Close() }()
			a.handleMuxStream(ctx, mux, conn)
		}(stream)
	}
}

func (a *App) handleMuxStream(ctx context.Context, mux *MuxSession, conn net.Conn) {
	_ = conn.SetDeadline(time.Now().Add(a.Config.Duration(a.Config.Timeouts.DataConnect)))
	var open protocol.StreamOpen
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &open); err != nil {
		return
	}
	if open.Version != protocol.Version || open.ClientID != mux.owner.ClientID ||
		open.SessionID != mux.owner.ID || open.SessionEpoch != mux.owner.Epoch ||
		open.MuxID != mux.ID {
		_ = protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_ERROR", Code: protocol.ErrSessionStale})
		return
	}
	switch open.Role {
	case "tcp-stream":
		pending, ok := a.Tunnels.Pair(ctx, open, conn)
		if !ok {
			_ = protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_ERROR", Code: protocol.ErrDataTicketInvalid})
			return
		}
		if err := protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_OK"}); err != nil {
			_ = pending.External.Close()
			return
		}
		_ = conn.SetDeadline(time.Time{})
		a.Tunnels.Relay(ctx, pending, conn)
	case "udp-channel":
		channel, ok := a.UDP.Pair(ctx, open, conn)
		if !ok {
			_ = protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_ERROR", Code: protocol.ErrUDPChannelInvalid})
			return
		}
		if err := protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_OK"}); err != nil {
			channel.Close()
			return
		}
		_ = conn.SetDeadline(time.Time{})
		a.UDP.RunChannel(channel)
	default:
		_ = protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_ERROR", Code: protocol.ErrInternal})
	}
}

func (a *App) handleDirectUpgrade(w http.ResponseWriter, r *http.Request) {
	if !a.allowDataRequest(w, r) {
		return
	}
	conn, err := transport.AcceptUpgrade(w, r)
	if err != nil {
		http.Error(w, "upgrade required", http.StatusUpgradeRequired)
		return
	}
	defer func() { _ = conn.Close() }()
	_ = conn.SetDeadline(time.Now().Add(a.Config.Duration(a.Config.Timeouts.DataConnect)))
	var open protocol.StreamOpen
	if err := protocol.ReadJSONFrame(conn, protocol.DefaultMaxJSONFrame, &open); err != nil {
		return
	}
	if open.Role != "direct" {
		return
	}
	pending, ok := a.Tunnels.Pair(r.Context(), open, conn)
	if !ok {
		_ = protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_ERROR", Code: protocol.ErrDataTicketInvalid})
		return
	}
	if err := protocol.WriteJSONFrame(conn, protocol.HandshakeResult{Version: protocol.Version, Type: "STREAM_OK"}); err != nil {
		_ = pending.External.Close()
		return
	}
	_ = conn.SetDeadline(time.Time{})
	a.Tunnels.Relay(r.Context(), pending, conn)
}

func (a *App) smuxConfig() *smux.Config {
	cfg := smux.DefaultConfig()
	cfg.Version = a.Config.DataPlane.SMux.ProtocolVersion
	cfg.KeepAliveInterval = a.Config.Duration(a.Config.DataPlane.SMux.KeepAliveInterval)
	cfg.KeepAliveTimeout = a.Config.Duration(a.Config.DataPlane.SMux.KeepAliveTimeout)
	cfg.MaxFrameSize = a.Config.DataPlane.SMux.MaxFrameSize
	cfg.MaxReceiveBuffer = a.Config.DataPlane.SMux.MaxReceiveBuffer
	cfg.MaxStreamBuffer = a.Config.DataPlane.SMux.MaxStreamBuffer
	return cfg
}
