package server

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/geoip"
	"github.com/portpilot/portpilot/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const adminCookie = "portpilot_admin"

type adminSession struct {
	ID              string
	UserName        string
	CSRF            string
	AuthenticatedAt time.Time
	ExpiresAt       time.Time
	SessionTTL      string
}

type loginWindow struct {
	Started time.Time
	Count   int
}

type AdminAuth struct {
	users        map[string]string
	sessions     map[string]*adminSession
	loginWindows map[string]loginWindow
	loginLimit   int
	mu           sync.Mutex
}

func NewAdminAuth(users []config.User, loginLimit int) *AdminAuth {
	auth := &AdminAuth{
		users:        make(map[string]string, len(users)),
		sessions:     make(map[string]*adminSession),
		loginWindows: make(map[string]loginWindow),
		loginLimit:   loginLimit,
	}
	for _, user := range users {
		auth.users[user.UserName] = user.PasswordHash
	}
	return auth
}

func (a *App) adminMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health/live", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "live"})
	})
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, r *http.Request) {
		if !a.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, a.healthStatus(r.Context(), "not_ready"))
			return
		}
		status := "ready"
		if a.Degraded() {
			status = "degraded"
		}
		writeJSON(w, http.StatusOK, a.healthStatus(r.Context(), status))
	})
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.requireAdmin(a.requireCSRF(a.handleLogout)))
	mux.HandleFunc("GET /api/auth/me", a.requireAdmin(a.handleMe))
	mux.HandleFunc("GET /api/clients", a.requireAdmin(a.handleClients))
	mux.HandleFunc("POST /api/clients", a.requireAdmin(a.requireCSRF(a.handleCreateClient)))
	mux.HandleFunc("PATCH /api/clients/{client_id}", a.requireAdmin(a.requireCSRF(a.handleUpdateClient)))
	mux.HandleFunc("POST /api/clients/{client_id}/rotate-token", a.requireAdmin(a.requireCSRF(a.handleRotateClientToken)))
	mux.HandleFunc("POST /api/clients/{client_id}/disconnect", a.requireAdmin(a.requireCSRF(a.handleDisconnect)))
	mux.HandleFunc("GET /api/config", a.requireAdmin(a.handleGetConfig))
	mux.HandleFunc("PUT /api/config", a.requireAdmin(a.requireCSRF(a.handlePutConfig)))
	mux.HandleFunc("GET /api/events", a.requireAdmin(a.handleEvents))
	mux.HandleFunc("GET /api/events/{event_id}", a.requireAdmin(a.handleEventDetail))
	mux.HandleFunc("GET /api/network/summary", a.requireAdmin(a.handleNetworkSummary))
	mux.HandleFunc("GET /api/network/connections", a.requireAdmin(a.handleNetworkConnections))
	mux.HandleFunc("GET /api/network/connections/{activity_id}", a.requireAdmin(a.handleNetworkConnectionDetail))
	mux.HandleFunc("GET /api/network/clients", a.requireAdmin(a.handleNetworkClients))
	mux.HandleFunc("GET /api/network/analytics/overview", a.requireAdmin(a.handleNetworkAnalyticsOverview))
	mux.HandleFunc("GET /api/network/analytics/dimensions", a.requireAdmin(a.handleNetworkAnalyticsDimensions))
	mux.HandleFunc("GET /api/network/analytics/top-ips", a.requireAdmin(a.handleNetworkAnalyticsTopIPs))
	mux.HandleFunc("GET /api/network/analytics/ip-series", a.requireAdmin(a.handleNetworkAnalyticsIPSeries))
	mux.HandleFunc("GET /api/metrics/summary", a.requireAdmin(a.handleSummary))
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/" {
			writeJSON(w, http.StatusOK, map[string]string{"service": "portpilot-server", "status": "ok"})
			return
		}
		http.NotFound(w, r)
	})
	return securityHeaders(mux)
}

