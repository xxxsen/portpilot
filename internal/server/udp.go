package server

import (
	"context"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/ratelimit"
	"go.uber.org/zap"
)

type UDPChannel struct {
	ID        string
	ProxyID   string
	ClientID  string
	SessionID string
	Connect   string
	Epoch     uint64
	Conn      net.Conn
	send      chan protocol.UDPFrame
	done      chan struct{}
	closed    atomic.Bool
	manager   *UDPManager
}

type udpPendingChannel struct {
	ProxyID   string
	ClientID  string
	SessionID string
	Connect   string
	Epoch     uint64
}

type udpFlow struct {
	ID         uint64
	ProxyID    string
	ClientID   string
	SessionID  string
	Connect    string
	Epoch      uint64
	Source     *net.UDPAddr
	Listener   *net.UDPConn
	ChannelID  string
	LastActive atomic.Int64
	Activity   *activity.Handle
	Limits     *ratelimit.Pair
}

type UDPManager struct {
	app      *App
	mu       sync.RWMutex
	channels map[string]map[string]*UDPChannel
	pending  map[string]udpPendingChannel
	bySource map[string]*udpFlow
	byID     map[string]*udpFlow
	nextFlow atomic.Uint64
	stop     chan struct{}
	stopOnce sync.Once
}

func NewUDPManager(app *App) *UDPManager {
	manager := &UDPManager{
		app: app, channels: make(map[string]map[string]*UDPChannel),
		pending:  make(map[string]udpPendingChannel),
		bySource: make(map[string]*udpFlow), byID: make(map[string]*udpFlow),
		stop: make(chan struct{}),
	}
	go manager.reaper()
	return manager
}

func (m *UDPManager) EnsureChannels(session *Session) {
	if session == nil || session.closed.Load() || !m.app.Sessions.IsCurrent(session) {
		return
	}
	for _, runtime := range m.app.Listeners.Snapshot() {
		if runtime.ClientID != session.ClientID || runtime.Data.Network != "udp" {
			continue
		}
		for {
			channelID, err := auth.RandomToken(18)
			if err != nil || !m.reserveChannel(session, runtime.Proxy.ProxyID, runtime.Data.Connect, channelID) {
				break
			}
			ticket, err := m.app.Tickets.Issue(auth.TicketBinding{
				ClientID: session.ClientID, SessionID: session.ID,
				SessionEpoch: session.Epoch, Kind: "udp", ObjectID: channelID,
			})
			if err != nil {
				m.releasePending(channelID)
				break
			}
			message, _ := protocol.NewMessage(protocol.TypeOpenUDPChannel, protocol.OpenUDPChannel{
				ProxyID: runtime.Proxy.ProxyID, ChannelID: channelID,
				Connect: runtime.Data.Connect, ChannelTicket: ticket,
			})
			if err := session.Send(message); err != nil {
				m.releasePending(channelID)
				return
			}
			go m.expirePending(session, channelID)
		}
	}
}

func (m *UDPManager) reserveChannel(session *Session, proxyID, connect, channelID string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	count := 0
	for _, channel := range m.channels[proxyRuntimeKey(session.ClientID, proxyID)] {
		if channel.Epoch == session.Epoch && channel.SessionID == session.ID && channel.Connect == connect && !channel.closed.Load() {
			count++
		}
	}
	for _, pending := range m.pending {
		if pending.ProxyID == proxyID && pending.SessionID == session.ID && pending.Epoch == session.Epoch && pending.Connect == connect {
			count++
		}
	}
	if count >= m.app.Config.DataPlane.UDP.ChannelsPerProxy {
		return false
	}
	m.pending[channelID] = udpPendingChannel{
		ProxyID: proxyID, ClientID: session.ClientID,
		SessionID: session.ID, Connect: connect, Epoch: session.Epoch,
	}
	return true
}

func (m *UDPManager) releasePending(channelID string) {
	m.mu.Lock()
	delete(m.pending, channelID)
	m.mu.Unlock()
}

func (m *UDPManager) expirePending(session *Session, channelID string) {
	timer := time.NewTimer(m.app.Config.Duration(m.app.Config.Timeouts.DataConnect))
	defer timer.Stop()
	select {
	case <-m.stop:
	case <-session.ctx.Done():
		m.releasePending(channelID)
	case <-timer.C:
		m.releasePending(channelID)
		m.EnsureChannels(session)
	}
}

