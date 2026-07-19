package server

import (
	"context"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/protocol"
)

func TestUDPFlowLimitQueueBoundAndChannelStickiness(t *testing.T) {
	t.Parallel()
	app := &App{
		Config: config.DefaultServerConfig(),
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	app.Config.DataPlane.UDP.MaxFlowsPerProxy = 1
	app.Config.DataPlane.UDP.ChannelQueueDatagrams = 2
	app.Config.DataPlane.UDP.FlowIdleTimeout = "1h"
	app.Config.Limits.MaxUDPFlowsTotal = 1
	app.Sessions = NewSessionManager(app)
	manager := NewUDPManager(app)
	app.UDP = manager
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	session := &Session{
		ClientID: "client", ID: "session", Epoch: 1,
		app: app, ctx: ctx, cancel: cancel,
		send: make(chan outboundControl, 1), muxes: make(map[string]*MuxSession),
		pendingMux: make(map[string]struct{}),
	}
	app.Sessions.active[session.ClientID] = session

	channelConn, peer := net.Pipe()
	defer func() { _ = peer.Close() }()
	channel := &UDPChannel{
		ID: "channel", ProxyID: "proxy", ClientID: "client",
		SessionID: "session", Epoch: 1, Connect: "127.0.0.1:18081",
		Conn: channelConn, send: make(chan protocol.UDPFrame, 2),
		done: make(chan struct{}), manager: manager,
	}
	manager.channels["proxy"] = map[string]*UDPChannel{channel.ID: channel}
	t.Cleanup(func() {
		app.Sessions.mu.Lock()
		delete(app.Sessions.active, session.ClientID)
		app.Sessions.mu.Unlock()
		manager.Close()
	})

	runtime := ProxyRuntime{
		ClientID: "client",
		Proxy:    config.ProxyEnvelope{ProxyID: "proxy", Enabled: true},
		Data: config.TunnelData{
			Network: "udp", Transport: "smux-datagram",
			Bind: "127.0.0.1:17001", Connect: channel.Connect,
		},
	}
	firstSource := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20001}
	manager.HandleDatagram(runtime, nil, firstSource, []byte("one"))
	manager.HandleDatagram(runtime, nil, firstSource, []byte("two"))
	flow := manager.bySource[sourceFlowKey("client", "proxy", 1, firstSource.String())]
	if flow == nil || flow.ChannelID != channel.ID {
		t.Fatalf("flow did not bind to the healthy channel: %#v", flow)
	}
	if len(channel.send) != cap(channel.send) {
		t.Fatalf("UDP channel queue was not filled: %d", len(channel.send))
	}
	manager.HandleDatagram(runtime, nil, firstSource, []byte("dropped"))
	if len(channel.send) != cap(channel.send) {
		t.Fatal("queue-full datagram exceeded the bounded channel queue")
	}

	secondSource := &net.UDPAddr{IP: net.ParseIP("127.0.0.1"), Port: 20002}
	manager.HandleDatagram(runtime, nil, secondSource, []byte("limited"))
	channels, pending, flows := manager.Counts()
	if channels != 1 || pending != 0 || flows != 1 {
		t.Fatalf("unexpected UDP counts: channels=%d pending=%d flows=%d", channels, pending, flows)
	}
	if manager.bySource[sourceFlowKey("client", "proxy", 1, secondSource.String())] != nil {
		t.Fatal("flow limit admitted a second source")
	}
}