func (a *App) healthStatus(ctx context.Context, status string) map[string]any {
	migrationVersion := 0
	database := "ok"
	if a.Events == nil {
		database = "unavailable"
	} else {
		version, err := a.Events.SchemaVersion(ctx)
		if err != nil {
			database = "degraded"
		} else {
			migrationVersion = version
		}
		if a.Events.Degraded() {
			database = "degraded"
		}
	}
	auditWriter := "ok"
	if a.storeError.Load() {
		auditWriter = "degraded"
	}
	activityWriter := "ok"
	if a.Activity != nil && a.Activity.Degraded() {
		activityWriter = "degraded"
	}
	analytics := map[string]any{
		"status": "unavailable", "backfill_complete_from": int64(0),
		"unaggregated_connections": int64(0),
	}
	if a.Activity != nil {
		if value, err := a.Activity.AnalyticsMeta(ctx); err == nil {
			pending, pendingErr := a.Events.CountUnaggregatedConnections(ctx)
			if pendingErr != nil {
				value.Status = "degraded"
			}
			analytics = map[string]any{
				"status": value.Status, "backfill_complete_from": value.CompleteFrom,
				"unaggregated_connections": pending,
				"processed_connections":    value.ProcessedConnections,
				"updated_at":               value.UpdatedAt,
			}
		} else {
			analytics["status"] = "degraded"
		}
	}
	geoStatus := map[string]any{"status": "disabled"}
	if a.GeoIP != nil {
		value := a.GeoIP.Status()
		geoStatus = map[string]any{
			"status":               value.Status,
			"database_fingerprint": value.DatabaseFingerprint,
			"last_reload_at":       value.LastReloadAt,
		}
	}
	return map[string]any{
		"status": status, "database": database,
		"audit_writer": auditWriter, "activity_writer": activityWriter,
		"network_analytics": analytics,
		"geoip":             geoStatus,
		"migration_version": migrationVersion,
	}
}

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		UserName   string `json:"user_name"`
		Password   string `json:"password"`
		SessionTTL string `json:"session_ttl"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	ttlName, ttl, ok := adminSessionTTL(request.SessionTTL)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_session_ttl"})
		return
	}
	request.UserName = strings.TrimSpace(request.UserName)
	source := a.requestSource(r)
	if !a.Admin.allowLogin("source:"+source) || !a.Admin.allowLogin("account:"+request.UserName) {
		if err := a.writeLoginAudit(r.Context(), request.UserName, source, ttlName, "denied", "rate_limited", r.UserAgent()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
			return
		}
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}
	hash := a.Admin.users[request.UserName]
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(request.Password)) != nil {
		if err := a.writeLoginAudit(r.Context(), request.UserName, source, ttlName, "failure", "invalid_credentials", r.UserAgent()); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
			return
		}
		time.Sleep(150 * time.Millisecond)
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid_credentials"})
		return
	}
	sessionID, err := auth.RandomToken(32)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "entropy_unavailable"})
		return
	}
	csrf, err := auth.RandomToken(24)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "entropy_unavailable"})
		return
	}
	now := time.Now()
	expiresAt := now.Add(ttl)
	if err := a.writeLoginAudit(r.Context(), request.UserName, source, ttlName, "success", "", r.UserAgent()); err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
		return
	}
	session := &adminSession{
		ID: sessionID, UserName: request.UserName, CSRF: csrf,
		AuthenticatedAt: now, ExpiresAt: expiresAt, SessionTTL: ttlName,
	}
	a.Admin.mu.Lock()
	a.Admin.sessions[sessionID] = session
	a.Admin.mu.Unlock()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are fixed; Secure follows the trusted proxy scheme.

		Name: adminCookie, Value: sessionID, Path: "/",
		HttpOnly: true, Secure: a.secureRequest(r), SameSite: http.SameSiteStrictMode,
		MaxAge: int(ttl.Seconds()), Expires: expiresAt,
	})
	writeJSON(w, http.StatusOK, map[string]any{
		"user_name": request.UserName, "csrf_token": csrf,
		"authenticated_at": now.UTC().UnixMilli(), "expires_at": expiresAt.UTC().UnixMilli(),
		"session_ttl": ttlName,
	})
}

func (a *App) handleLogout(w http.ResponseWriter, r *http.Request) {
	session := adminFromContext(r)
	a.Admin.mu.Lock()
	delete(a.Admin.sessions, session.ID)
	a.Admin.mu.Unlock()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are fixed; Secure follows the trusted proxy scheme.

		Name: adminCookie, Value: "", Path: "/", HttpOnly: true,
		Secure: a.secureRequest(r), SameSite: http.SameSiteStrictMode, MaxAge: -1,
	})
	a.writeAuditBestEffort(r.Context(), store.AuditEvent{
		Category: "auth", EventType: "admin_logout", Outcome: "success",
		ActorType: "admin", ActorID: session.UserName, SourceAddr: a.requestSource(r),
		Summary: "管理员主动退出", ReasonCode: "user_requested",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	session := adminFromContext(r)
	writeJSON(w, http.StatusOK, map[string]any{
		"user_name": session.UserName, "csrf_token": session.CSRF,
		"authenticated_at": session.AuthenticatedAt.UTC().UnixMilli(),
		"expires_at":       session.ExpiresAt.UTC().UnixMilli(), "session_ttl": session.SessionTTL,
	})
}

func (a *App) handleClients(w http.ResponseWriter, _ *http.Request) {
	statuses := make(map[string]SessionStatus)
	for _, status := range a.Sessions.Snapshot() {
		statuses[status.ClientID] = status
	}
	type clientView struct {
		ClientID      string                 `json:"client_id"`
		Remark        string                 `json:"remark"`
		Enabled       bool                   `json:"enabled"`
		Online        bool                   `json:"online"`
		State         string                 `json:"state"`
		Epoch         uint64                 `json:"epoch"`
		SessionEpoch  uint64                 `json:"session_epoch"`
		MuxSessions   int                    `json:"mux_sessions"`
		ActiveStreams int64                  `json:"active_streams"`
		ProxyCount    int                    `json:"proxy_count"`
		CreatedAt     int64                  `json:"created_at"`
		UpdatedAt     int64                  `json:"updated_at"`
		Proxies       []config.ProxyEnvelope `json:"proxies"`
	}
	file := a.ConfigManager.Snapshot()
	result := make([]clientView, 0, len(file.ClientConfig))
	for _, client := range file.ClientConfig {
		status, online := statuses[client.ClientID]
		if !online {
			status.Epoch = a.Sessions.Epoch(client.ClientID)
		}
		state := "OFFLINE"
		if !client.Enabled {
			state = "DISABLED"
		} else if online {
			state = status.State
		}
		result = append(result, clientView{
			ClientID: client.ClientID, Remark: client.Remark,
			Enabled: client.Enabled, Online: online, State: state,
			Epoch: status.Epoch, SessionEpoch: status.Epoch,
			MuxSessions: status.MuxSessions, ActiveStreams: status.ActiveStreams,
			ProxyCount: len(client.ProxyList), CreatedAt: client.CreatedAt, UpdatedAt: client.UpdatedAt,
			Proxies: client.ProxyList,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": result, "clients": result, "revision": file.Revision,
	})
}

func (a *App) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("client_id")
	if !a.Sessions.Kick(clientID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client_offline"})
		return
	}
	session := adminFromContext(r)
	a.writeAuditBestEffort(r.Context(), store.AuditEvent{
		Category: "client", EventType: "admin_client_disconnect", Outcome: "success",
		ActorType: "admin", ActorID: session.UserName, SourceAddr: a.requestSource(r),
		ClientID: clientID, Summary: "管理员断开客户端连接", ReasonCode: "user_requested",
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleGetConfig(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, config.Redacted(a.ConfigManager.Snapshot()))
}

func (a *App) handlePutConfig(w http.ResponseWriter, r *http.Request) {
	var request struct {
		ExpectedRevision uint64            `json:"expected_revision"`
		Config           config.ClientFile `json:"config"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	current := a.ConfigManager.Snapshot()
	rotatedClients := rotatedClientTokens(current, request.Config)
	if len(rotatedClients) > 0 {
		writeJSON(w, http.StatusBadRequest, map[string]string{
			"error": "token_rotation_requires_dedicated_endpoint",
		})
		return
	}
	updated, ok := a.commitConfigMutation(w, r, request.ExpectedRevision, request.Config, configMutation{
		eventType: "config_update", summary: "更新代理配置",
		changes: diffConfig(current, request.Config),
	})
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, config.Redacted(updated))
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if a.Events == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "event_store_unavailable"})
		return
	}
	query, err := auditQueryFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query", "detail": err.Error()})
		return
	}
	events, err := a.Events.ListAudit(r.Context(), query)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "event_store_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (a *App) handleEventDetail(w http.ResponseWriter, r *http.Request) {
	event, err := a.Events.AuditDetail(r.Context(), r.PathValue("event_id"))
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "event_not_found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "event_store_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, event)
}