func (m *UDPManager) Pair(ctx context.Context, open protocol.StreamOpen, conn net.Conn) (*UDPChannel, bool) {
	binding := auth.TicketBinding{
		ClientID: open.ClientID, SessionID: open.SessionID,
		SessionEpoch: open.SessionEpoch, Kind: "udp", ObjectID: open.ChannelID,
	}
	if !m.app.Tickets.Consume(open.ChannelTicket, binding) {
		m.app.recordSecurity(ctx, open.ClientID, "udp_ticket_rejected", map[string]any{"channel_id": open.ChannelID, "proxy_id": open.ProxyID, "session_epoch": open.SessionEpoch})
		return nil, false
	}
	session := m.app.Sessions.Current(open.ClientID)
	if session == nil || session.ID != open.SessionID || session.Epoch != open.SessionEpoch {
		m.app.recordSecurity(ctx, open.ClientID, "udp_pair_rejected", map[string]any{"channel_id": open.ChannelID, "proxy_id": open.ProxyID, "session_epoch": open.SessionEpoch})
		return nil, false
	}
	validProxy := false
	for _, runtime := range m.app.Listeners.Snapshot() {
		if runtime.Proxy.ProxyID == open.ProxyID && runtime.ClientID == open.ClientID && runtime.Data.Network == "udp" {
			validProxy = true
			break
		}
	}
	if !validProxy {
		m.app.recordSecurity(ctx, open.ClientID, "udp_proxy_rejected", map[string]any{"channel_id": open.ChannelID, "proxy_id": open.ProxyID, "session_epoch": open.SessionEpoch})
		return nil, false
	}
	channel := &UDPChannel{
		ID: open.ChannelID, ProxyID: open.ProxyID, ClientID: open.ClientID,
		SessionID: open.SessionID, Epoch: open.SessionEpoch, Conn: conn, manager: m,
		send: make(chan protocol.UDPFrame, m.app.Config.DataPlane.UDP.ChannelQueueDatagrams),
		done: make(chan struct{}),
	}
	m.mu.Lock()
	pending, reserved := m.pending[open.ChannelID]
	if !reserved || pending.ProxyID != open.ProxyID || pending.ClientID != open.ClientID ||
		pending.SessionID != open.SessionID || pending.Epoch != open.SessionEpoch {
		m.mu.Unlock()
		return nil, false
	}
	delete(m.pending, open.ChannelID)
	channel.Connect = pending.Connect
	if m.channels[proxyRuntimeKey(open.ClientID, open.ProxyID)] == nil {
		m.channels[proxyRuntimeKey(open.ClientID, open.ProxyID)] = make(map[string]*UDPChannel)
	}
	if _, exists := m.channels[proxyRuntimeKey(open.ClientID, open.ProxyID)][open.ChannelID]; exists {
		m.mu.Unlock()
		return nil, false
	}
	m.channels[proxyRuntimeKey(open.ClientID, open.ProxyID)][open.ChannelID] = channel
	m.mu.Unlock()
	return channel, true
}

func (m *UDPManager) RunChannel(channel *UDPChannel) {
	defer channel.Close()
	go channel.writeLoop()
	max := m.app.Config.DataPlane.UDP.MaxDatagramSize
	for {
		frame, err := protocol.ReadUDPFrame(channel.Conn, max)
		if err != nil {
			if !errors.Is(err, io.EOF) && !channel.closed.Load() {
				m.app.Logger.Warn(
					"UDP channel read failed",
					zap.String("proxy_id", channel.ProxyID),
					zap.Error(err),
				)
			}
			return
		}
		switch frame.Type {
		case protocol.UDPTypeDataToServer:
			m.writeResponse(channel, frame)
		case protocol.UDPTypeFlowClose:
			m.removeFlow(channel.ClientID, channel.ProxyID, channel.Epoch, frame.FlowID, "client_flow_close")
		default:
			return
		}
	}
}

