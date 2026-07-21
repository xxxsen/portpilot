package server

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/config"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestWriteProxyProtocolHeaderPrecedesPayload(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	app := &App{Config: config.DefaultServerConfig()}
	manager := NewTunnelManager(app)
	header := []byte("PROXY TCP4 192.0.2.10 198.51.100.20 1234 7000\r\n")
	pending := &pendingTunnel{
		ProxyProtocolEnabled: true,
		ProxyProtocolHeader:  header,
	}
	result := make(chan error, 1)
	go func() {
		result <- manager.writeProxyProtocolHeader(pending, server)
	}()
	received := make([]byte, len(header))
	if _, err := io.ReadFull(client, received); err != nil {
		t.Fatal(err)
	}
	if string(received) != string(header) {
		t.Fatalf("header = %q", received)
	}
	select {
	case err := <-result:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("header write did not finish")
	}
}

func TestProxyProtocolWriteFailureWritesStructuredLog(t *testing.T) {
	t.Parallel()
	external, externalPeer := net.Pipe()
	data, dataPeer := net.Pipe()
	t.Cleanup(func() {
		_ = externalPeer.Close()
		_ = dataPeer.Close()
	})
	core, observed := observer.New(zap.ErrorLevel)
	app := &App{Config: config.DefaultServerConfig(), Logger: zap.New(core)}
	manager := NewTunnelManager(app)
	pending := &pendingTunnel{
		ID: "connection-a", ClientID: "client-a", ProxyID: "proxy-a",
		External: external, ProxyProtocolEnabled: true,
		ProxyProtocolHeader: []byte("PROXY TCP4 192.0.2.10 198.51.100.20 1234 7000\r\n"),
	}
	manager.Relay(t.Context(), pending, failingWriteConn{Conn: data})

	logs := observed.FilterMessage("write PROXY protocol header failed").All()
	if len(logs) != 1 {
		t.Fatalf("PROXY protocol failure logs = %d, want 1", len(logs))
	}
	fields := logs[0].ContextMap()
	if fields["client_id"] != "client-a" || fields["proxy_id"] != "proxy-a" ||
		fields["connection_id"] != "connection-a" {
		t.Fatalf("unexpected PROXY protocol log: %#v", fields)
	}
}

type failingWriteConn struct {
	net.Conn
}

func (failingWriteConn) Write([]byte) (int, error) {
	return 0, errors.New("forced write failure")
}

func TestWriteProxyProtocolDisabledWritesNothing(t *testing.T) {
	t.Parallel()
	server, client := net.Pipe()
	t.Cleanup(func() {
		_ = server.Close()
		_ = client.Close()
	})
	manager := NewTunnelManager(&App{Config: config.DefaultServerConfig()})
	if err := manager.writeProxyProtocolHeader(&pendingTunnel{}, server); err != nil {
		t.Fatal(err)
	}
	_ = client.SetReadDeadline(time.Now().Add(20 * time.Millisecond))
	if _, err := client.Read(make([]byte, 1)); err == nil {
		t.Fatal("disabled PROXY protocol wrote data")
	}
}
