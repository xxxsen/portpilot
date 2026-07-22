package server

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

type AttentionDestination struct {
	View   string            `json:"view"`
	Params map[string]string `json:"params"`
}

type AttentionItem struct {
	ID              string               `json:"id"`
	Severity        string               `json:"severity"`
	Kind            string               `json:"kind"`
	Title           string               `json:"title"`
	Summary         string               `json:"summary"`
	FirstObservedAt int64                `json:"first_observed_at"`
	UpdatedAt       int64                `json:"updated_at"`
	ClientID        string               `json:"client_id"`
	ProxyID         string               `json:"proxy_id"`
	Count           int                  `json:"count"`
	Destination     AttentionDestination `json:"destination"`
}

type AttentionService struct {
	app                *App
	now                func() time.Time
	mu                 sync.Mutex
	firstObserved      map[string]int64
	comparisonMu       sync.Mutex
	comparisonLoadedAt time.Time
	comparison         []AttentionItem
}

func NewAttentionService(app *App) *AttentionService {
	return &AttentionService{app: app, now: time.Now, firstObserved: make(map[string]int64)}
}

func (s *AttentionService) Snapshot(ctx context.Context) []AttentionItem {
	_, _, runtime := s.app.ProxyStatus.Snapshot(ctx)
	now := s.now().UTC().UnixMilli()
	items := make([]AttentionItem, 0)
	if s.app.Degraded() {
		items = append(items, AttentionItem{
			ID: "system_degraded", Severity: "critical", Kind: "system_degraded",
			Title: "服务观测能力降级", Summary: "部分管理数据暂时不可用",
			Destination: AttentionDestination{View: "overview", Params: map[string]string{}},
		})
	}
	offline := make(map[string][]ProxyRuntimeStatus)
	degraded := make(map[string][]ProxyRuntimeStatus)
	for _, value := range runtime {
		switch value.Availability {
		case "error":
			items = append(items, AttentionItem{
				ID:       "listener_error:" + value.ClientID + ":" + value.ProxyID,
				Severity: "critical", Kind: "listener_error",
				Title:    value.ProxyRemark + " 监听异常",
				Summary:  value.Listener.LastErrorCode,
				ClientID: value.ClientID, ProxyID: value.ProxyID, Count: 1,
				Destination: AttentionDestination{
					View:   "proxies",
					Params: map[string]string{"client_id": value.ClientID, "proxy_id": value.ProxyID},
				},
			})
		case "waiting_client":
			offline[value.ClientID] = append(offline[value.ClientID], value)
		case "degraded":
			degraded[value.ClientID] = append(degraded[value.ClientID], value)
		}
	}
	for clientID, values := range offline {
		remark := values[0].ClientRemark
		if remark == "" {
			remark = clientID
		}
		items = append(items, AttentionItem{
			ID: "client_offline:" + clientID, Severity: "warning", Kind: "client_offline",
			Title: remark + " 离线", Summary: countSummary(len(values), "条已启用代理正在等待客户端"),
			ClientID: clientID, Count: len(values),
			Destination: AttentionDestination{
				View: "clients", Params: map[string]string{"client_id": clientID},
			},
		})
	}
	for clientID, values := range degraded {
		remark := values[0].ClientRemark
		if remark == "" {
			remark = clientID
		}
		items = append(items, AttentionItem{
			ID: "client_degraded:" + clientID, Severity: "warning", Kind: "client_degraded",
			Title: remark + " 承载降级", Summary: countSummary(len(values), "条代理的数据承载条件不完整"),
			ClientID: clientID, Count: len(values),
			Destination: AttentionDestination{
				View: "clients", Params: map[string]string{"client_id": clientID},
			},
		})
	}
	if s.app.Activity == nil || s.app.Events == nil || s.app.Activity.Degraded() || s.app.Events.Degraded() {
		items = append(items, analyticsAttention())
	} else if metadata, err := s.app.Activity.AnalyticsMeta(ctx); err != nil || metadata.Status != "ready" {
		items = append(items, analyticsAttention())
	}
	if s.app.GeoIP != nil {
		status := s.app.GeoIP.Status()
		if status.SourceType != "disabled" && status.Status != "ready" {
			items = append(items, AttentionItem{
				ID: "geoip_degraded", Severity: "info", Kind: "geoip_degraded",
				Title: "GeoIP 数据降级", Summary: status.LastErrorCode,
				Destination: AttentionDestination{
					View: "network", Params: map[string]string{"network_section": "insights"},
				},
			})
		}
	}
	items = append(items, s.comparisonItems(ctx, time.UnixMilli(now))...)
	s.observe(items, now)
	sort.Slice(items, func(i, j int) bool {
		left, right := severityPriority(items[i].Severity), severityPriority(items[j].Severity)
		if left != right {
			return left < right
		}
		if items[i].FirstObservedAt != items[j].FirstObservedAt {
			return items[i].FirstObservedAt < items[j].FirstObservedAt
		}
		return items[i].ID < items[j].ID
	})
	return items
}

func (s *AttentionService) observe(items []AttentionItem, now int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := make(map[string]struct{}, len(items))
	for index := range items {
		current[items[index].ID] = struct{}{}
		first := s.firstObserved[items[index].ID]
		if first == 0 {
			first = now
			s.firstObserved[items[index].ID] = now
		}
		items[index].FirstObservedAt = first
		items[index].UpdatedAt = now
	}
	for id := range s.firstObserved {
		if _, ok := current[id]; !ok {
			delete(s.firstObserved, id)
		}
	}
}

func analyticsAttention() AttentionItem {
	return AttentionItem{
		ID: "analytics_degraded", Severity: "warning", Kind: "analytics_degraded",
		Title: "历史分析尚未就绪", Summary: "历史统计正在恢复或暂时不可用",
		Destination: AttentionDestination{
			View: "network", Params: map[string]string{"network_section": "insights"},
		},
	}
}

func countSummary(count int, suffix string) string {
	return fmt.Sprintf("%d %s", count, suffix)
}

func severityPriority(value string) int {
	switch value {
	case "critical":
		return 0
	case "warning":
		return 1
	default:
		return 2
	}
}
