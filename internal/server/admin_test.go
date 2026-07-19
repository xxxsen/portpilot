package server

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/config"
)

func TestAdminSessionExpiryAndCSRF(t *testing.T) {
	t.Parallel()
	admin := NewAdminAuth(nil, 20*time.Millisecond, time.Hour, 10)
	now := time.Now()
	admin.sessions["session"] = &adminSession{
		ID: "session", UserName: "admin", CSRF: "csrf",
		CreatedAt: now, LastSeen: now,
	}
	if admin.getSession("session") == nil {
		t.Fatal("fresh session was rejected")
	}
	time.Sleep(30 * time.Millisecond)
	if admin.getSession("session") != nil {
		t.Fatal("idle session did not expire")
	}

	app := &App{Admin: admin}
	called := false
	handler := app.requireCSRF(func(http.ResponseWriter, *http.Request) { called = true })
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPut, "/api/config", nil)
	request = request.WithContext(withAdmin(request.Context(), &adminSession{CSRF: "expected"}))
	response := httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusForbidden || called {
		t.Fatalf("missing CSRF was accepted: status=%d called=%v", response.Code, called)
	}
	request.Header.Set("X-CSRF-Token", "expected")
	response = httptest.NewRecorder()
	handler(response, request)
	if response.Code != http.StatusOK || !called {
		t.Fatalf("valid CSRF was rejected: status=%d called=%v", response.Code, called)
	}
}

func TestRequestSourceOnlyTrustsConfiguredProxy(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.ServerConfig{TrustedProxies: []string{"127.0.0.1/32"}}}
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", nil)
	request.RemoteAddr = "198.51.100.10:1234"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")
	request.Header.Set("X-Forwarded-Proto", "https")
	if source := app.requestSource(request); source != "198.51.100.10" {
		t.Fatalf("untrusted proxy spoofed source: %q", source)
	}
	if app.secureRequest(request) {
		t.Fatal("untrusted proxy spoofed HTTPS scheme")
	}

	request.RemoteAddr = "127.0.0.1:4321"
	if source := app.requestSource(request); source != "203.0.113.9" {
		t.Fatalf("trusted proxy source not used: %q", source)
	}
	if !app.secureRequest(request) {
		t.Fatal("trusted proxy HTTPS scheme was ignored")
	}
}

func TestAdminLoginLimiterSeparatesSourceAndAccount(t *testing.T) {
	t.Parallel()
	admin := NewAdminAuth(nil, time.Minute, time.Hour, 10)
	for attempt := 0; attempt < 10; attempt++ {
		if !admin.allowLogin("source:192.0.2.1") || !admin.allowLogin("account:admin") {
			t.Fatalf("login attempt %d was limited early", attempt)
		}
	}
	if admin.allowLogin("source:192.0.2.1") {
		t.Fatal("source login limit was not enforced")
	}
	if admin.allowLogin("account:admin") {
		t.Fatal("account login limit was not enforced")
	}
	if !admin.allowLogin("source:192.0.2.2") || !admin.allowLogin("account:other") {
		t.Fatal("unrelated source and account shared a login window")
	}
}
