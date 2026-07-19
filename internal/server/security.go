package server

import (
	"net/http"

	"github.com/portpilot/portpilot/internal/store"
)

func (a *App) allowDataRequest(w http.ResponseWriter, r *http.Request) bool {
	source := a.requestSource(r)
	if a.DataLimiter == nil || a.DataLimiter.Allow(source) {
		return true
	}
	a.recordSecurity("", "data_handshake_rate_limited", map[string]any{
		"source": source,
	})
	http.Error(w, "rate limited", http.StatusTooManyRequests)
	return false
}

func (a *App) recordSecurity(clientID, reason string, metadata map[string]any) {
	a.Events.Record(store.Event{
		Kind: "security", ClientID: clientID, Reason: reason, Metadata: metadata,
	})
}
