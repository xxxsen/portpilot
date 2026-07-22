package server

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/netip"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

var activityPrefixPattern = regexp.MustCompile(`^[0-9a-fA-F]{8,32}$`)

type SearchDestination struct {
	View   string            `json:"view"`
	Params map[string]string `json:"params"`
}

type SearchItem struct {
	Type        string            `json:"type"`
	ID          string            `json:"id"`
	Title       string            `json:"title"`
	Subtitle    string            `json:"subtitle"`
	Score       int               `json:"score"`
	Destination SearchDestination `json:"destination"`
}

type searchBucket struct {
	tokens float64
	at     time.Time
}

type searchRateLimiter struct {
	mu      sync.Mutex
	buckets map[string]searchBucket
	now     func() time.Time
}

func newSearchRateLimiter() *searchRateLimiter {
	return &searchRateLimiter{buckets: make(map[string]searchBucket), now: time.Now}
}

func (l *searchRateLimiter) allow(key string) bool {
	now := l.now()
	l.mu.Lock()
	defer l.mu.Unlock()
	value := l.buckets[key]
	if value.at.IsZero() {
		value = searchBucket{tokens: 5, at: now}
	} else {
		value.tokens = min(5, value.tokens+now.Sub(value.at).Seconds()*0.5)
		value.at = now
	}
	if value.tokens < 1 {
		l.buckets[key] = value
		return false
	}
	value.tokens--
	l.buckets[key] = value
	if len(l.buckets) > 4096 {
		for id, candidate := range l.buckets {
			if now.Sub(candidate.at) > 10*time.Minute {
				delete(l.buckets, id)
			}
		}
	}
	return true
}

func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len(query) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "query_too_short"})
		return
	}
	if len(query) > 128 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_query"})
		return
	}
	limit, err := positiveInt(r.URL.Query().Get("limit"), 20)
	if err != nil || limit > 20 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_limit"})
		return
	}
	started := time.Now()
	a.searchStats.requests.Add(1)
	defer a.searchStats.observeDuration(started)
	session := adminFromContext(r)
	a.searchLimiterOnce.Do(func() { a.searchLimiter = newSearchRateLimiter() })
	if !a.searchLimiter.allow(session.ID) {
		a.searchStats.rateLimited.Add(1)
		w.Header().Set("Retry-After", "2")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "rate_limited"})
		return
	}

	items := a.searchConfiguration(query)
	partial := false
	unavailable := make([]string, 0, 1)
	historyItems, historyErr := a.searchHistory(r.Context(), query)
	if historyErr != nil {
		partial = true
		a.searchStats.partial.Add(1)
		if errors.Is(historyErr, context.DeadlineExceeded) {
			a.searchStats.timeouts.Add(1)
		}
		unavailable = append(unavailable, "history")
	} else {
		items = append(items, historyItems...)
	}
	items = deduplicateSearchItems(items)
	sort.Slice(items, func(i, j int) bool {
		if items[i].Score != items[j].Score {
			return items[i].Score > items[j].Score
		}
		left, right := searchTypePriority(items[i].Type), searchTypePriority(items[j].Type)
		if left != right {
			return left < right
		}
		return items[i].ID < items[j].ID
	})
	items = capSearchTypes(items, 5)
	if len(items) > limit {
		items = items[:limit]
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"query": query, "items": items, "partial": partial,
		"unavailable_sources": unavailable,
	})
}

func (a *App) searchConfiguration(query string) []SearchItem {
	lower := strings.ToLower(query)
	file := a.ConfigManager.Snapshot()
	sessions := make(map[string]SessionStatus)
	for _, session := range a.Sessions.Snapshot() {
		sessions[session.ClientID] = session
	}
	items := make([]SearchItem, 0)
	for _, client := range file.ClientConfig {
		score := max(
			searchTextScore(lower, client.ClientID, true, false),
			searchTextScore(lower, client.Remark, false, true),
		)
		if score > 0 {
			state := "OFFLINE"
			if session, ok := sessions[client.ClientID]; ok {
				state = session.State
			}
			title := client.Remark
			if title == "" {
				title = client.ClientID
			}
			items = append(items, SearchItem{
				Type: "client", ID: client.ClientID, Title: title,
				Subtitle: client.ClientID + " · " + state, Score: score,
				Destination: SearchDestination{
					View: "clients", Params: map[string]string{"client_id": client.ClientID},
				},
			})
		}
		for _, proxy := range client.ProxyList {
			data, err := proxy.Tunnel()
			if err != nil {
				continue
			}
			score := max(
				searchTextScore(lower, proxy.ProxyID, true, false),
				searchTextScore(lower, proxy.Remark, false, true),
				searchAddressScore(lower, data.Bind),
				searchAddressScore(lower, data.Connect),
			)
			if score == 0 {
				continue
			}
			title := proxy.Remark
			if title == "" {
				title = proxy.ProxyID
			}
			items = append(items, SearchItem{
				Type: "proxy", ID: client.ClientID + "/" + proxy.ProxyID,
				Title: title, Subtitle: client.ClientID + " · " + data.Network + " · " + data.Bind,
				Score: score,
				Destination: SearchDestination{
					View:   "proxies",
					Params: map[string]string{"client_id": client.ClientID, "proxy_id": proxy.ProxyID},
				},
			})
		}
	}
	return items
}