func (a *App) handleSummary(w http.ResponseWriter, _ *http.Request) {
	pending, active := a.Tunnels.Counts()
	udpChannels, udpPendingChannels, udpFlows := a.UDP.Counts()
	resources := currentProcessResources()
	analyticsMetrics := activity.Metrics{}
	if a.Activity != nil {
		analyticsMetrics = a.Activity.Metrics()
	}
	geoIPMetrics := geoip.Metrics{}
	if a.GeoIP != nil {
		geoIPMetrics = a.GeoIP.Metrics()
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"online_clients":  len(a.Sessions.Snapshot()),
		"pending_tunnels": pending, "active_tunnels": active,
		"listeners":    len(a.Listeners.Snapshot()),
		"udp_channels": udpChannels, "udp_pending_channels": udpPendingChannels, "udp_flows": udpFlows,
		"goroutines": resources.Goroutines, "open_fds": resources.OpenFDs,
		"heap_alloc_bytes": resources.HeapAlloc, "heap_inuse_bytes": resources.HeapInuse,
		"stack_inuse_bytes": resources.StackInuse, "system_memory_bytes": resources.SystemMemory,
		"network_analytics_metrics":       analyticsMetrics,
		"network_analytics_query_metrics": a.analytics.snapshot(),
		"geoip_metrics":                   geoIPMetrics,
		"degraded":                        a.Degraded(),
	})
}

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
			return
		}
		session, expired := a.Admin.lookupSession(cookie.Value)
		if session == nil {
			if expired != nil {
				a.writeAuditBestEffort(r.Context(), store.AuditEvent{
					Category: "auth", EventType: "admin_logout", Outcome: "success",
					ActorType: "admin", ActorID: expired.UserName, SourceAddr: a.requestSource(r),
					Summary: "管理员会话到期", ReasonCode: "session_expired",
				})
			}
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_expired"})
			return
		}
		next(w, r.WithContext(withAdmin(r.Context(), session)))
	}
}

