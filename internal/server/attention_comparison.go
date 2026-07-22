package server

import (
	"context"
	"strings"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

const attentionComparisonCacheTTL = time.Minute

func (s *AttentionService) comparisonItems(ctx context.Context, now time.Time) []AttentionItem {
	s.comparisonMu.Lock()
	defer s.comparisonMu.Unlock()
	if !s.comparisonLoadedAt.IsZero() &&
		now.Sub(s.comparisonLoadedAt) < attentionComparisonCacheTTL {
		return append([]AttentionItem(nil), s.comparison...)
	}
	s.comparisonLoadedAt = now
	s.comparison = nil
	if s.app.Activity == nil || s.app.Events == nil {
		return nil
	}
	metadata, err := s.app.Activity.AnalyticsMeta(ctx)
	if err != nil || metadata.Status != "ready" {
		return nil
	}
	duration := 24 * time.Hour
	base := store.AnalyticsFineResolutionSeconds
	previousFrom := now.Add(-2 * duration)
	if previousFrom.Before(now.Add(-s.app.Config.Duration(
		s.app.Config.Observability.NetworkFineRollupRetention,
	))) {
		base = store.AnalyticsHourlyResolutionSeconds
	}
	baseMillis := int64(base) * 1000
	to := now.UnixMilli() / baseMillis * baseMillis
	current := store.AnalyticsQuery{
		From: to - duration.Milliseconds(), To: to,
		BaseResolution: base, OutputResolution: store.AnalyticsHourlyResolutionSeconds,
		Page: 1, PageSize: 25,
	}
	previous := current
	previous.From = current.From - duration.Milliseconds()
	previous.To = current.From
	retentionFrom := now.Add(-s.app.Config.Duration(
		s.app.Config.Observability.NetworkStatisticsRetention,
	)).UnixMilli()
	if previous.From < retentionFrom || metadata.CompleteFrom > previous.From {
		return nil
	}
	queryCtx, cancel := context.WithTimeout(ctx, analyticsQueryTimeout)
	defer cancel()
	data, err := s.app.Events.NetworkAnalyticsComparison(queryCtx, current, previous, 5)
	if err != nil {
		return nil
	}
	currentTraffic := data.Current.UpBytes + data.Current.DownBytes
	previousTraffic := data.Previous.UpBytes + data.Previous.DownBytes
	estimated := estimatedRatio(data.Current.EstimatedBytes, currentTraffic) > 0.10 ||
		estimatedRatio(data.Previous.EstimatedBytes, previousTraffic) > 0.10
	changes := proxyChangeSignals(data.CurrentDimensions, data.PreviousDimensions, estimated)
	for _, change := range changes {
		signals := attentionSignals(change.Signals)
		if len(signals) == 0 {
			continue
		}
		title := change.ClientID + " / " + change.ProxyID + " 变化异常"
		s.comparison = append(s.comparison, AttentionItem{
			ID:       "proxy_change_spike:" + change.ClientID + ":" + change.ProxyID + ":" + change.Protocol,
			Severity: "warning", Kind: "proxy_change_spike", Title: title,
			Summary:  strings.Join(signals, "、"),
			ClientID: change.ClientID, ProxyID: change.ProxyID, Count: len(signals),
			Destination: AttentionDestination{
				View: "network",
				Params: map[string]string{
					"network_section": "insights", "client_id": change.ClientID,
					"proxy_id": change.ProxyID, "protocol": change.Protocol,
				},
			},
		})
		if len(s.comparison) == 3 {
			break
		}
	}
	return append([]AttentionItem(nil), s.comparison...)
}

func estimatedRatio(estimated, traffic int64) float64 {
	if traffic <= 0 {
		return 0
	}
	return float64(estimated) / float64(traffic)
}

func attentionSignals(values []string) []string {
	result := make([]string, 0, 2)
	for _, value := range values {
		switch value {
		case "failure_rate_spike":
			result = append(result, "失败率显著上升")
		case "connection_spike":
			result = append(result, "连接数显著上升")
		}
	}
	return result
}