func (m *UDPManager) HandleDatagram(runtime ProxyRuntime, listener *net.UDPConn, source *net.UDPAddr, data []byte) {
	session := m.app.Sessions.Current(runtime.ClientID)
	if session == nil || session.closed.Load() {
		m.recordDrop(runtime, source, len(data), "client_offline")
		return
	}
	sourceKey := sourceFlowKey(runtime.ClientID, runtime.Proxy.ProxyID, session.Epoch, source.String())
	m.mu.Lock()
	flow, created := m.findOrCreateFlowLocked(runtime, listener, source, sourceKey, session)
	if flow == nil {
		m.mu.Unlock()
		m.recordDrop(runtime, source, len(data), "flow_limit")
		return
	}
	channel := m.channelForFlowLocked(flow, runtime, session, sourceKey)
	if channel == nil {
		m.removeNewFlowLocked(flow, sourceKey, created)
		m.mu.Unlock()
		m.recordDrop(runtime, source, len(data), "no_udp_channel")
		return
	}
	if created && !m.startFlowActivityLocked(flow, runtime, listener, source, session) {
		m.removeNewFlowLocked(flow, sourceKey, true)
		m.mu.Unlock()
		m.recordDrop(runtime, source, len(data), "network_activity_limit")
		return
	}
	flow.LastActive.Store(time.Now().UnixNano())
	frame := protocol.UDPFrame{
		Type: protocol.UDPTypeDataToClient, FlowID: flow.ID,
		Addr: flow.Source, Data: data,
	}
	if !flow.Limits.Up.AllowN(len(data)) {
		if flow.Activity != nil {
			flow.Activity.AddUpRateLimited(len(data))
		}
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	select {
	case channel.send <- frame:
		if flow.Activity != nil {
			flow.Activity.AddUp(len(data), 1)
		}
	default:
		m.recordDrop(runtime, source, len(data), "queue_full")
	}
}

func (m *UDPManager) findOrCreateFlowLocked(
	runtime ProxyRuntime,
	listener *net.UDPConn,
	source *net.UDPAddr,
	sourceKey string,
	session *Session,
) (*udpFlow, bool) {
	if flow := m.bySource[sourceKey]; flow != nil {
		return flow, false
	}
	if m.flowCountLocked(runtime.ClientID, runtime.Proxy.ProxyID, session.Epoch) >= m.app.Config.DataPlane.UDP.MaxFlowsPerProxy ||
		len(m.byID) >= m.app.Config.Limits.MaxUDPFlowsTotal {
		return nil, false
	}
	flow := &udpFlow{
		ID: m.nextFlow.Add(1), ProxyID: runtime.Proxy.ProxyID, ClientID: runtime.ClientID,
		SessionID: session.ID, Connect: runtime.Data.Connect, Epoch: session.Epoch,
		Source: cloneUDPAddr(source), Listener: listener,
		Limits: ratelimit.NewPair(
			runtime.Data.RateLimit.UpBytesPerSecond,
			runtime.Data.RateLimit.DownBytesPerSecond,
			true, m.app.Config.DataPlane.UDP.MaxDatagramSize,
		),
	}
	m.bySource[sourceKey] = flow
	m.byID[flowIDKey(flow.ClientID, flow.ProxyID, flow.Epoch, flow.ID)] = flow
	return flow, true
}

func (m *UDPManager) channelForFlowLocked(
	flow *udpFlow,
	runtime ProxyRuntime,
	session *Session,
	sourceKey string,
) *UDPChannel {
	channel := m.channels[proxyRuntimeKey(flow.ClientID, flow.ProxyID)][flow.ChannelID]
	if channel != nil && !channel.closed.Load() && channel.ClientID == runtime.ClientID &&
		channel.SessionID == session.ID && channel.Epoch == session.Epoch && channel.Connect == flow.Connect {
		return channel
	}
	available := make([]*UDPChannel, 0, len(m.channels[proxyRuntimeKey(flow.ClientID, flow.ProxyID)]))
	for _, candidate := range m.channels[proxyRuntimeKey(flow.ClientID, flow.ProxyID)] {
		if candidate.ClientID == runtime.ClientID && candidate.SessionID == session.ID &&
			candidate.Epoch == session.Epoch && candidate.Connect == flow.Connect && !candidate.closed.Load() {
			available = append(available, candidate)
		}
	}
	if len(available) == 0 {
		return nil
	}
	channel = available[hashSource(sourceKey)%uint64(len(available))]
	flow.ChannelID = channel.ID
	return channel
}

func (m *UDPManager) startFlowActivityLocked(
	flow *udpFlow,
	runtime ProxyRuntime,
	listener *net.UDPConn,
	source *net.UDPAddr,
	session *Session,
) bool {
	if m.app.Activity == nil {
		return true
	}
	activityHandle, err := m.app.Activity.Start(activity.Start{
		Protocol: "udp", ClientID: runtime.ClientID, ClientRemark: runtime.ClientRemark,
		ProxyID: runtime.Proxy.ProxyID, ProxyRemark: runtime.Proxy.Remark,
		ExternalAddr: source.String(), ServerBind: listener.LocalAddr().String(),
		TargetAddr: runtime.Data.Connect, Transport: "smux",
		SessionEpoch:            session.Epoch,
		UpLimitBytesPerSecond:   runtime.Data.RateLimit.UpBytesPerSecond,
		DownLimitBytesPerSecond: runtime.Data.RateLimit.DownBytesPerSecond,
	})
	if err != nil {
		return false
	}
	flow.Activity = activityHandle
	flow.Activity.Connected()
	return true
}

func (m *UDPManager) removeNewFlowLocked(flow *udpFlow, sourceKey string, created bool) {
	if !created {
		return
	}
	delete(m.bySource, sourceKey)
	delete(m.byID, flowIDKey(flow.ClientID, flow.ProxyID, flow.Epoch, flow.ID))
}

func (c *UDPChannel) writeLoop() {
	for {
		select {
		case <-c.manager.stop:
			c.Close()
			return
		case <-c.done:
			c.Close()
			return
		case frame := <-c.send:
			if err := protocol.WriteUDPFrame(c.Conn, frame, c.manager.app.Config.DataPlane.UDP.MaxDatagramSize); err != nil {
				c.Close()
				return
			}
		}
	}
}

func (m *UDPManager) writeResponse(channel *UDPChannel, frame protocol.UDPFrame) {
	m.mu.RLock()
	flow := m.byID[flowIDKey(channel.ClientID, channel.ProxyID, channel.Epoch, frame.FlowID)]
	m.mu.RUnlock()
	if flow == nil || flow.Listener == nil {
		return
	}
	if !flow.Limits.Down.AllowN(len(frame.Data)) {
		if flow.Activity != nil {
			flow.Activity.AddDownRateLimited(len(frame.Data))
		}
		return
	}
	if _, err := flow.Listener.WriteToUDP(frame.Data, flow.Source); err == nil {
		flow.LastActive.Store(time.Now().UnixNano())
		if flow.Activity != nil {
			flow.Activity.AddDown(len(frame.Data), 1)
		}
	}
}

func (m *UDPManager) Reconcile() {
	m.reconcileFlows()
	for _, status := range m.app.Sessions.Snapshot() {
		m.EnsureChannels(m.app.Sessions.Current(status.ClientID))
	}
	m.cleanupStaleChannels()
}

func (m *UDPManager) reconcileFlows() {
	runtimes := make(map[string]ProxyRuntime)
	for _, runtime := range m.app.Listeners.Snapshot() {
		if runtime.Data.Network == "udp" {
			runtimes[proxyRuntimeKey(runtime.ClientID, runtime.Proxy.ProxyID)] = runtime
		}
	}
	maxDatagram := m.app.Config.DataPlane.UDP.MaxDatagramSize
	type removedFlow struct {
		flow    *udpFlow
		channel *UDPChannel
	}
	var removed []removedFlow
	m.mu.Lock()
	for key, flow := range m.byID {
		runtime, ok := runtimes[proxyRuntimeKey(flow.ClientID, flow.ProxyID)]
		allowed := ok
		if allowed {
			allowed = m.app.allowExternal(runtime, flow.Source).Allowed
		}
		if !allowed {
			delete(m.byID, key)
			delete(m.bySource, sourceFlowKey(
				flow.ClientID, flow.ProxyID, flow.Epoch, flow.Source.String(),
			))
			removed = append(removed, removedFlow{
				flow: flow, channel: m.channels[proxyRuntimeKey(flow.ClientID, flow.ProxyID)][flow.ChannelID],
			})
			continue
		}
		_ = flow.Limits.Up.Update(
			runtime.Data.RateLimit.UpBytesPerSecond, true, maxDatagram,
		)
		_ = flow.Limits.Down.Update(
			runtime.Data.RateLimit.DownBytesPerSecond, true, maxDatagram,
		)
		if flow.Activity != nil {
			flow.Activity.SetRateLimits(
				runtime.Data.RateLimit.UpBytesPerSecond,
				runtime.Data.RateLimit.DownBytesPerSecond,
			)
		}
	}
	m.mu.Unlock()
	for _, item := range removed {
		if item.channel != nil && !item.channel.closed.Load() {
			select {
			case item.channel.send <- protocol.UDPFrame{
				Type: protocol.UDPTypeFlowClose, FlowID: item.flow.ID,
			}:
			default:
			}
		}
		if item.flow.Activity != nil {
			item.flow.Activity.Close("source_policy_changed", "external source no longer allowed")
		}
	}
}

func (m *UDPManager) cleanupStaleChannels() {
	desiredConnect := make(map[string]string)
	desiredClient := make(map[string]string)
	for _, runtime := range m.app.Listeners.Snapshot() {
		if runtime.Data.Network == "udp" {
			desiredConnect[proxyRuntimeKey(runtime.ClientID, runtime.Proxy.ProxyID)] = runtime.Data.Connect
			desiredClient[proxyRuntimeKey(runtime.ClientID, runtime.Proxy.ProxyID)] = runtime.ClientID
		}
	}
	m.mu.Lock()
	stale := make([]*UDPChannel, 0)
	for proxyKey, channels := range m.channels {
		for _, channel := range channels {
			if desiredConnect[proxyKey] == channel.Connect && desiredClient[proxyKey] == channel.ClientID {
				continue
			}
			inUse := false
			for _, flow := range m.byID {
				if flow.ChannelID == channel.ID {
					inUse = true
					break
				}
			}
			if !inUse {
				stale = append(stale, channel)
			}
		}
	}
	m.mu.Unlock()
	for _, channel := range stale {
		channel.Close()
	}
}

func (m *UDPManager) removeChannel(channel *UDPChannel) {
	m.mu.Lock()
	if current := m.channels[proxyRuntimeKey(channel.ClientID, channel.ProxyID)][channel.ID]; current == channel {
		delete(m.channels[proxyRuntimeKey(channel.ClientID, channel.ProxyID)], channel.ID)
	}
	m.mu.Unlock()
	session := m.app.Sessions.Current(channel.ClientID)
	if session != nil && session.ID == channel.SessionID && session.Epoch == channel.Epoch {
		go m.EnsureChannels(session)
	}
}

func (m *UDPManager) removeFlow(clientID, proxyID string, epoch, id uint64, reason string) {
	key := flowIDKey(clientID, proxyID, epoch, id)
	m.mu.Lock()
	flow := m.byID[key]
	if flow != nil {
		delete(m.byID, key)
		delete(m.bySource, sourceFlowKey(flow.ClientID, flow.ProxyID, flow.Epoch, flow.Source.String()))
	}
	m.mu.Unlock()
	if flow != nil && flow.Activity != nil {
		flow.Activity.Close(reason, reason)
	}
}

func (m *UDPManager) flowCountLocked(clientID, proxyID string, epoch uint64) int {
	count := 0
	for _, flow := range m.byID {
		if flow.ClientID == clientID && flow.ProxyID == proxyID && flow.Epoch == epoch {
			count++
		}
	}
	return count
}

func (m *UDPManager) reaper() {
	interval := time.Second
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-m.stop:
			return
		case now := <-ticker.C:
			ttl := m.app.Config.Duration(m.app.Config.DataPlane.UDP.FlowIdleTimeout)
			var expired []*udpFlow
			m.mu.Lock()
			for key, flow := range m.byID {
				last := time.Unix(0, flow.LastActive.Load())
				if now.Sub(last) > ttl {
					expired = append(expired, flow)
					delete(m.byID, key)
					delete(m.bySource, sourceFlowKey(flow.ClientID, flow.ProxyID, flow.Epoch, flow.Source.String()))
				}
			}
			m.mu.Unlock()
			for _, flow := range expired {
				m.mu.RLock()
				channel := m.channels[proxyRuntimeKey(flow.ClientID, flow.ProxyID)][flow.ChannelID]
				m.mu.RUnlock()
				if channel != nil && !channel.closed.Load() {
					select {
					case channel.send <- protocol.UDPFrame{Type: protocol.UDPTypeFlowClose, FlowID: flow.ID}:
					default:
					}
				}
				if flow.Activity != nil {
					flow.Activity.Close("idle_timeout", "UDP flow idle timeout")
				}
			}
			m.cleanupStaleChannels()
		}
	}
}

