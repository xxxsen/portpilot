package server

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/knock"
	"github.com/portpilot/portpilot/internal/mailer"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

type serverFakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *serverFakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

type captureSender struct {
	messages chan mailer.Message
}

func (s *captureSender) Send(_ context.Context, message mailer.Message) error {
	s.messages <- message
	return nil
}

func newKnockTestApp(t *testing.T) (*App, *captureSender) {
	t.Helper()
	database, err := store.Open(":memory:", zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	randomData := make([]byte, 4096)
	for index := range randomData {
		randomData[index] = byte(index)
	}
	clock := &serverFakeClock{now: time.Now().UTC()}
	manager := knock.New(knock.Config{
		MagicLinkTTL: 10 * time.Minute, GrantTTL: 10 * time.Minute,
		ResendInterval:     30 * time.Second,
		AllowedEmails:      []string{"allowed@example.com"},
		MaxRequestsPerHour: 100, MaxPendingTokens: 32, MaxGrants: 32,
		Clock: clock, Random: bytes.NewReader(randomData),
	})
	sender := &captureSender{messages: make(chan mailer.Message, 8)}
	worker := mailer.NewWorker(sender, 8)
	app := &App{
		Config: config.ServerConfig{
			RealIPHeader: "X-Real-IP",
			Knock: config.KnockConfig{
				Enabled: true, PublicBaseURL: "https://portpilot.example.com",
				MagicLinkTTL: "10m", GrantTTL: "10m", ResendInterval: "30s",
			},
		},
		Knock: manager, KnockMailer: worker, Events: database, Logger: zap.NewNop(),
	}
	t.Cleanup(func() {
		closeContext, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = worker.Close(closeContext)
		manager.Close()
		_ = database.Close()
	})
	return app, sender
}

func TestKnockHTTPFlowAndEnumerationProtection(t *testing.T) {
	t.Parallel()
	app, sender := newKnockTestApp(t)
	handler := securityHeaders(app.adminMux())

	allowed := knockJSONRequest("/api/knock/requests", `{"email":"allowed@example.com"}`, "192.0.2.10")
	allowedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(allowedRecorder, allowed)
	if allowedRecorder.Code != http.StatusAccepted {
		t.Fatalf("allowed request = %d %s", allowedRecorder.Code, allowedRecorder.Body.String())
	}
	message := waitMessage(t, sender.messages)
	if !strings.Contains(message.TextBody, "https://portpilot.example.com/knock/verify#token=") ||
		!strings.Contains(message.HTMLBody, "https://portpilot.example.com/knock/verify#token=") {
		t.Fatalf("message lacks fixed fragment link: %#v", message)
	}

	unknown := knockJSONRequest("/api/knock/requests", `{"email":"unknown@example.com"}`, "192.0.2.11")
	unknownRecorder := httptest.NewRecorder()
	handler.ServeHTTP(unknownRecorder, unknown)
	if unknownRecorder.Code != http.StatusAccepted ||
		unknownRecorder.Body.String() != allowedRecorder.Body.String() {
		t.Fatalf("allowlist enumeration leak: allowed=%q unknown=%q", allowedRecorder.Body.String(), unknownRecorder.Body.String())
	}
	select {
	case unexpected := <-sender.messages:
		t.Fatalf("non-allowlisted email was queued: %#v", unexpected)
	case <-time.After(25 * time.Millisecond):
	}

	tokenMatch := regexp.MustCompile(`#token=([A-Za-z0-9_-]+)`).FindStringSubmatch(message.TextBody)
	if len(tokenMatch) != 2 {
		t.Fatalf("could not extract token from message: %q", message.TextBody)
	}
	mismatch := knockJSONRequest("/api/knock/verify", `{"token":"`+tokenMatch[1]+`"}`, "192.0.2.99")
	mismatchRecorder := httptest.NewRecorder()
	handler.ServeHTTP(mismatchRecorder, mismatch)
	if mismatchRecorder.Code != http.StatusForbidden ||
		!strings.Contains(mismatchRecorder.Body.String(), "source_ip_mismatch") {
		t.Fatalf("mismatch response = %d %s", mismatchRecorder.Code, mismatchRecorder.Body.String())
	}

	verify := knockJSONRequest("/api/knock/verify", `{"token":"`+tokenMatch[1]+`"}`, "192.0.2.10")
	verifyRecorder := httptest.NewRecorder()
	handler.ServeHTTP(verifyRecorder, verify)
	if verifyRecorder.Code != http.StatusOK {
		t.Fatalf("verify response = %d %s", verifyRecorder.Code, verifyRecorder.Body.String())
	}
	var verified map[string]any
	if err := json.Unmarshal(verifyRecorder.Body.Bytes(), &verified); err != nil {
		t.Fatal(err)
	}
	if verified["source_ip"] != "192.0.2.10" {
		t.Fatalf("verify source = %#v", verified)
	}

	reusedRecorder := httptest.NewRecorder()
	handler.ServeHTTP(reusedRecorder, knockJSONRequest(
		"/api/knock/verify", `{"token":"`+tokenMatch[1]+`"}`, "192.0.2.10",
	))
	if reusedRecorder.Code != http.StatusUnauthorized {
		t.Fatalf("reused token response = %d %s", reusedRecorder.Code, reusedRecorder.Body.String())
	}

	statusRecorder := httptest.NewRecorder()
	statusRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/knock/status", nil)
	statusRequest.RemoteAddr = "127.0.0.1:1234"
	statusRequest.Header.Set("X-Real-IP", "192.0.2.10, 198.51.100.1")
	handler.ServeHTTP(statusRecorder, statusRequest)
	if statusRecorder.Code != http.StatusOK ||
		!strings.Contains(statusRecorder.Body.String(), `"authorized":true`) ||
		strings.Contains(statusRecorder.Body.String(), "allowed@example.com") {
		t.Fatalf("status response = %d %s", statusRecorder.Code, statusRecorder.Body.String())
	}

	page, err := app.Events.ListAudit(t.Context(), store.AuditQuery{
		Page: 1, PageSize: 25, EventType: "knock_grant_created",
	})
	if err != nil || page.Total != 1 {
		t.Fatalf("grant audit = %#v, %v", page, err)
	}
}

func TestKnockHTTPRejectsWrongOriginAndContentType(t *testing.T) {
	t.Parallel()
	app, sender := newKnockTestApp(t)
	handler := app.adminMux()

	wrongOrigin := knockJSONRequest("/api/knock/requests", `{"email":"allowed@example.com"}`, "192.0.2.20")
	wrongOrigin.Header.Set("Origin", "https://attacker.example.com")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, wrongOrigin)
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong origin response = %d", recorder.Code)
	}

	wrongType := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/knock/requests", strings.NewReader(`{"email":"allowed@example.com"}`))
	wrongType.RemoteAddr = "192.0.2.20:1234"
	typeRecorder := httptest.NewRecorder()
	handler.ServeHTTP(typeRecorder, wrongType)
	if typeRecorder.Code != http.StatusBadRequest {
		t.Fatalf("wrong content type response = %d", typeRecorder.Code)
	}
	select {
	case message := <-sender.messages:
		t.Fatalf("invalid public request queued mail: %#v", message)
	default:
	}
}

