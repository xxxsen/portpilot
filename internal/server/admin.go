package server

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const adminCookie = "portpilot_admin"

type adminSession struct {
	ID        string
	UserName  string
	CSRF      string
	CreatedAt time.Time
	LastSeen  time.Time
}

type loginWindow struct {
	Started time.Time
	Count   int
}

type AdminAuth struct {
	users        map[string]string
	sessions     map[string]*adminSession
	loginWindows map[string]loginWindow
	idleTimeout  time.Duration
	absoluteTTL  time.Duration
	loginLimit   int
	mu           sync.Mutex
}

func NewAdminAuth(users []config.User, idleTimeout, absoluteTTL time.Duration, loginLimit int) *AdminAuth {
	auth := &AdminAuth{
		users:        make(map[string]string, len(users)),
		sessions:     make(map[string]*adminSession),
		loginWindows: make(map[string]loginWindow),
		idleTimeout:  idleTimeout, absoluteTTL: absoluteTTL,
		loginLimit: loginLimit,
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
	mux.HandleFunc("GET /health/ready", func(w http.ResponseWriter, _ *http.Request) {
		if !a.Ready() {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "not_ready"})
			return
		}
		status := "ready"
		if a.Degraded() {
			status = "degraded"
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": status})
	})
	mux.HandleFunc("POST /api/auth/login", a.handleLogin)
	mux.HandleFunc("POST /api/auth/logout", a.requireAdmin(a.requireCSRF(a.handleLogout)))
	mux.HandleFunc("GET /api/auth/me", a.requireAdmin(a.handleMe))
	mux.HandleFunc("GET /api/clients", a.requireAdmin(a.handleClients))
	mux.HandleFunc("POST /api/clients/{client_id}/disconnect", a.requireAdmin(a.requireCSRF(a.handleDisconnect)))
	mux.HandleFunc("GET /api/config", a.requireAdmin(a.handleGetConfig))
	mux.HandleFunc("PUT /api/config", a.requireAdmin(a.requireCSRF(a.handlePutConfig)))
	mux.HandleFunc("GET /api/events", a.requireAdmin(a.handleEvents))
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

func (a *App) handleLogin(w http.ResponseWriter, r *http.Request) {
	var request struct {
		UserName string `json:"user_name"`
		Password string `json:"password"`
	}
	if err := decodeJSONRequest(r, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	source := a.requestSource(r)
	if !a.Admin.allowLogin("source:"+source) || !a.Admin.allowLogin("account:"+request.UserName) {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}
	hash := a.Admin.users[request.UserName]
	if hash == "" || bcrypt.CompareHashAndPassword([]byte(hash), []byte(request.Password)) != nil {
		a.Events.Record(store.Event{Kind: "admin_login", Reason: "failed", Metadata: map[string]any{"user_name": request.UserName, "source": source}})
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
	session := &adminSession{
		ID: sessionID, UserName: request.UserName, CSRF: csrf,
		CreatedAt: now, LastSeen: now,
	}
	a.Admin.mu.Lock()
	a.Admin.sessions[sessionID] = session
	a.Admin.mu.Unlock()
	http.SetCookie(w, &http.Cookie{ //nolint:gosec // HttpOnly and SameSite are fixed; Secure follows the trusted proxy scheme.

		Name: adminCookie, Value: sessionID, Path: "/",
		HttpOnly: true, Secure: a.secureRequest(r), SameSite: http.SameSiteStrictMode,
		MaxAge: int(a.Admin.absoluteTTL.Seconds()),
	})
	a.Events.Record(store.Event{Kind: "admin_login", Reason: "success", Metadata: map[string]any{"user_name": request.UserName, "source": source}})
	writeJSON(w, http.StatusOK, map[string]string{"user_name": request.UserName, "csrf_token": csrf})
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
	a.Events.Record(store.Event{Kind: "admin_logout", Reason: "success", Metadata: map[string]any{"user_name": session.UserName}})
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleMe(w http.ResponseWriter, r *http.Request) {
	session := adminFromContext(r)
	writeJSON(w, http.StatusOK, map[string]string{"user_name": session.UserName, "csrf_token": session.CSRF})
}

func (a *App) handleClients(w http.ResponseWriter, _ *http.Request) {
	statuses := make(map[string]SessionStatus)
	for _, status := range a.Sessions.Snapshot() {
		statuses[status.ClientID] = status
	}
	type clientView struct {
		ClientID      string                 `json:"client_id"`
		Enabled       bool                   `json:"enabled"`
		State         string                 `json:"state"`
		SessionID     string                 `json:"session_id,omitempty"`
		Epoch         uint64                 `json:"epoch"`
		MuxSessions   int                    `json:"mux_sessions"`
		ActiveStreams int64                  `json:"active_streams"`
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
			ClientID: client.ClientID, Enabled: client.Enabled, State: state,
			SessionID: status.SessionID, Epoch: status.Epoch,
			MuxSessions: status.MuxSessions, ActiveStreams: status.ActiveStreams,
			Proxies: client.ProxyList,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"clients": result, "revision": file.Revision})
}

func (a *App) handleDisconnect(w http.ResponseWriter, r *http.Request) {
	clientID := r.PathValue("client_id")
	if !a.Sessions.Kick(clientID) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "client_offline"})
		return
	}
	session := adminFromContext(r)
	a.Events.Record(store.Event{Kind: "admin_client_disconnect", ClientID: clientID, Reason: "requested", Metadata: map[string]any{"user_name": session.UserName}})
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
	rotatedClients := rotatedClientTokens(a.ConfigManager.Snapshot(), request.Config)
	updated, err := a.ConfigManager.Replace(request.ExpectedRevision, request.Config, a.Listeners.Prepare)
	if err != nil {
		if errors.Is(err, config.ErrRevisionConflict) {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "revision_conflict"})
			return
		}
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_config", "detail": err.Error()})
		return
	}
	session := adminFromContext(r)
	for _, clientID := range rotatedClients {
		a.Sessions.Kick(clientID)
		a.Events.Record(store.Event{Kind: "client_token_rotated", ClientID: clientID, Reason: "success", Metadata: map[string]any{"user_name": session.UserName}})
	}
	a.UDP.Reconcile()
	a.Events.Record(store.Event{Kind: "config_update", Reason: "success", Metadata: map[string]any{"user_name": session.UserName, "revision": updated.Revision}})
	writeJSON(w, http.StatusOK, config.Redacted(updated))
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	if a.Events == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "event_store_unavailable"})
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	events, err := a.Events.List(r.Context(), limit)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "event_store_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"events": events})
}

