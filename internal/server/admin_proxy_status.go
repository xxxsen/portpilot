package server

import (
	"net/http"
	"sort"
	"strings"
)

type proxyRuntimePage struct {
	Revision   uint64               `json:"revision"`
	SnapshotAt int64                `json:"snapshot_at"`
	Items      []ProxyRuntimeStatus `json:"items"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"page_size"`
	Total      int                  `json:"total"`
	TotalPages int                  `json:"total_pages"`
}

func (a *App) handleProxyRuntime(w http.ResponseWriter, r *http.Request) {
	page, err := positiveInt(r.URL.Query().Get("page"), 1)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_page"})
		return
	}
	pageSize, err := positiveInt(r.URL.Query().Get("page_size"), 25)
	if err != nil || (pageSize != 25 && pageSize != 50 && pageSize != 100) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_page_size"})
		return
	}
	query := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("query")))
	if len(query) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
	availability := strings.TrimSpace(r.URL.Query().Get("availability"))
	if availability != "" && !validAvailability(availability) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_availability"})
		return
	}
	protocol := strings.TrimSpace(r.URL.Query().Get("protocol"))
	if protocol != "" && protocol != "tcp" && protocol != "udp" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_protocol"})
		return
	}

	revision, snapshotAt, values := a.ProxyStatus.Snapshot(r.Context())
	items := make([]ProxyRuntimeStatus, 0, len(values))
	for _, value := range values {
		if clientID != "" && value.ClientID != clientID {
			continue
		}
		if availability != "" && value.Availability != availability {
			continue
		}
		if protocol != "" && value.Protocol != protocol {
			continue
		}
		if query != "" && !proxyRuntimeMatches(value, query) {
			continue
		}
		items = append(items, value)
	}
	sort.Slice(items, func(i, j int) bool {
		left := availabilityPriority(items[i].Availability)
		right := availabilityPriority(items[j].Availability)
		if left != right {
			return left < right
		}
		if items[i].ClientID != items[j].ClientID {
			return items[i].ClientID < items[j].ClientID
		}
		return items[i].ProxyID < items[j].ProxyID
	})
	total := len(items)
	totalPages := (total + pageSize - 1) / pageSize
	start := (page - 1) * pageSize
	if start > total {
		start = total
	}
	end := min(start+pageSize, total)
	writeJSON(w, http.StatusOK, proxyRuntimePage{
		Revision: revision, SnapshotAt: snapshotAt, Items: items[start:end],
		Page: page, PageSize: pageSize, Total: total, TotalPages: totalPages,
	})
}

func validAvailability(value string) bool {
	switch value {
	case "disabled", "waiting_client", "degraded", "ready", "error":
		return true
	default:
		return false
	}
}

func availabilityPriority(value string) int {
	switch value {
	case "error":
		return 0
	case "degraded":
		return 1
	case "waiting_client":
		return 2
	case "ready":
		return 3
	default:
		return 4
	}
}

func proxyRuntimeMatches(value ProxyRuntimeStatus, query string) bool {
	for _, candidate := range []string{
		value.ClientID, value.ClientRemark, value.ProxyID, value.ProxyRemark,
		value.Bind, value.Connect,
	} {
		if strings.Contains(strings.ToLower(candidate), query) {
			return true
		}
	}
	return false
}