func (a *App) searchHistory(parent context.Context, query string) ([]SearchItem, error) {
	ctx, cancel := context.WithTimeout(parent, 500*time.Millisecond)
	defer cancel()
	items := make([]SearchItem, 0)
	if ip, err := netip.ParseAddr(query); err == nil {
		normalized := ip.Unmap().String()
		source, err := a.Events.SearchSourceIP(ctx, normalized)
		if err != nil && !errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("search source IP: %w", err)
		}
		if err == nil {
			items = append(items, SearchItem{
				Type: "source_ip", ID: normalized, Title: normalized,
				Subtitle: sourceIPSubtitle(source), Score: 100,
				Destination: SearchDestination{
					View:   "network",
					Params: map[string]string{"network_section": "insights", "source_ip": normalized},
				},
			})
		}
	}
	if activityPrefixPattern.MatchString(query) {
		prefix := strings.ToLower(query)
		active := a.Activity.Snapshot()
		for _, connection := range active {
			if strings.HasPrefix(connection.ActivityID, prefix) {
				items = append(items, activitySearchItem(connection, "active", prefix))
			}
		}
		historical, err := a.Events.SearchActivityPrefix(ctx, prefix, 5)
		if err != nil {
			return nil, fmt.Errorf("search activity prefix: %w", err)
		}
		for _, connection := range historical {
			items = append(items, activitySearchItem(connection, "history", prefix))
		}
	}
	return items, nil
}

func activitySearchItem(connection store.NetworkConnection, view, query string) SearchItem {
	return SearchItem{
		Type: "activity", ID: connection.ActivityID, Title: connection.ActivityID,
		Subtitle: connection.Protocol + " · " + connection.ClientID + " · " + connection.ProxyID + " · " + connection.State,
		Score:    activitySearchScore(connection.ActivityID, query),
		Destination: SearchDestination{
			View: "network",
			Params: map[string]string{
				"network_section": "connections", "connection_view": view,
				"activity_id": connection.ActivityID,
			},
		},
	}
}

func activitySearchScore(activityID, query string) int {
	if strings.EqualFold(activityID, query) {
		return 100
	}
	return 90
}

func sourceIPSubtitle(value store.SourceIPSearchResult) string {
	country := value.CountryCode
	if country == "" {
		country = value.GeoKind
	}
	if country == "" {
		country = "unknown"
	}
	return fmt.Sprintf("%s · %d 次历史连接 · %s", time.UnixMilli(value.LastActivityAt).Format("2006-01-02 15:04"), value.Connections, country)
}

func searchTextScore(query, candidate string, identifier, remark bool) int {
	value := strings.ToLower(candidate)
	switch {
	case value == "" || !strings.Contains(value, query):
		return 0
	case value == query && identifier:
		return 100
	case strings.HasPrefix(value, query) && identifier:
		return 90
	case value == query && remark:
		return 85
	default:
		return 60
	}
}

func searchAddressScore(query, candidate string) int {
	value := strings.ToLower(candidate)
	switch {
	case value == "" || !strings.Contains(value, query):
		return 0
	case value == query:
		return 100
	case strings.HasPrefix(value, query):
		return 80
	default:
		return 60
	}
}

func deduplicateSearchItems(items []SearchItem) []SearchItem {
	best := make(map[string]SearchItem, len(items))
	for _, item := range items {
		key := item.Type + "\x00" + item.ID
		current, ok := best[key]
		if !ok || item.Score > current.Score ||
			(item.Type == "activity" && item.Destination.Params["connection_view"] == "active") {
			best[key] = item
		}
	}
	result := make([]SearchItem, 0, len(best))
	for _, item := range best {
		result = append(result, item)
	}
	return result
}

func capSearchTypes(items []SearchItem, limit int) []SearchItem {
	counts := make(map[string]int)
	result := make([]SearchItem, 0, len(items))
	for _, item := range items {
		if counts[item.Type] >= limit {
			continue
		}
		counts[item.Type]++
		result = append(result, item)
	}
	return result
}

func searchTypePriority(value string) int {
	switch value {
	case "client":
		return 0
	case "proxy":
		return 1
	case "source_ip":
		return 2
	default:
		return 3
	}
}
