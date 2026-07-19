package server

import (
	"context"
	"net/http"

	"github.com/portpilot/portpilot/internal/store"
)

func (a *App) allowDataRequest(w http.ResponseWriter, r *http.Request) bool {
	source := a.requestSource(r)
	if a.DataLimiter == nil || a.DataLimiter.Allow(source) {
		return true
	}
	a.recordSecurity(r.Context(), "", "data_handshake_rate_limited", map[string]any{
		"source": source,
	})
	http.Error(w, "rate limited", http.StatusTooManyRequests)
	return false
}

func (a *App) recordSecurity(ctx context.Context, clientID, reason string, metadata map[string]any) {
	source, _ := metadata["source"].(string)
	delete(metadata, "source")
	a.writeAuditBestEffort(ctx, store.AuditEvent{
		Category: "security", EventType: "security", Outcome: "denied",
		ActorType: "external", SourceAddr: source, ClientID: clientID,
		Summary: "安全策略拒绝请求", ReasonCode: reason, Reason: reason,
		Details: metadata,
	})
}
