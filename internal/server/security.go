package server

import (
	"context"
	"net/http"

	"go.uber.org/zap"
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

func (a *App) recordSecurity(_ context.Context, clientID, reason string, metadata map[string]any) {
	source, _ := metadata["source"].(string)
	delete(metadata, "source")
	key := "security\x00" + reason + "\x00" + clientID + "\x00" + source
	if a.RuntimeLogLimiter != nil && !a.RuntimeLogLimiter.Allow(key) {
		return
	}
	if a.Logger == nil {
		return
	}
	a.Logger.Warn(
		"security request rejected",
		zap.String("reason", reason),
		zap.String("client_id", clientID),
		zap.String("source_ip", source),
		zap.Any("context", metadata),
	)
}
