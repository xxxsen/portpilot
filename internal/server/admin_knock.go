package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/portpilot/portpilot/internal/accesspolicy"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/knock"
	"github.com/portpilot/portpilot/internal/mailer"
	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

const knockAcceptedMessage = "如果该邮箱允许访问，授权邮件将很快送达"

func (a *App) handleKnockStatus(w http.ResponseWriter, r *http.Request) {
	sourceIP, err := a.knockRequestIP(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	response := map[string]any{
		"enabled": a.Config.Knock.Enabled, "source_ip": sourceIP.String(),
		"authorized": false, "granted_at": int64(0), "expires_at": int64(0),
		"remaining_seconds": int64(0),
	}
	if a.Knock != nil {
		if grant, ok := a.Knock.ValidGrant(sourceIP); ok {
			response["authorized"] = true
			response["granted_at"] = grant.GrantedAt.UnixMilli()
			response["expires_at"] = grant.ExpiresAt.UnixMilli()
			response["remaining_seconds"] = remainingSeconds(grant.ExpiresAt)
		}
	}
	writeJSON(w, http.StatusOK, response)
}

func (a *App) handleKnockRequest(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	if !a.Config.Knock.Enabled || a.Knock == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "knock_disabled"})
		return
	}
	if !a.validKnockOrigin(r) || !jsonContentType(r) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	var request struct {
		Email string `json:"email"`
	}
	if err := decodeLimitedJSON(w, r, 4<<10, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	sourceIP, err := a.knockRequestIP(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	email, normalizeErr := config.NormalizeEmail(request.Email)
	if normalizeErr != nil {
		digest := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(request.Email))))
		email = "invalid-" + hex.EncodeToString(digest[:12])
	}
	result, retry, requestErr := a.Knock.Request(email, sourceIP)
	if errors.Is(requestErr, knock.ErrRateLimited) {
		a.logKnockRateLimited(sourceIP.String(), email)
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retry.Seconds())))))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}
	if requestErr == nil && result.Allowed {
		a.enqueueKnockMail(result, email, sourceIP)
	} else if errors.Is(requestErr, knock.ErrPendingCapacity) {
		a.Logger.Warn("knock pending token capacity reached")
	}
	waitKnockResponse(started)
	writeJSON(w, http.StatusAccepted, map[string]string{"message": knockAcceptedMessage})
}

func (a *App) enqueueKnockMail(result knock.RequestResult, email string, sourceIP netip.Addr) {
	link := strings.TrimRight(a.Config.Knock.PublicBaseURL, "/") +
		"/knock/verify#token=" + result.Token
	message := knockMailMessage(email, sourceIP.String(), result.ExpiresAt, link)
	task := mailer.Task{
		Message: message,
		OnSuccess: func() {
			a.Logger.Info(
				"knock mail sent",
				zap.String("recipient_hash", recipientHash(email)),
			)
		},
		OnFailure: func(_ error) {
			a.Knock.DiscardToken(result.TokenHash)
			a.Logger.Warn(
				"knock mail send failed",
				zap.String("recipient_hash", recipientHash(email)),
				zap.String("error_code", "smtp_send_failed"),
			)
		},
	}
	if a.KnockMailer == nil || !a.KnockMailer.Enqueue(task) {
		a.Knock.DiscardToken(result.TokenHash)
		a.Logger.Warn(
			"knock mail queue full",
			zap.String("recipient_hash", recipientHash(email)),
		)
		return
	}
	a.Logger.Info(
		"knock mail queued",
		zap.String("recipient_hash", recipientHash(email)),
	)
}

