package server

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/proxyproto"
	"github.com/portpilot/portpilot/internal/ratelimit"
	"github.com/portpilot/portpilot/internal/relay"
	"go.uber.org/zap"
)

type pendingTunnel struct {
	ID                   string
	ClientID             string
	ProxyID              string
	SessionID            string
	Epoch                uint64
	External             net.Conn
	Data                 net.Conn
	CreatedAt            time.Time
	Activity             *activity.Handle
	Limits               *ratelimit.Pair
	ProxyProtocolEnabled bool
	ProxyProtocolHeader  []byte
	ProxyProtocolError   error
	done                 chan struct{}
	closed               atomic.Bool
}

type TunnelManager struct {
	app            *App
	mu             sync.Mutex
	pending        map[string]*pendingTunnel
	activeTunnels  map[string]*pendingTunnel
	activeByClient map[string]int
	active         atomic.Int64
}

var errActivityDisabled = errors.New("network activity service is disabled")

func NewTunnelManager(app *App) *TunnelManager {
	return &TunnelManager{
		app: app, pending: make(map[string]*pendingTunnel),
		activeTunnels:  make(map[string]*pendingTunnel),
		activeByClient: make(map[string]int),
	}
}

func (m *TunnelManager) HandleExternal(ctx context.Context, runtime ProxyRuntime, conn net.Conn) {
	activityHandle, err := m.startActivity(runtime, conn)
	if err != nil && !errors.Is(err, errActivityDisabled) {
		_ = conn.Close()
		m.app.recordSecurity(ctx, runtime.ClientID, "network_activity_limit", map[string]any{
			"proxy_id": runtime.Proxy.ProxyID, "source": conn.RemoteAddr().String(),
		})
		return
	}
	session := m.app.Sessions.Current(runtime.ClientID)
	if session == nil || session.closed.Load() {
		_ = conn.Close()
		if activityHandle != nil {
			activityHandle.Fail("client_offline", "client is offline")
		}
		return
	}
	m.mu.Lock()
	if len(m.pending) >= m.app.Config.Limits.MaxPendingTunnels ||
		m.clientTunnelCountLocked(runtime.ClientID) >= m.app.Config.Limits.MaxTunnelsPerClient {
		m.mu.Unlock()
		_ = conn.Close()
		if activityHandle != nil {
			activityHandle.Fail("resource_limit", "tunnel resource limit reached")
		}
		return
	}
	connectionID := ""
	if activityHandle != nil {
		connectionID = activityHandle.ID()
	} else {
		connectionID, err = auth.RandomToken(18)
		if err != nil {
			m.mu.Unlock()
			_ = conn.Close()
			return
		}
	}
	ticket, err := m.app.Tickets.Issue(auth.TicketBinding{
		ClientID: runtime.ClientID, SessionID: session.ID,
		SessionEpoch: session.Epoch, Kind: "tcp", ObjectID: connectionID,
	})
	if err != nil {
		m.mu.Unlock()
		_ = conn.Close()
		if activityHandle != nil {
			activityHandle.Fail("ticket_issue_failed", "could not issue data ticket")
		}
		return
	}
	pending := &pendingTunnel{
		ID: connectionID, ClientID: runtime.ClientID, ProxyID: runtime.Proxy.ProxyID,
		SessionID: session.ID, Epoch: session.Epoch, External: conn,
		CreatedAt: time.Now(), Activity: activityHandle,
		Limits: ratelimit.NewPair(
			runtime.Data.RateLimit.UpBytesPerSecond,
			runtime.Data.RateLimit.DownBytesPerSecond,
			false, 0,
		),
		ProxyProtocolEnabled: runtime.Data.EnableProxyProtocol,
		done:                 make(chan struct{}),
	}
	if pending.ProxyProtocolEnabled {
		pending.ProxyProtocolHeader, pending.ProxyProtocolError = proxyproto.V1(
			conn.RemoteAddr(), conn.LocalAddr(),
		)
	}
	if activityHandle != nil {
		activityHandle.Connecting()
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

func (m *TunnelManager) Pair(ctx context.Context, open protocol.StreamOpen, dataConn net.Conn) (*pendingTunnel, bool) {
	binding := auth.TicketBinding{
		ClientID: open.ClientID, SessionID: open.SessionID,
		SessionEpoch: open.SessionEpoch, Kind: "tcp", ObjectID: open.ConnectionID,
	}
	if !m.app.Tickets.Consume(open.DataTicket, binding) {
		m.app.recordSecurity(ctx, open.ClientID, "tcp_ticket_rejected", map[string]any{"connection_id": open.ConnectionID, "session_epoch": open.SessionEpoch})
		return nil, false
	}
	m.mu.Lock()
	pending := m.pending[open.ConnectionID]
	if pending == nil || pending.ClientID != open.ClientID ||
		pending.SessionID != open.SessionID || pending.Epoch != open.SessionEpoch {
		m.app.recordSecurity(ctx, open.ClientID, "tcp_pair_rejected", map[string]any{"connection_id": open.ConnectionID, "session_epoch": open.SessionEpoch})
		m.mu.Unlock()
		return nil, false
	}
	runtime, runtimeExists := m.app.Listeners.Runtime(pending.ClientID, pending.ProxyID)
	decision := externalAccessDecision{Reason: "proxy_unavailable"}
	if runtimeExists {
		decision = m.app.allowExternal(runtime, pending.External.RemoteAddr())
	}
	if !decision.Allowed {
		delete(m.pending, open.ConnectionID)
		signalTunnelDone(pending)
		m.mu.Unlock()
		if pending.Activity != nil {
			pending.Activity.Fail(decision.Reason, "external source no longer allowed")
		}
		_ = pending.External.Close()
		if runtimeExists {
			m.app.recordExternalAccessDenied(runtime, "tcp", decision, 0)
		}
		return nil, false
	}
	delete(m.pending, open.ConnectionID)
	pending.Data = dataConn
	if pending.Activity != nil {
		if open.Role == "direct" {
			pending.Activity.SetTransport("direct")
		} else {
			pending.Activity.SetTransport("smux")
		}
	}
	m.activeTunnels[pending.ID] = pending
	signalTunnelDone(pending)
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
	if err := m.writeProxyProtocolHeader(pending, dataConn); err != nil {
		_ = pending.External.Close()
		_ = dataConn.Close()
		if pending.Activity != nil {
			pending.Activity.Fail(
				"proxy_protocol_write_failed",
				"could not send PROXY protocol header",
			)
		}
		if m.app.Logger != nil {
			m.app.Logger.Error(
				"write PROXY protocol header failed",
				zap.String("client_id", pending.ClientID),
				zap.String("proxy_id", pending.ProxyID),
				zap.String("connection_id", pending.ID),
				zap.String("stage", "before_payload"),
				zap.Error(err),
			)
		}
		return
	}
	if pending.Activity != nil {
		pending.Activity.Connected()
	}
	result := relay.BidirectionalObservedWrapped(
		relayCtx, pending.External, dataConn,
		m.app.Config.Duration(m.app.Config.Timeouts.TCPIdle),
		func(bytes int) {
			if pending.Activity != nil {
				pending.Activity.AddUp(bytes, 0)
			}
		},
		func(bytes int) {
			if pending.Activity != nil {
				pending.Activity.AddDown(bytes, 0)
			}
		},
		ratelimit.WrapWriter(relayCtx, pending.Limits.Up, func(waited time.Duration) {
			if pending.Activity != nil {
				pending.Activity.AddUpThrottled(waited)
			}
		}),
		ratelimit.WrapWriter(relayCtx, pending.Limits.Down, func(waited time.Duration) {
			if pending.Activity != nil {
				pending.Activity.AddDownThrottled(waited)
			}
		}),
	)
	reason := "closed"
	if result.Err != nil && !errors.Is(result.Err, net.ErrClosed) {
		reason = "relay_error"
	}
	if pending.Activity != nil {
		pending.Activity.Close(reason, reason)
	}
}

func (m *TunnelManager) writeProxyProtocolHeader(
	pending *pendingTunnel,
	dataConn net.Conn,
) error {
	if !pending.ProxyProtocolEnabled {
		return nil
	}
	if pending.ProxyProtocolError != nil {
		return pending.ProxyProtocolError
	}
	deadline := time.Now().Add(m.app.Config.Duration(m.app.Config.Timeouts.DataConnect))
	if err := dataConn.SetWriteDeadline(deadline); err != nil {
		return fmt.Errorf("set PROXY protocol write deadline: %w", err)
	}
	written := 0
	for written < len(pending.ProxyProtocolHeader) {
		count, err := dataConn.Write(pending.ProxyProtocolHeader[written:])
		written += count
		if err != nil {
			return fmt.Errorf("write PROXY protocol header: %w", err)
		}
		if count == 0 {
			return io.ErrShortWrite
		}
	}
	if err := dataConn.SetWriteDeadline(time.Time{}); err != nil {
		return fmt.Errorf("clear PROXY protocol write deadline: %w", err)
	}
	if pending.Activity != nil {
		pending.Activity.MarkProxyProtocolHeaderSent()
	}
	return nil
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
			signalTunnelDone(pending)
			_ = pending.External.Close()
			if pending.Activity != nil {
				pending.Activity.Fail("session_closed", "client session closed before pairing")
			}
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
		signalTunnelDone(pending)
		_ = pending.External.Close()
		if pending.Activity != nil {
			pending.Activity.Fail(reason, reason)
		}
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

func (m *TunnelManager) Reconcile() {
	runtimes := make(map[string]ProxyRuntime)
	for _, runtime := range m.app.Listeners.Snapshot() {
		if runtime.Data.Network == "tcp" {
			runtimes[proxyRuntimeKey(runtime.ClientID, runtime.Proxy.ProxyID)] = runtime
		}
	}
	var closeItems []*pendingTunnel
	m.mu.Lock()
	for id, tunnel := range m.pending {
		runtime, ok := runtimes[proxyRuntimeKey(tunnel.ClientID, tunnel.ProxyID)]
		allowed := ok
		if allowed {
			allowed = m.app.allowExternal(runtime, tunnel.External.RemoteAddr()).Allowed
		}
		if !allowed {
			delete(m.pending, id)
			closeItems = append(closeItems, tunnel)
			continue
		}
		_ = tunnel.Limits.Up.Update(runtime.Data.RateLimit.UpBytesPerSecond, false, 0)
		_ = tunnel.Limits.Down.Update(runtime.Data.RateLimit.DownBytesPerSecond, false, 0)
		if tunnel.Activity != nil {
			tunnel.Activity.SetRateLimits(
				runtime.Data.RateLimit.UpBytesPerSecond,
				runtime.Data.RateLimit.DownBytesPerSecond,
			)
		}
	}
	for _, tunnel := range m.activeTunnels {
		runtime, ok := runtimes[proxyRuntimeKey(tunnel.ClientID, tunnel.ProxyID)]
		allowed := ok
		if allowed {
			allowed = m.app.allowExternal(runtime, tunnel.External.RemoteAddr()).Allowed
		}
		if !allowed {
			closeItems = append(closeItems, tunnel)
			continue
		}
		_ = tunnel.Limits.Up.Update(runtime.Data.RateLimit.UpBytesPerSecond, false, 0)
		_ = tunnel.Limits.Down.Update(runtime.Data.RateLimit.DownBytesPerSecond, false, 0)
		if tunnel.Activity != nil {
			tunnel.Activity.SetRateLimits(
				runtime.Data.RateLimit.UpBytesPerSecond,
				runtime.Data.RateLimit.DownBytesPerSecond,
			)
		}
	}
	m.mu.Unlock()
	for _, tunnel := range closeItems {
		if tunnel.Activity != nil {
			tunnel.Activity.Close("source_policy_changed", "external source no longer allowed")
		}
		_ = tunnel.External.Close()
		if tunnel.Data != nil {
			_ = tunnel.Data.Close()
		}
		if tunnel.closed.CompareAndSwap(false, true) {
			signalTunnelDone(tunnel)
		}
	}
}

func signalTunnelDone(tunnel *pendingTunnel) {
	select {
	case <-tunnel.done:
	default:
		close(tunnel.done)
	}
}

func (m *TunnelManager) startActivity(runtime ProxyRuntime, conn net.Conn) (*activity.Handle, error) {
	if m.app.Activity == nil {
		return nil, errActivityDisabled
	}
	epoch := m.app.Sessions.Epoch(runtime.ClientID)
	handle, err := m.app.Activity.Start(activity.Start{
		Protocol: "tcp", ClientID: runtime.ClientID, ClientRemark: runtime.ClientRemark,
		ProxyID: runtime.Proxy.ProxyID, ProxyRemark: runtime.Proxy.Remark,
		ExternalAddr: conn.RemoteAddr().String(), ServerBind: runtime.Data.Bind,
		TargetAddr: runtime.Data.Connect, Transport: runtime.Data.Transport,
		SessionEpoch:            epoch,
		UpLimitBytesPerSecond:   runtime.Data.RateLimit.UpBytesPerSecond,
		DownLimitBytesPerSecond: runtime.Data.RateLimit.DownBytesPerSecond,
		ProxyProtocolEnabled:    runtime.Data.EnableProxyProtocol,
	})
	if err != nil {
		return nil, fmt.Errorf("start TCP network activity: %w", err)
	}
	return handle, nil
}
