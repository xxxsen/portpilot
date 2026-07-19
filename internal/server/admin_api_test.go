package server

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
	"golang.org/x/crypto/bcrypt"
)

func TestAdminLoginUsesSelectedAbsoluteTTL(t *testing.T) {
	t.Parallel()
	hash, err := bcrypt.GenerateFromPassword([]byte("secret"), bcrypt.MinCost)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	app := &App{
		Config: config.DefaultServerConfig(),
		Admin: NewAdminAuth([]config.User{{
			UserName: "admin", PasswordHash: string(hash),
		}}, 10),
		Events: database, Logger: slog.Default(),
	}
	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/auth/login",
		bytes.NewBufferString(`{"user_name":"admin","password":"secret","session_ttl":"24h"}`),
	)
	request.RemoteAddr = "192.0.2.1:1234"
	response := httptest.NewRecorder()
	app.handleLogin(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("login status = %d, body=%s", response.Code, response.Body.String())
	}
	cookies := response.Result().Cookies()
	if len(cookies) != 1 || cookies[0].MaxAge != 24*60*60 {
		t.Fatalf("unexpected session cookie: %#v", cookies)
	}
	var payload struct {
		AuthenticatedAt int64  `json:"authenticated_at"`
		ExpiresAt       int64  `json:"expires_at"`
		SessionTTL      string `json:"session_ttl"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.SessionTTL != "24h" ||
		payload.ExpiresAt-payload.AuthenticatedAt != (24*time.Hour).Milliseconds() {
		t.Fatalf("unexpected login payload: %#v", payload)
	}
	page, err := database.ListAudit(t.Context(), store.AuditQuery{
		Page: 1, PageSize: 25, EventType: "admin_login",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Outcome != "success" {
		t.Fatalf("login audit missing: %#v", page)
	}
}

func TestClientManagementAPICreatesAndRedactsToken(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "clients.json")
	if err := os.WriteFile(configPath, []byte("{\n  \"revision\": 1,\n  \"client_config\": []\n}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	manager, err := config.NewManager(configPath, cfg.AdminAddr, cfg.ServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(filepath.Join(dir, "events.db"), nil)
	if err != nil {
		t.Fatal(err)
	}
	app := &App{
		Config: cfg, ConfigManager: manager, Events: database,
		Admin: NewAdminAuth(nil, 10), Logger: slog.Default(),
	}
	app.Sessions = NewSessionManager(app)
	app.Listeners = NewListenerManager(app)
	app.UDP = NewUDPManager(app)
	t.Cleanup(func() {
		app.UDP.Close()
		app.Listeners.Close()
		_ = database.Close()
	})
	admin := &adminSession{
		ID: "session", UserName: "admin", CSRF: "csrf",
		AuthenticatedAt: time.Now(), ExpiresAt: time.Now().Add(time.Hour), SessionTTL: "1h",
	}
	app.Admin.sessions[admin.ID] = admin
	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/clients",
		bytes.NewBufferString(`{"expected_revision":1,"client_id":"branch-1","remark":"Branch office"}`),
	)
	request.AddCookie(testAdminCookie(admin.ID))
	request.Header.Set("X-CSRF-Token", admin.CSRF)
	response := httptest.NewRecorder()
	app.adminMux().ServeHTTP(response, request)
	if response.Code != http.StatusCreated {
		t.Fatalf("create status = %d, body=%s", response.Code, response.Body.String())
	}
	var created struct {
		OneTimeToken string `json:"one_time_token"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if len(created.OneTimeToken) != 43 {
		t.Fatalf("unexpected token length: %d", len(created.OneTimeToken))
	}
	listRequest := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/api/clients", nil)
	listRequest.AddCookie(testAdminCookie(admin.ID))
	listResponse := httptest.NewRecorder()
	app.adminMux().ServeHTTP(listResponse, listRequest)
	if listResponse.Code != http.StatusOK {
		t.Fatalf("list status = %d", listResponse.Code)
	}
	if bytes.Contains(listResponse.Body.Bytes(), []byte(created.OneTimeToken)) ||
		bytes.Contains(listResponse.Body.Bytes(), []byte("client_token")) {
		t.Fatalf("client list leaked token: %s", listResponse.Body.String())
	}
	snapshot := manager.Snapshot()
	if len(snapshot.ClientConfig) != 1 ||
		snapshot.ClientConfig[0].ClientToken != created.OneTimeToken ||
		snapshot.ClientConfig[0].CreatedAt <= 0 {
		t.Fatalf("configuration was not persisted: %#v", snapshot)
	}
	rotateRequest := httptest.NewRequestWithContext(
		t.Context(), http.MethodPost, "/api/clients/branch-1/rotate-token",
		bytes.NewBufferString(`{"expected_revision":2}`),
	)
	rotateRequest.AddCookie(testAdminCookie(admin.ID))
	rotateRequest.Header.Set("X-CSRF-Token", admin.CSRF)
	rotateResponse := httptest.NewRecorder()
	app.adminMux().ServeHTTP(rotateResponse, rotateRequest)
	if rotateResponse.Code != http.StatusOK {
		t.Fatalf("rotate status = %d, body=%s", rotateResponse.Code, rotateResponse.Body.String())
	}
	audits, err := database.ListAudit(t.Context(), store.AuditQuery{
		Page: 1, PageSize: 25, EventType: "client_token_rotated",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(audits.Items) != 1 || audits.Items[0].Outcome != "success" {
		t.Fatalf("rotation audit lacks session context: %#v", audits.Items)
	}
	rotationAudit, err := database.AuditDetail(t.Context(), audits.Items[0].ID)
	if err != nil {
		t.Fatal(err)
	}
	if rotationAudit.Details["session_closed"] != false {
		t.Fatalf("rotation audit lacks session context: %#v", rotationAudit)
	}
}

func testAdminCookie(value string) *http.Cookie {
	return &http.Cookie{
		Name: adminCookie, Value: value, Path: "/",
		HttpOnly: true, Secure: true, SameSite: http.SameSiteStrictMode,
	}
}

func TestConfigDiffNeverContainsClientToken(t *testing.T) {
	t.Parallel()
	before := config.ClientFile{ClientConfig: []config.ClientConfig{{
		ClientID: "client", ClientToken: "old-secret", Remark: "Before",
		Enabled: true, CreatedAt: 100, UpdatedAt: 100,
	}}}
	after := config.ClientFile{ClientConfig: []config.ClientConfig{{
		ClientID: "client", ClientToken: "new-secret", Remark: "After",
		Enabled: true, CreatedAt: 1, UpdatedAt: 999,
	}}}
	changes := diffConfig(before, after)
	encoded, err := json.Marshal(changes)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(encoded, []byte("old-secret")) ||
		bytes.Contains(encoded, []byte("new-secret")) ||
		bytes.Contains(encoded, []byte("client_token")) {
		t.Fatalf("configuration audit leaked a token: %s", encoded)
	}
	if len(changes) != 1 || changes[0].After["remark"] != "After" {
		t.Fatalf("unexpected configuration diff: %#v", changes)
	}
}

func TestNetworkConnectionsSeparateActiveAndHistory(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	activityService, err := activity.New(database, activity.Config{
		SampleInterval: time.Hour, CleanupInterval: time.Hour,
		AuditRetention: time.Hour, NetworkRetention: time.Hour,
		SampleRetention: time.Hour, MaxActive: 10,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(activityService.Close)
	app := &App{Events: database, Activity: activityService}

	active, err := activityService.Start(activity.Start{
		Protocol: "tcp", ClientID: "client-live", ClientRemark: "Live",
		ProxyID: "proxy-live", ProxyRemark: "Live proxy",
	})
	if err != nil {
		t.Fatal(err)
	}
	active.Connected()
	active.AddUp(2048, 0)
	active.AddDown(1024, 0)

	now := time.Now().UTC().UnixMilli()
	closedAt := now
	if err := database.CreateNetworkConnection(t.Context(), store.NetworkConnection{
		ActivityID: "00000000000000000000000000000001",
		Protocol:   "udp", State: "closed", ClientID: "client-old",
		ClientRemark: "Old", ProxyID: "proxy-old", ProxyRemark: "Old proxy",
		AcceptedAt: now - 1000, LastActivityAt: now, ClosedAt: &closedAt,
	}); err != nil {
		t.Fatal(err)
	}

	activePage := requestNetworkPage(t, app, "/api/network/connections?scope=active&page=1&page_size=25")
	if activePage.Total != 1 || len(activePage.Items) != 1 ||
		activePage.Items[0].ActivityID != active.ID() {
		t.Fatalf("active scope returned non-live connections: %#v", activePage)
	}
	historyPage := requestNetworkPage(t, app, "/api/network/connections?scope=history&page=1&page_size=25")
	if historyPage.Total != 1 || len(historyPage.Items) != 1 ||
		historyPage.Items[0].ActivityID != "00000000000000000000000000000001" {
		t.Fatalf("history scope returned live connections: %#v", historyPage)
	}

	request := httptest.NewRequestWithContext(
		t.Context(), http.MethodGet,
		"/api/network/connections?scope=active&state=closed",
		nil,
	)
	response := httptest.NewRecorder()
	app.handleNetworkConnections(response, request)
	if response.Code != http.StatusBadRequest {
		t.Fatalf("incompatible scope status = %d, body=%s", response.Code, response.Body.String())
	}
}

func requestNetworkPage(t *testing.T, app *App, target string) store.NetworkPage {
	t.Helper()
	request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, target, nil)
	response := httptest.NewRecorder()
	app.handleNetworkConnections(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("network page status = %d, body=%s", response.Code, response.Body.String())
	}
	var page store.NetworkPage
	if err := json.Unmarshal(response.Body.Bytes(), &page); err != nil {
		t.Fatal(err)
	}
	return page
}