func (a *App) handleKnockVerify(w http.ResponseWriter, r *http.Request) {
	if !a.Config.Knock.Enabled || a.Knock == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "knock_disabled"})
		return
	}
	if !a.validKnockOrigin(r) || !jsonContentType(r) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	var request struct {
		Token string `json:"token"`
	}
	if err := decodeLimitedJSON(w, r, 4<<10, &request); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	raw, err := base64.RawURLEncoding.DecodeString(request.Token)
	if err != nil || len(raw) != 32 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	sourceIP, err := a.knockRequestIP(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_request"})
		return
	}
	if retry, allowed := a.Knock.AllowVerify(sourceIP); !allowed {
		w.Header().Set("Retry-After", strconv.Itoa(max(1, int(math.Ceil(retry.Seconds())))))
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}
	verification, err := a.Knock.BeginVerify(sha256.Sum256(raw), sourceIP)
	if err != nil {
		a.writeKnockVerifyError(w, err, sourceIP)
		return
	}
	committed := false
	defer func() {
		if !committed {
			a.Knock.AbortVerify(verification.ID)
		}
	}()
	userAgent := r.UserAgent()
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	event, auditErr := a.Events.WriteAudit(r.Context(), store.AuditEvent{
		Category: "auth", EventType: "knock_grant_created", Outcome: "success",
		ActorType: "external", ActorID: verification.Email, SourceAddr: sourceIP.String(),
		Summary: "邮件临时访问授权生效",
		Details: map[string]any{
			"requested_at": verification.RequestedAt.UnixMilli(),
			"granted_at":   verification.GrantedAt.UnixMilli(),
			"expires_at":   verification.ExpiresAt.UnixMilli(),
			"grant_ttl":    a.Config.Knock.GrantTTL, "refreshed": verification.Refreshed,
			"user_agent": userAgent,
		},
	})
	if auditErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
		return
	}
	grant, err := a.Knock.CommitVerify(verification.ID, event.ID)
	if err != nil {
		a.Logger.Error("commit knock verification failed", zap.Error(err))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "verification_commit_failed"})
		return
	}
	committed = true
	writeJSON(w, http.StatusOK, map[string]any{
		"source_ip":  grant.SourceIP.String(),
		"granted_at": grant.GrantedAt.UnixMilli(), "expires_at": grant.ExpiresAt.UnixMilli(),
		"remaining_seconds": remainingSeconds(grant.ExpiresAt),
		"refreshed":         verification.Refreshed,
	})
}

func (a *App) handleKnockGrants(w http.ResponseWriter, _ *http.Request) {
	items := make([]map[string]any, 0)
	if a.Knock != nil {
		for _, grant := range a.Knock.ListGrants() {
			items = append(items, map[string]any{
				"source_ip": grant.SourceIP.String(), "granted_by": grant.GrantedBy,
				"granted_at": grant.GrantedAt.UnixMilli(), "expires_at": grant.ExpiresAt.UnixMilli(),
				"remaining_seconds": remainingSeconds(grant.ExpiresAt),
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "total": len(items)})
}

func (a *App) handleKnockRevoke(w http.ResponseWriter, r *http.Request) {
	sourceIP, err := accesspolicy.NormalizeIPString(r.PathValue("source_ip"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_source_ip"})
		return
	}
	if a.Knock == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	grant, existed := a.Knock.Revoke(sourceIP)
	if !existed {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	session := adminFromContext(r)
	_, auditErr := a.Events.WriteAudit(r.Context(), store.AuditEvent{
		Category: "auth", EventType: "knock_grant_revoked", Outcome: "success",
		ActorType: "admin", ActorID: session.UserName, SourceAddr: a.requestSource(r),
		Summary: "管理员撤销邮件临时访问授权",
		Details: map[string]any{
			"target_source_ip": grant.SourceIP.String(), "granted_by": grant.GrantedBy,
			"granted_at": grant.GrantedAt.UnixMilli(), "expires_at": grant.ExpiresAt.UnixMilli(),
		},
	})
	if auditErr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "audit_store_unavailable"})
		return
	}
	a.Logger.Info(
		"knock grant revoked",
		zap.String("source_ip", grant.SourceIP.String()),
		zap.String("admin_user", session.UserName),
	)
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) writeKnockVerifyError(w http.ResponseWriter, err error, sourceIP netip.Addr) {
	status := http.StatusUnauthorized
	code := "invalid_or_expired_token"
	switch {
	case errors.Is(err, knock.ErrSourceIPMismatch):
		status, code = http.StatusForbidden, "source_ip_mismatch"
		a.Logger.Warn("knock token source mismatch", zap.String("source_ip", sourceIP.String()))
	case errors.Is(err, knock.ErrVerificationInProgress):
		status, code = http.StatusConflict, "verification_in_progress"
	case errors.Is(err, knock.ErrGrantCapacity):
		status, code = http.StatusServiceUnavailable, "grant_capacity_exceeded"
	case !errors.Is(err, knock.ErrInvalidOrExpiredToken):
		status, code = http.StatusServiceUnavailable, "verification_unavailable"
	}
	a.Logger.Warn(
		"knock token verification failed",
		zap.String("source_ip", sourceIP.String()),
		zap.String("reason", code),
	)
	writeJSON(w, status, map[string]string{"error": code})
}

func (a *App) knockRequestIP(r *http.Request) (netip.Addr, error) {
	sourceIP, err := accesspolicy.NormalizeIPString(a.requestSource(r))
	if err != nil {
		return netip.Addr{}, fmt.Errorf("normalize knock request IP: %w", err)
	}
	return sourceIP, nil
}

func (a *App) validKnockOrigin(r *http.Request) bool {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return true
	}
	base, err := url.Parse(a.Config.Knock.PublicBaseURL)
	if err != nil {
		return false
	}
	return origin == base.Scheme+"://"+base.Host
}