func (m *UDPManager) Close() {
	m.stopOnce.Do(func() { close(m.stop) })
	m.mu.Lock()
	channels := make([]*UDPChannel, 0)
	flows := make([]*udpFlow, 0, len(m.byID))
	for _, proxyChannels := range m.channels {
		for _, channel := range proxyChannels {
			channels = append(channels, channel)
		}
	}
	m.channels = make(map[string]map[string]*UDPChannel)
	m.pending = make(map[string]udpPendingChannel)
	for _, flow := range m.byID {
		flows = append(flows, flow)
	}
	m.bySource = make(map[string]*udpFlow)
	m.byID = make(map[string]*udpFlow)
	m.mu.Unlock()
	for _, channel := range channels {
		channel.Close()
	}
	for _, flow := range flows {
		if flow.Activity != nil {
			flow.Activity.Interrupt("server_stop", "server stopped")
		}
	}
}

func (m *UDPManager) CloseSession(session *Session, reason string) {
	if session == nil {
		return
	}
	m.mu.Lock()
	var flows []*udpFlow
	for key, flow := range m.byID {
		if flow.ClientID != session.ClientID || flow.SessionID != session.ID || flow.Epoch != session.Epoch {
			continue
		}
		flows = append(flows, flow)
		delete(m.byID, key)
		delete(m.bySource, sourceFlowKey(flow.ClientID, flow.ProxyID, flow.Epoch, flow.Source.String()))
	}
	m.mu.Unlock()
	for _, flow := range flows {
		if flow.Activity != nil {
			flow.Activity.Interrupt(reason, reason)
		}
	}
}

