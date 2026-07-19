package server

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/store"
)

func TestDataHandshakeLimiterRejectsExcessRequests(t *testing.T) {
	t.Parallel()
	app := &App{DataLimiter: auth.NewRateLimiter(1, time.Minute)}
	first := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/portpilot/data/mux", nil)
	first.RemoteAddr = "192.0.2.1:1234"
	if !app.allowDataRequest(httptest.NewRecorder(), first) {
		t.Fatal("first data handshake was limited")
	}
	response := httptest.NewRecorder()
	if app.allowDataRequest(response, first) {
		t.Fatal("excess data handshake was admitted")
	}
	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("unexpected limited response: %d", response.Code)
	}
}

func TestRejectedDataTicketCreatesAuditEvent(t *testing.T) {
	t.Parallel()
	events, err := store.Open(
		filepath.Join(t.TempDir(), "events.db"),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := events.Close(); err != nil {
			t.Error(err)
		}
	}()
	app := &App{
		Events: events, Tickets: auth.NewTicketStore(time.Minute),
	}
	manager := NewTunnelManager(app)
	if _, ok := manager.Pair(protocol.StreamOpen{
		ClientID: "client", SessionID: "session", SessionEpoch: 7,
		ConnectionID: "connection", DataTicket: "invalid",
	}, nil); ok {
		t.Fatal("invalid ticket was accepted")
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		items, listErr := events.List(t.Context(), 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(items) > 0 {
			if items[0]["kind"] != "security" || items[0]["reason"] != "tcp_ticket_rejected" {
				t.Fatalf("unexpected security event: %#v", items[0])
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("ticket rejection audit event was not persisted")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