func jsonContentType(r *http.Request) bool {
	value, _, _ := strings.Cut(r.Header.Get("Content-Type"), ";")
	return strings.EqualFold(strings.TrimSpace(value), "application/json")
}

func decodeLimitedJSON(w http.ResponseWriter, r *http.Request, maximum int64, target any) error {
	defer func() { _ = r.Body.Close() }()
	reader := http.MaxBytesReader(w, r.Body, maximum)
	decoder := json.NewDecoder(reader)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return fmt.Errorf("decode request JSON: %w", err)
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		return errors.New("trailing JSON")
	}
	return nil
}

func knockMailMessage(to, sourceIP string, expiresAt time.Time, link string) mailer.Message {
	expiresText := expiresAt.Local().Format("2006-01-02 15:04:05 MST")
	return mailer.Message{
		To: to, Subject: "PortPilot 临时访问授权",
		TextBody: fmt.Sprintf(
			"有人为 IP %s 请求了 PortPilot 临时访问授权。\n\n请在 %s 前从同一网络打开以下链接：\n%s\n\n如果不是你发起的请求，请忽略本邮件。",
			sourceIP, expiresText, link,
		),
		HTMLBody: "<!doctype html><html><body><h1>PortPilot 临时访问授权</h1><p>有人为 IP <code>" +
			html.EscapeString(sourceIP) + "</code> 请求了临时访问授权。</p><p>请在 " +
			html.EscapeString(expiresText) + " 前从同一网络打开链接。</p><p><a href=\"" +
			html.EscapeString(link) + "\">授权当前 IP</a></p><p>如果不是你发起的请求，请忽略本邮件。</p></body></html>",
	}
}

func recipientHash(email string) string {
	digest := sha256.Sum256([]byte(email))
	return hex.EncodeToString(digest[:6])
}

func (a *App) logKnockRateLimited(sourceIP, email string) {
	if a.Logger == nil {
		return
	}
	a.Logger.Warn(
		"knock request rate limited",
		zap.String("source_ip", sourceIP),
		zap.String("recipient_hash", recipientHash(email)),
	)
}

func waitKnockResponse(started time.Time) {
	if remaining := 150*time.Millisecond - time.Since(started); remaining > 0 {
		time.Sleep(remaining)
	}
}

func remainingSeconds(expiresAt time.Time) int64 {
	remaining := time.Until(expiresAt)
	if remaining <= 0 {
		return 0
	}
	return int64(math.Ceil(remaining.Seconds()))
}