func (m *UDPManager) recordDrop(runtime ProxyRuntime, source *net.UDPAddr, bytes int, reason string) {
	sourceAddr := ""
	if source != nil {
		sourceAddr = source.String()
	}
	key := "udp_drop\x00" + runtime.ClientID + "\x00" + runtime.Proxy.ProxyID +
		"\x00" + sourceAddr + "\x00" + reason
	if m.app.RuntimeLogLimiter != nil && !m.app.RuntimeLogLimiter.Allow(key) {
		return
	}
	if m.app.Logger != nil {
		m.app.Logger.Warn(
			"UDP datagram dropped",
			zap.String("reason", reason),
			zap.String("client_id", runtime.ClientID),
			zap.String("proxy_id", runtime.Proxy.ProxyID),
			zap.String("source_addr", sourceAddr),
			zap.Int("datagram_bytes", bytes),
		)
	}
}

func (c *UDPChannel) Close() {
	if c.closed.CompareAndSwap(false, true) {
		close(c.done)
		_ = c.Conn.Close()
		c.manager.removeChannel(c)
	}
}

func sourceFlowKey(clientID, proxyID string, epoch uint64, source string) string {
	return fmt.Sprintf("%s/%d/%s/%s", clientID, epoch, proxyID, source)
}

func flowIDKey(clientID, proxyID string, epoch, id uint64) string {
	return fmt.Sprintf("%s/%s/%d/%d", clientID, proxyID, epoch, id)
}

func cloneUDPAddr(address *net.UDPAddr) *net.UDPAddr {
	return &net.UDPAddr{IP: append(net.IP(nil), address.IP...), Port: address.Port, Zone: address.Zone}
}

func hashSource(value string) uint64 {
	hash := fnv.New64a()
	_, _ = hash.Write([]byte(value))
	return hash.Sum64()
}
