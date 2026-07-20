package server

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/portpilot/portpilot/internal/store"
	"go.uber.org/zap"
)

var adminSessionTTLs = map[string]time.Duration{
	"10m": 10 * time.Minute,
	"30m": 30 * time.Minute,
	"1h":  time.Hour,
	"24h": 24 * time.Hour,
}

func adminSessionTTL(value string) (string, time.Duration, bool) {
	if value == "" {
		value = "30m"
	}
	duration, ok := adminSessionTTLs[value]
	return value, duration, ok
}

func (a *App) writeLoginAudit(
	ctx context.Context,
	userName, source, ttl, outcome, reasonCode, userAgent string,
) error {
	userAgent = strings.TrimSpace(userAgent)
	if len(userAgent) > 512 {
		userAgent = userAgent[:512]
	}
	_, err := a.Events.WriteAudit(ctx, store.AuditEvent{
		Category: "auth", EventType: "admin_login", Outcome: outcome,
		ActorType: "admin", ActorID: userName, SourceAddr: source,
		Summary:    loginSummary(outcome),
		ReasonCode: reasonCode,
		Details: map[string]any{
			"session_ttl": ttl,
			"user_agent":  userAgent,
		},
	})
	if err != nil {
		return fmt.Errorf("write login audit: %w", err)
	}
	return nil
}

func loginSummary(outcome string) string {
	switch outcome {
	case "success":
		return "管理员登录成功"
	case "denied":
		return "管理员登录被拒绝"
	default:
		return "管理员登录失败"
	}
}

func (a *App) writeAuditBestEffort(ctx context.Context, event store.AuditEvent) {
	if a.Events == nil {
		return
	}
	if _, err := a.Events.WriteAudit(ctx, event); err != nil {
		a.storeError.Store(true)
		a.Logger.Error(
			"write audit event failed",
			zap.String("event_type", event.EventType),
			zap.Error(err),
		)
		return
	}
	a.storeError.Store(false)
}