func (a *App) requireCSRF(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		session := adminFromContext(r)
		token := r.Header.Get("X-CSRF-Token")
		if session == nil || len(token) != len(session.CSRF) ||
			subtle.ConstantTimeCompare([]byte(token), []byte(session.CSRF)) != 1 {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "csrf_failed"})
			return
		}
		next(w, r)
	}
}

func (a *AdminAuth) getSession(id string) *adminSession {
	session, _ := a.lookupSession(id)
	return session
}

func (a *AdminAuth) lookupSession(id string) (*adminSession, *adminSession) {
	a.mu.Lock()
	defer a.mu.Unlock()
	session := a.sessions[id]
	if session == nil {
		return nil, nil
	}
	now := time.Now()
	if !now.Before(session.ExpiresAt) {
		delete(a.sessions, id)
		copy := *session
		return nil, &copy
	}
	copy := *session
	return &copy, nil
}

func (a *AdminAuth) allowLogin(source string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	window := a.loginWindows[source]
	if now.Sub(window.Started) > time.Minute {
		window = loginWindow{Started: now}
	}
	window.Count++
	a.loginWindows[source] = window
	return window.Count <= a.loginLimit
}

func (m *SessionManager) Kick(clientID string) bool {
	m.mu.Lock()
	session := m.active[clientID]
	if session != nil {
		delete(m.active, clientID)
	}
	m.mu.Unlock()
	if session != nil {
		session.Close()
		return true
	}
	return false
}

func (a *App) secureRequest(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if !a.trustedProxy(r.RemoteAddr) {
		return false
	}
	return strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

func (a *App) trustedProxy(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return a.trustedProxyIP(ip)
}

func (a *App) trustedProxyIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	for _, value := range a.Config.TrustedProxies {
		_, network, err := net.ParseCIDR(value)
		if err == nil && network.Contains(ip) {
			return true
		}
	}
	return false
}

func decodeJSONRequest(r *http.Request, target any) error {
	defer func() { _ = r.Body.Close() }()
	decoder := json.NewDecoder(io.LimitReader(r.Body, 2<<20))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request JSON: %w", err)
	}
	var extra any
	if decoder.Decode(&extra) != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

func (a *App) requestSource(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if !a.trustedProxy(r.RemoteAddr) {
		return host
	}
	forwardedHeader := strings.TrimSpace(r.Header.Get("X-Forwarded-For"))
	if forwardedHeader != "" {
		values := strings.Split(forwardedHeader, ",")
		ips := make([]net.IP, 0, len(values))
		for _, value := range values {
			ip := net.ParseIP(strings.TrimSpace(value))
			if ip == nil {
				a.recordInvalidForwardedHeader(r.Context(), host)
				return host
			}
			ips = append(ips, ip)
		}
		for index := len(ips) - 1; index >= 0; index-- {
			if !a.trustedProxyIP(ips[index]) || index == 0 {
				return ips[index].String()
			}
		}
	}
	realIP := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if realIP != "" {
		ip := net.ParseIP(realIP)
		if ip == nil {
			a.recordInvalidForwardedHeader(r.Context(), host)
			return host
		}
		return ip.String()
	}
	return host
}

func (a *App) recordInvalidForwardedHeader(ctx context.Context, peer string) {
	if a.Events == nil {
		return
	}
	a.writeAuditBestEffort(ctx, store.AuditEvent{
		Category: "security", EventType: "security", Outcome: "denied",
		ActorType: "external", SourceAddr: peer,
		Summary: "忽略非法代理来源头", ReasonCode: "invalid_forwarded_header",
	})
}

type adminContextKey struct{}

func withAdmin(ctx context.Context, session *adminSession) context.Context {
	return context.WithValue(ctx, adminContextKey{}, session)
}

func adminFromContext(r *http.Request) *adminSession {
	session, _ := r.Context().Value(adminContextKey{}).(*adminSession)
	return session
}
