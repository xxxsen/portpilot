package server

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

var networkRanges = map[string]time.Duration{
	"1h": time.Hour, "24h": 24 * time.Hour, "7d": 7 * 24 * time.Hour, "30d": 30 * 24 * time.Hour,
}

func (a *App) handleNetworkSummary(w http.ResponseWriter, r *http.Request) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "24h"
	}
	duration, ok := networkRanges[rangeName]
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_range"})
		return
	}
	clientID := strings.TrimSpace(r.URL.Query().Get("client_id"))
	from := time.Now().UTC().Add(-duration).UnixMilli()
	history, err := a.Events.NetworkSummary(r.Context(), from, clientID)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	activeTotal, activeClients := a.Activity.Counts()
	activeClientCount := len(activeClients)
	if clientID != "" {
		activeTotal = activeClients[clientID]
		activeClientCount = 0
		if activeTotal > 0 {
			activeClientCount = 1
		}
	}
	clients, err := a.networkClientItems(r, "", 0)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	if clientID != "" {
		filtered := clients[:0]
		for _, client := range clients {
			if client.ClientID == clientID {
				filtered = append(filtered, client)
			}
		}
		clients = filtered
	}
	failureRate := float64(0)
	if history.Connections > 0 {
		failureRate = float64(history.Failures) / float64(history.Connections)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"range": rangeName, "client_id": clientID,
		"active_connections": activeTotal, "active_clients": activeClientCount,
		"connections": history.Connections, "failures": history.Failures,
		"failure_rate": failureRate, "up_bytes": history.UpBytes, "down_bytes": history.DownBytes,
		"series": history.Series, "clients": clients,
	})
}

func (a *App) handleNetworkConnections(w http.ResponseWriter, r *http.Request) {
	query, err := networkQueryFromRequest(r)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query", "detail": err.Error()})
		return
	}
	needed := query.Page * query.PageSize
	fetchPages := (needed + 99) / 100
	if fetchPages < 1 {
		fetchPages = 1
	}
	historical := make([]store.NetworkConnection, 0, needed)
	var historicalTotal int64
	for page := 1; page <= fetchPages; page++ {
		dbQuery := query
		dbQuery.Page = page
		dbQuery.PageSize = 100
		result, listErr := a.Events.ListNetworkConnections(r.Context(), dbQuery)
		if listErr != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query", "detail": listErr.Error()})
			return
		}
		historicalTotal = result.Total
		historical = append(historical, result.Items...)
		if len(result.Items) < 100 {
			break
		}
	}
	byID := make(map[string]store.NetworkConnection, len(historical))
	for _, connection := range historical {
		byID[connection.ActivityID] = connection
	}
	for _, connection := range a.Activity.Snapshot() {
		if matchesNetworkQuery(connection, query) {
			byID[connection.ActivityID] = connection
		}
	}
	merged := make([]store.NetworkConnection, 0, len(byID))
	for _, connection := range byID {
		merged = append(merged, connection)
	}
	sortNetworkConnections(merged, query.Sort)
	start := (query.Page - 1) * query.PageSize
	end := start + query.PageSize
	items := []store.NetworkConnection{}
	if start < len(merged) {
		if end > len(merged) {
			end = len(merged)
		}
		items = merged[start:end]
	}
	total := historicalTotal
	if int64(len(byID)) > total {
		total = int64(len(byID))
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(query.PageSize) - 1) / int64(query.PageSize))
	}
	writeJSON(w, http.StatusOK, store.NetworkPage{
		Items: items, Page: query.Page, PageSize: query.PageSize,
		Total: total, TotalPages: totalPages,
	})
}

func (a *App) handleNetworkConnectionDetail(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("activity_id")
	if connection, ok := a.Activity.Get(id); ok {
		detail, err := a.Events.NetworkConnectionDetail(
			r.Context(), id,
			time.Now().UTC().Add(-a.Config.Duration(a.Config.Observability.NetworkSampleRetention)).UnixMilli(),
		)
		if errors.Is(err, sql.ErrNoRows) {
			detail.NetworkConnection = connection
			detail.Samples = []store.NetworkSample{}
			writeJSON(w, http.StatusOK, detail)
			return
		}
		if err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
			return
		}
		detail.NetworkConnection = connection
		writeJSON(w, http.StatusOK, detail)
		return
	}
	detail, err := a.Events.NetworkConnectionDetail(
		r.Context(), id,
		time.Now().UTC().Add(-a.Config.Duration(a.Config.Observability.NetworkSampleRetention)).UnixMilli(),
	)
	if errors.Is(err, sql.ErrNoRows) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "connection_not_found"})
		return
	}
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

type networkClientView struct {
	ClientID              string `json:"client_id"`
	ClientRemark          string `json:"client_remark"`
	Enabled               bool   `json:"enabled"`
	Online                bool   `json:"online"`
	Deleted               bool   `json:"deleted"`
	SessionEpoch          uint64 `json:"session_epoch"`
	MuxSessions           int    `json:"mux_sessions"`
	ActiveConnections     int    `json:"active_connections"`
	HistoricalConnections int64  `json:"historical_connections"`
	Failures              int64  `json:"failures"`
	UpBytes               int64  `json:"up_bytes"`
	DownBytes             int64  `json:"down_bytes"`
	LastSeenAt            int64  `json:"last_seen_at"`
}