func TestKnockAuditFailureDoesNotCreateGrantAndTokenCanRetry(t *testing.T) {
	t.Parallel()
	app, sender := newKnockTestApp(t)
	handler := app.adminMux()
	requestRecorder := httptest.NewRecorder()
	handler.ServeHTTP(requestRecorder, knockJSONRequest(
		"/api/knock/requests", `{"email":"allowed@example.com"}`, "192.0.2.30",
	))
	message := waitMessage(t, sender.messages)
	token := regexp.MustCompile(`#token=([A-Za-z0-9_-]+)`).FindStringSubmatch(message.TextBody)[1]

	events := app.Events
	app.Events = nil
	failed := httptest.NewRecorder()
	handler.ServeHTTP(failed, knockJSONRequest(
		"/api/knock/verify", `{"token":"`+token+`"}`, "192.0.2.30",
	))
	if failed.Code != http.StatusServiceUnavailable ||
		!strings.Contains(failed.Body.String(), "audit_store_unavailable") {
		t.Fatalf("audit failure response = %d %s", failed.Code, failed.Body.String())
	}
	if _, ok := app.Knock.ValidGrant(netip.MustParseAddr("192.0.2.30")); ok {
		t.Fatal("grant was created despite audit failure")
	}
	app.Events = events
	retry := httptest.NewRecorder()
	handler.ServeHTTP(retry, knockJSONRequest(
		"/api/knock/verify", `{"token":"`+token+`"}`, "192.0.2.30",
	))
	if retry.Code != http.StatusOK {
		t.Fatalf("retry response = %d %s", retry.Code, retry.Body.String())
	}
}

func knockJSONRequest(path, body, sourceIP string) *http.Request {
	request := httptest.NewRequestWithContext(context.Background(), http.MethodPost, path, strings.NewReader(body))
	request.RemoteAddr = "127.0.0.1:1234"
	request.Header.Set("X-Real-IP", sourceIP)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Origin", "https://portpilot.example.com")
	return request
}

func waitMessage(t *testing.T, messages <-chan mailer.Message) mailer.Message {
	t.Helper()
	select {
	case message := <-messages:
		return message
	case <-time.After(time.Second):
		t.Fatal("mail was not delivered to fake sender")
		return mailer.Message{}
	}
}
