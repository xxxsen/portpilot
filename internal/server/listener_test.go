package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/portpilot/portpilot/internal/config"
)

func TestListenerPrepareReusesReplacesRollsBackAndDeletes(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig(), Logger: slog.New(slog.NewTextHandler(io.Discard, nil))}
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
	original := manager.handles["proxy"]
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
	if manager.handles["proxy"] != original {
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
	if manager.handles["proxy"] != original || original.closed.Load() {
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
	if manager.handles["proxy"] == original || !original.closed.Load() {
		t.Fatal("changed bind did not replace and close the old listener")
	}

	commit, abort, err = manager.Prepare(config.ClientFile{Revision: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer abort()
	active := manager.handles["proxy"]
	commit()
	if len(manager.handles) != 0 || active == nil || !active.closed.Load() {
		t.Fatal("deleted proxy listener remained active")
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