func (a *App) handleNetworkClients(w http.ResponseWriter, r *http.Request) {
	page, err := positiveInt(r.URL.Query().Get("page"), 1)
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	pageSize, err := positiveInt(r.URL.Query().Get("page_size"), 50)
	if err != nil || (pageSize != 25 && pageSize != 50 && pageSize != 100) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	items, err := a.networkClientItems(r, r.URL.Query().Get("query"), page*pageSize)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	start := (page - 1) * pageSize
	end := start + pageSize
	pageItems := []networkClientView{}
	if start < len(items) {
		if end > len(items) {
			end = len(items)
		}
		pageItems = items[start:end]
	}
	totalPages := 0
	if len(items) > 0 {
		totalPages = (len(items) + pageSize - 1) / pageSize
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"items": pageItems, "page": page, "page_size": pageSize,
		"total": len(items), "total_pages": totalPages,
	})
}

func (a *App) networkClientItems(
	r *http.Request,
	query string,
	_ int,
) ([]networkClientView, error) {
	query = strings.TrimSpace(query)
	history := make(map[string]store.NetworkClientHistory)
	for page := 1; ; page++ {
		result, err := a.Events.ListNetworkClients(r.Context(), store.NetworkClientQuery{
			Page: page, PageSize: 100, Query: query,
		})
		if err != nil {
			return nil, fmt.Errorf("list historical network clients: %w", err)
		}
		for _, item := range result.Items {
			history[item.ClientID] = item
		}
		if page >= result.TotalPages || len(result.Items) == 0 {
			break
		}
	}
	_, active := a.Activity.Counts()
	statuses := make(map[string]SessionStatus)
	for _, status := range a.Sessions.Snapshot() {
		statuses[status.ClientID] = status
	}
	items := make(map[string]networkClientView)
	for clientID, item := range history {
		items[clientID] = networkClientView{
			ClientID: clientID, ClientRemark: item.ClientRemark, Deleted: true,
			ActiveConnections: active[clientID], HistoricalConnections: item.HistoricalConnections,
			Failures: item.Failures, UpBytes: item.UpBytes, DownBytes: item.DownBytes,
			LastSeenAt: item.LastSeenAt,
		}
	}
	lowerQuery := strings.ToLower(query)
	for _, client := range a.ConfigManager.Snapshot().ClientConfig {
		if lowerQuery != "" && !strings.Contains(strings.ToLower(client.ClientID), lowerQuery) &&
			!strings.Contains(strings.ToLower(client.Remark), lowerQuery) {
			continue
		}
		item := items[client.ClientID]
		status, online := statuses[client.ClientID]
		item.ClientID = client.ClientID
		item.ClientRemark = client.Remark
		item.Enabled = client.Enabled
		item.Online = online
		item.Deleted = false
		item.SessionEpoch = status.Epoch
		item.MuxSessions = status.MuxSessions
		item.ActiveConnections = active[client.ClientID]
		items[client.ClientID] = item
	}
	result := make([]networkClientView, 0, len(items))
	for _, item := range items {
		result = append(result, item)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ActiveConnections != result[j].ActiveConnections {
			return result[i].ActiveConnections > result[j].ActiveConnections
		}
		if result[i].LastSeenAt != result[j].LastSeenAt {
			return result[i].LastSeenAt > result[j].LastSeenAt
		}
		return result[i].ClientID < result[j].ClientID
	})
	return result, nil
}

func matchesNetworkQuery(connection store.NetworkConnection, query store.NetworkQuery) bool {
	if query.ClientID != "" && connection.ClientID != query.ClientID {
		return false
	}
	if query.Protocol != "" && connection.Protocol != query.Protocol {
		return false
	}
	if query.DateFrom > 0 && connection.AcceptedAt < query.DateFrom {
		return false
	}
	if query.DateTo > 0 && connection.AcceptedAt >= query.DateTo {
		return false
	}
	if len(query.States) > 0 {
		found := false
		for _, state := range query.States {
			if connection.State == state {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	if query.Query == "" {
		return true
	}
	haystack := strings.ToLower(strings.Join([]string{
		connection.ActivityID, connection.ClientID, connection.ClientRemark,
		connection.ProxyID, connection.ProxyRemark, connection.ExternalAddr, connection.TargetAddr,
	}, "\n"))
	return strings.Contains(haystack, strings.ToLower(strings.TrimSpace(query.Query)))
}

func sortNetworkConnections(items []store.NetworkConnection, order string) {
	if order == "" {
		order = "accepted_desc"
	}
	sort.Slice(items, func(i, j int) bool {
		left, right := items[i], items[j]
		switch order {
		case "accepted_asc":
			return left.AcceptedAt < right.AcceptedAt
		case "traffic_desc":
			return left.UpBytes+left.DownBytes > right.UpBytes+right.DownBytes
		case "duration_desc":
			now := time.Now().UTC().UnixMilli()
			leftEnd, rightEnd := now, now
			if left.ClosedAt != nil {
				leftEnd = *left.ClosedAt
			}
			if right.ClosedAt != nil {
				rightEnd = *right.ClosedAt
			}
			return leftEnd-left.AcceptedAt > rightEnd-right.AcceptedAt
		default:
			if left.AcceptedAt == right.AcceptedAt {
				return left.ActivityID > right.ActivityID
			}
			return left.AcceptedAt > right.AcceptedAt
		}
	})
}
