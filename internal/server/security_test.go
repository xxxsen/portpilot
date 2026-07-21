package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/protocol"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
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

func TestRejectedDataTicketWritesStructuredLogOnly(t *testing.T) {
	t.Parallel()
	events, err := store.Open(":memory:", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := events.Close(); err != nil {
			t.Error(err)
		}
	}()
	core, observed := observer.New(zap.WarnLevel)
	app := &App{
		Events: events, Tickets: auth.NewTicketStore(time.Minute),
		Logger: zap.New(core), RuntimeLogLimiter: auth.NewRateLimiter(1, time.Minute),
	}
	manager := NewTunnelManager(app)
	if _, ok := manager.Pair(t.Context(), protocol.StreamOpen{
		ClientID: "client", SessionID: "session", SessionEpoch: 7,
		ConnectionID: "connection", DataTicket: "invalid",
	}, nil); ok {
		t.Fatal("invalid ticket was accepted")
	}
	entries := observed.FilterMessage("security request rejected").All()
	if len(entries) != 1 {
		t.Fatalf("security log entries = %d, want 1", len(entries))
	}
	fields := entries[0].ContextMap()
	if fields["reason"] != "tcp_ticket_rejected" || fields["client_id"] != "client" {
		t.Fatalf("unexpected security log context: %#v", fields)
	}
	page, err := events.ListAudit(t.Context(), store.AuditQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 {
		t.Fatalf("runtime rejection leaked into audit events: %#v", page.Items)
	}
}
