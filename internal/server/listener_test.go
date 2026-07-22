package server

import (
	"context"
	"encoding/json"
	"net"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
	"go.uber.org/zap"
)

func TestListenerPrepareReusesReplacesRollsBackAndDeletes(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig(), Logger: zap.NewNop()}
	manager := NewListenerManager(app)
	defer manager.Close()

	firstAddress := freeListenerAddress(t)
	initial := listenerFile(t, "127.0.0.1:18080", firstAddress)
	commit, abort, err := manager.Prepare(initial)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	commit()
	original := manager.handles[proxyRuntimeKey("client", "proxy")]
	if original == nil {
		t.Fatal("listener was not committed")
	}

	updated := listenerFile(t, "127.0.0.1:18081", firstAddress)
	commit, abort, err = manager.Prepare(updated)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	commit()
	if manager.handles[proxyRuntimeKey("client", "proxy")] != original {
		t.Fatal("unchanged bind was unnecessarily re-listened")
	}
	if runtime := original.runtime.Load(); runtime == nil || runtime.Data.Connect != "127.0.0.1:18081" {
		t.Fatalf("reused listener did not publish new runtime: %#v", runtime)
	}

	blockedAddress := freeListenerAddress(t)
	blocker, err := (&net.ListenConfig{}).Listen(t.Context(), "tcp", blockedAddress)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = blocker.Close() }()
	failing := listenerFile(t, "127.0.0.1:18082", blockedAddress)
	if _, _, err := manager.Prepare(failing); err == nil {
		t.Fatal("conflicting bind did not fail")
	}
	if manager.handles[proxyRuntimeKey("client", "proxy")] != original || original.closed.Load() {
		t.Fatal("failed prepare changed the active listener")
	}

	secondAddress := freeListenerAddress(t)
	replacement := listenerFile(t, "127.0.0.1:18082", secondAddress)
	commit, abort, err = manager.Prepare(replacement)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	commit()
	if manager.handles[proxyRuntimeKey("client", "proxy")] == original || !original.closed.Load() {
		t.Fatal("changed bind did not replace and close the old listener")
	}

	commit, abort, err = manager.Prepare(config.ClientFile{Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	active := manager.handles[proxyRuntimeKey("client", "proxy")]
	commit()
	if len(manager.handles) != 0 || active == nil || !active.closed.Load() {
		t.Fatal("deleted proxy listener remained active")
	}
}

func TestListenerCompositeKeyAllowsMatchingProxyIDsAcrossClients(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig(), Logger: zap.NewNop()}
	manager := NewListenerManager(app)
	defer manager.Close()

	proxy := func(bind string) config.ProxyEnvelope {
		data, err := json.Marshal(config.TunnelData{
			Network: "tcp", Transport: "auto", Bind: bind, Connect: "127.0.0.1:18080",
		})
		if err != nil {
			t.Fatal(err)
		}
		return config.ProxyEnvelope{ProxyID: "shared", Kind: "tunnel", Enabled: true, Data: data}
	}
	file := config.ClientFile{Revision: 1, ClientConfig: []config.ClientConfig{
		{ClientID: "client-a", Enabled: true, ProxyList: []config.ProxyEnvelope{proxy(freeListenerAddress(t))}},
		{ClientID: "client-b", Enabled: true, ProxyList: []config.ProxyEnvelope{proxy(freeListenerAddress(t))}},
	}}
	commit, abort, err := manager.Prepare(file)
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	commit()
	left := manager.handles[proxyRuntimeKey("client-a", "shared")]
	right := manager.handles[proxyRuntimeKey("client-b", "shared")]
	if len(manager.handles) != 2 || left == nil || right == nil || left == right {
		t.Fatalf("composite listener identity was not preserved: %#v", manager.handles)
	}
}

func listenerFile(t *testing.T, connect, bind string) config.ClientFile {
	t.Helper()
	data, err := json.Marshal(config.TunnelData{
		Network: "tcp", Transport: "auto", Bind: bind, Connect: connect,
	})
	if err != nil {
		t.Fatal(err)
	}
	return config.ClientFile{
		Revision: 1,
		ClientConfig: []config.ClientConfig{{
			ClientID: "client", ClientToken: "01234567890123456789012345678901234567890123", Enabled: true,
			ProxyList: []config.ProxyEnvelope{{
				ProxyID: "proxy", Kind: "tunnel", Enabled: true, Data: data,
			}},
		}},
	}
}

func freeListenerAddress(t *testing.T) string {
	t.Helper()
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := listener.Addr().String()
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	return address
}