func (a *App) handleSummary(w http.ResponseWriter, _ *http.Request) {
	pending, active := a.Tunnels.Counts()
	udpChannels, udpPendingChannels, udpFlows := a.UDP.Counts()
	resources := currentProcessResources()
	writeJSON(w, http.StatusOK, map[string]any{
		"online_clients":  len(a.Sessions.Snapshot()),
		"pending_tunnels": pending, "active_tunnels": active,
		"listeners":    len(a.Listeners.Snapshot()),
		"udp_channels": udpChannels, "udp_pending_channels": udpPendingChannels, "udp_flows": udpFlows,
		"goroutines": resources.Goroutines, "open_fds": resources.OpenFDs,
		"heap_alloc_bytes": resources.HeapAlloc, "heap_inuse_bytes": resources.HeapInuse,
		"stack_inuse_bytes": resources.StackInuse, "system_memory_bytes": resources.SystemMemory,
		"degraded": a.Degraded(),
	})
}

func (a *App) requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		cookie, err := r.Cookie(adminCookie)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "authentication_required"})
			return
		}
		session := a.Admin.getSession(cookie.Value)
		if session == nil {
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
	a.mu.Lock()
	defer a.mu.Unlock()
	session := a.sessions[id]
	if session == nil {
		return nil
	}
	now := time.Now()
	if now.Sub(session.LastSeen) > a.idleTimeout || now.Sub(session.CreatedAt) > a.absoluteTTL {
		delete(a.sessions, id)
		return nil
	}
	session.LastSeen = now
	copy := *session
	return &copy
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
	forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0])
	if net.ParseIP(forwarded) != nil {
		return forwarded
	}
	realIP := strings.TrimSpace(r.Header.Get("X-Real-IP"))
	if net.ParseIP(realIP) != nil {
		return realIP
	}
	return host
}

type adminContextKey struct{}

func withAdmin(ctx context.Context, session *adminSession) context.Context {
	return context.WithValue(ctx, adminContextKey{}, session)
}

func adminFromContext(r *http.Request) *adminSession {
	session, _ := r.Context().Value(adminContextKey{}).(*adminSession)
	return session
}
