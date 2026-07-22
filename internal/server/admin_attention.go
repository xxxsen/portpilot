package server

import (
	"net/http"
	"time"
)

func (a *App) handleAttention(w http.ResponseWriter, r *http.Request) {
	limit, err := positiveInt(r.URL.Query().Get("limit"), 8)
	if err != nil || (limit != 8 && limit != 20) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
		return
	}
	items := a.Attention.Snapshot(r.Context())
	counts := map[string]int{"critical": 0, "warning": 0, "info": 0}
	for _, item := range items {
		counts[item.Severity]++
	}
	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"snapshot_at": time.Now().UTC().UnixMilli(),
		"items":       items, "total": total, "counts": counts,
	})
}
