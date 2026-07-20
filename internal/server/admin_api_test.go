package server

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
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
		Events: database, Logger: zap.NewNop(),
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
		Admin: NewAdminAuth(nil, 10), Logger: zap.NewNop(),
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
		SampleRetention: time.Hour, StatisticsRetention: time.Hour,
		FineRollupRetention: time.Hour, MaxActive: 10,
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

func TestNetworkAnalyticsAPIIncludesMetadataAndHistoricalData(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	acceptedAt := now.Add(-30 * time.Minute).UnixMilli()
	closedAt := now.Add(-20 * time.Minute).UnixMilli()
	connection := store.NetworkConnection{
		ActivityID: "00000000000000000000000000000901",
		Protocol:   "tcp", State: "accepted",
		ClientID: "client-a", ClientRemark: "Client A",
		ProxyID: "proxy-a", ProxyRemark: "Proxy A",
		ExternalAddr: "198.51.100.10:5000", ExternalIP: "198.51.100.10",
		AcceptedAt: acceptedAt, LastActivityAt: closedAt,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	connection.State = "closed"
	connection.ClosedAt = &closedAt
	connection.UpBytes = 1024
	connection.DownBytes = 2048
	if _, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		store.GeoClassification{Kind: "reserved"},
		closedAt,
	); err != nil {
		t.Fatal(err)
	}
	if err := database.EnsureAnalyticsMeta(t.Context(), now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if err := database.UpdateAnalyticsMeta(
		t.Context(),
		"ready",
		now.Add(-90*24*time.Hour).UnixMilli(),
		1,
		"",
		now.UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: config.DefaultServerConfig(), Events: database}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/network/analytics/overview?range=1h",
		nil,
	)
	response := httptest.NewRecorder()
	app.handleNetworkAnalyticsOverview(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("analytics overview status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Range struct {
			Requested         string `json:"requested"`
			ResolutionSeconds int    `json:"resolution_seconds"`
		} `json:"range"`
		Completeness struct {
			Status       string `json:"status"`
			CompleteFrom int64  `json:"complete_from"`
			GeoIPStatus  string `json:"geoip_status"`
		} `json:"completeness"`
		Connections int64 `json:"connections"`
		UpBytes     int64 `json:"up_bytes"`
		DownBytes   int64 `json:"down_bytes"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Range.Requested != "1h" || payload.Range.ResolutionSeconds != 300 ||
		payload.Completeness.Status != "ready" ||
		payload.Completeness.GeoIPStatus != "disabled" ||
		payload.Connections != 1 || payload.UpBytes != 1024 || payload.DownBytes != 2048 {
		t.Fatalf("unexpected analytics response: %#v body=%s", payload, response.Body.String())
	}

	backfillFrom := now.Add(-24 * time.Hour).UnixMilli()
	if err := database.UpdateAnalyticsMeta(
		t.Context(),
		"backfilling",
		backfillFrom,
		0,
		"",
		now.Add(time.Second).UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	app.handleNetworkAnalyticsOverview(response, request)
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK ||
		payload.Completeness.Status != "backfilling" ||
		payload.Completeness.CompleteFrom != backfillFrom {
		t.Fatalf("backfill metadata response=%d payload=%#v", response.Code, payload.Completeness)
	}
	if err := database.UpdateAnalyticsMeta(
		t.Context(),
		"degraded",
		0,
		0,
		"busy",
		now.Add(2*time.Second).UnixMilli(),
	); err != nil {
		t.Fatal(err)
	}
	response = httptest.NewRecorder()
	app.handleNetworkAnalyticsOverview(response, request)
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if response.Code != http.StatusOK ||
		payload.Completeness.Status != "degraded" ||
		payload.Completeness.CompleteFrom != backfillFrom {
		t.Fatalf("degraded metadata response=%d payload=%#v", response.Code, payload.Completeness)
	}
}

func TestNetworkAnalyticsAPIRejectsInvalidScopeAndIP(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig()}
	for _, test := range []struct {
		path    string
		handler http.HandlerFunc
		code    string
	}{
		{
			path:    "/api/network/analytics/overview?range=24h&proxy_id=proxy",
			handler: app.handleNetworkAnalyticsOverview,
			code:    "invalid_scope",
		},
		{
			path:    "/api/network/analytics/ip-series?range=24h&source_ip=invalid",
			handler: app.handleNetworkAnalyticsIPSeries,
			code:    "invalid_ip",
		},
	} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		test.handler(response, request)
		if response.Code != http.StatusBadRequest ||
			!bytes.Contains(response.Body.Bytes(), []byte(`"`+test.code+`"`)) {
			t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
	}
}

func TestNetworkAnalyticsAPIRejectsInvalidQueryParameters(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig()}
	for _, test := range []struct {
		name    string
		path    string
		handler http.HandlerFunc
		code    string
	}{
		{
			name:    "missing custom range",
			path:    "/api/network/analytics/overview?range=custom",
			handler: app.handleNetworkAnalyticsOverview,
			code:    "invalid_range",
		},
		{
			name:    "invalid protocol",
			path:    "/api/network/analytics/overview?range=24h&protocol=icmp",
			handler: app.handleNetworkAnalyticsOverview,
			code:    "invalid_scope",
		},
		{
			name:    "invalid client",
			path:    "/api/network/analytics/overview?range=24h&client_id=bad%2Fid",
			handler: app.handleNetworkAnalyticsOverview,
			code:    "invalid_scope",
		},
		{
			name:    "invalid page size",
			path:    "/api/network/analytics/dimensions?range=24h&page_size=10",
			handler: app.handleNetworkAnalyticsDimensions,
			code:    "invalid_query",
		},
		{
			name:    "invalid sort",
			path:    "/api/network/analytics/dimensions?range=24h&sort=client_id",
			handler: app.handleNetworkAnalyticsDimensions,
			code:    "invalid_query",
		},
		{
			name:    "invalid top k",
			path:    "/api/network/analytics/top-ips?range=24h&k=21",
			handler: app.handleNetworkAnalyticsTopIPs,
			code:    "invalid_query",
		},
		{
			name:    "missing source IP",
			path:    "/api/network/analytics/ip-series?range=24h",
			handler: app.handleNetworkAnalyticsIPSeries,
			code:    "invalid_ip",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.path, nil)
			response := httptest.NewRecorder()
			test.handler(response, request)
			if response.Code != http.StatusBadRequest ||
				!bytes.Contains(response.Body.Bytes(), []byte(`"`+test.code+`"`)) {
				t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
			}
		})
	}
}

func TestNetworkAnalyticsAPITimeoutIsStableAndObservable(t *testing.T) {
	t.Parallel()
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	app := &App{
		Config:           config.DefaultServerConfig(),
		Events:           database,
		analyticsTimeout: time.Nanosecond,
	}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/network/analytics/overview?range=24h",
		nil,
	)
	response := httptest.NewRecorder()
	app.handleNetworkAnalyticsOverview(response, request)
	if response.Code != http.StatusServiceUnavailable ||
		response.Body.String() != "{\"error\":\"activity_store_unavailable\"}\n" {
		t.Fatalf("timeout response=%d body=%s", response.Code, response.Body.String())
	}
	metrics := app.analytics.snapshot()
	if metrics.Overview.QueryTotal != 1 ||
		metrics.Overview.QueryTimeoutTotal != 1 ||
		metrics.Overview.QueryDurationNanos <= 0 {
		t.Fatalf("timeout metrics=%#v", metrics.Overview)
	}
}

func TestNetworkAnalyticsRangesStayBoundedAndAligned(t *testing.T) {
	t.Parallel()
	app := &App{Config: config.DefaultServerConfig()}
	for _, test := range []struct {
		name       string
		path       string
		resolution int
	}{
		{name: "one hour", path: "/?range=1h", resolution: 300},
		{name: "one day", path: "/?range=24h", resolution: 3600},
		{name: "one week", path: "/?range=7d", resolution: 21600},
		{name: "one month", path: "/?range=30d", resolution: 86400},
		{name: "ninety days", path: "/?range=90d", resolution: 86400},
	} {
		request := httptest.NewRequestWithContext(t.Context(), http.MethodGet, test.path, nil)
		value, err := app.analyticsRange(request)
		if err != nil {
			t.Fatalf("%s: %v", test.name, err)
		}
		if value.ResolutionSeconds != test.resolution ||
			value.EffectiveFrom >= value.EffectiveTo ||
			(value.EffectiveTo-value.EffectiveFrom)/(int64(value.ResolutionSeconds)*1000) > 180 {
			t.Fatalf("%s: unexpected range %#v", test.name, value)
		}
	}
	now := time.Now().UTC().Add(-time.Minute).UnixMilli()
	from := now - (10 * 24 * time.Hour).Milliseconds()
	customRequest := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		fmt.Sprintf("/?range=custom&from=%d&to=%d", from, now),
		nil,
	)
	custom, err := app.analyticsRange(customRequest)
	if err != nil {
		t.Fatal(err)
	}
	points := (custom.EffectiveTo - custom.EffectiveFrom) /
		(int64(custom.ResolutionSeconds) * 1000)
	if points > 180 {
		t.Fatalf("custom range returned %d points: %#v", points, custom)
	}
	tooOld := from - (100 * 24 * time.Hour).Milliseconds()
	invalid := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		fmt.Sprintf("/?range=custom&from=%d&to=%d", tooOld, now),
		nil,
	)
	if _, err := app.analyticsRange(invalid); !errors.Is(err, errInvalidAnalyticsRange) {
		t.Fatalf("oversized custom range error=%v", err)
	}
}

func TestNetworkAnalyticsRoutesRequireAdmin(t *testing.T) {
	t.Parallel()
	app := &App{Admin: NewAdminAuth(nil, 10)}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/network/analytics/overview?range=24h",
		nil,
	)
	response := httptest.NewRecorder()
	app.adminMux().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized ||
		!bytes.Contains(response.Body.Bytes(), []byte("authentication_required")) {
		t.Fatalf("unauthenticated analytics response=%d body=%s", response.Code, response.Body.String())
	}
}

func TestNetworkAnalyticsDimensionsPreserveDeletedHistory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "clients.json")
	const token = "01234567890123456789012345678901234567890123"
	if err := os.WriteFile(configPath, []byte(fmt.Sprintf(`{
		"revision": 1,
		"client_config": [{
			"client_id": "client-a",
			"client_token": %q,
			"remark": "Current client",
			"enabled": true,
			"created_at": 1,
			"updated_at": 1,
			"proxy_list": []
		}]
	}`, token)), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg := config.DefaultServerConfig()
	manager, err := config.NewManager(configPath, cfg.AdminAddr, cfg.ServerAddr)
	if err != nil {
		t.Fatal(err)
	}
	database, err := store.Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	now := time.Now().UTC()
	acceptedAt := now.Add(-20 * time.Minute).UnixMilli()
	closedAt := now.Add(-10 * time.Minute).UnixMilli()
	connection := store.NetworkConnection{
		ActivityID: "00000000000000000000000000000911",
		Protocol:   "tcp", State: "accepted", ClientID: "client-a",
		ClientRemark: "Historical client", ProxyID: "deleted-proxy",
		ProxyRemark: "Deleted proxy", ExternalAddr: "192.0.2.1:5000",
		ExternalIP: "192.0.2.1", AcceptedAt: acceptedAt, LastActivityAt: closedAt,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	connection.State = "closed"
	connection.ClosedAt = &closedAt
	if _, err := database.FinalizeNetworkConnectionAndAggregate(
		t.Context(),
		connection,
		store.GeoClassification{Kind: "reserved"},
		closedAt,
	); err != nil {
		t.Fatal(err)
	}
	app := &App{Config: cfg, ConfigManager: manager, Events: database}
	request := httptest.NewRequestWithContext(
		t.Context(),
		http.MethodGet,
		"/api/network/analytics/dimensions?range=1h&page=1&page_size=25",
		nil,
	)
	response := httptest.NewRecorder()
	app.handleNetworkAnalyticsDimensions(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("dimension status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Items []struct {
			ClientRemark  string `json:"client_remark"`
			ProxyRemark   string `json:"proxy_remark"`
			ClientDeleted bool   `json:"client_deleted"`
			ProxyDeleted  bool   `json:"proxy_deleted"`
		} `json:"items"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Items) != 1 ||
		payload.Items[0].ClientDeleted ||
		!payload.Items[0].ProxyDeleted ||
		payload.Items[0].ClientRemark != "Historical client" ||
		payload.Items[0].ProxyRemark != "Deleted proxy" {
		t.Fatalf("deleted dimension metadata mismatch: %#v", payload.Items)
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
