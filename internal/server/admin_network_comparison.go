package server

import (
	"context"
	"errors"
	"net/http"
	"sort"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

type metricChange struct {
	Current        float64  `json:"current"`
	Previous       float64  `json:"previous"`
	AbsoluteChange float64  `json:"absolute_change"`
	RelativeChange *float64 `json:"relative_change"`
	State          string   `json:"state"`
	Estimated      bool     `json:"estimated"`
}

type comparisonMetrics struct {
	Connections metricChange `json:"connections"`
	Failures    metricChange `json:"failures"`
	FailureRate metricChange `json:"failure_rate"`
	UniqueIPs   metricChange `json:"unique_ips"`
	UpBytes     metricChange `json:"up_bytes"`
	DownBytes   metricChange `json:"down_bytes"`
	TotalBytes  metricChange `json:"total_bytes"`
}

type alignedAnalyticsPoint struct {
	store.AnalyticsSeriesPoint
	AlignedBucketAt int64 `json:"aligned_bucket_at"`
}

type rankChange struct {
	SourceIP     string `json:"source_ip"`
	GeoKind      string `json:"geo_kind"`
	CountryCode  string `json:"country_code"`
	CurrentRank  *int   `json:"current_rank"`
	PreviousRank *int   `json:"previous_rank"`
	RankChange   *int   `json:"rank_change"`
	Status       string `json:"status"`
}

type proxyChangeSignal struct {
	ClientID            string   `json:"client_id"`
	ProxyID             string   `json:"proxy_id"`
	Protocol            string   `json:"protocol"`
	Signals             []string `json:"signals"`
	CurrentConnections  int64    `json:"current_connections"`
	PreviousConnections int64    `json:"previous_connections"`
	CurrentFailureRate  float64  `json:"current_failure_rate"`
	PreviousFailureRate float64  `json:"previous_failure_rate"`
	CurrentTraffic      int64    `json:"current_traffic"`
	PreviousTraffic     int64    `json:"previous_traffic"`
}

type comparisonResponse struct {
	CurrentMetadata  analyticsMetadata            `json:"current"`
	PreviousMetadata analyticsMetadata            `json:"previous"`
	Comparable       bool                         `json:"comparable"`
	Reason           string                       `json:"reason"`
	Metrics          comparisonMetrics            `json:"metrics"`
	CurrentSeries    []store.AnalyticsSeriesPoint `json:"current_series"`
	PreviousSeries   []alignedAnalyticsPoint      `json:"previous_series"`
	NewSourceCount   int64                        `json:"new_source_count"`
	NewSources       []store.ComparisonSource     `json:"new_sources"`
	NewCountries     []string                     `json:"new_countries"`
	ConnectionsRank  []rankChange                 `json:"connections_rank_changes"`
	TrafficRank      []rankChange                 `json:"traffic_rank_changes"`
	ProxyChanges     []proxyChangeSignal          `json:"proxy_changes"`
}

func (a *App) handleNetworkAnalyticsComparison(w http.ResponseWriter, r *http.Request) {
	currentQuery, currentMetadata, err := a.analyticsQuery(r)
	if err != nil {
		writeAnalyticsQueryError(w, err)
		return
	}
	k, err := positiveInt(r.URL.Query().Get("k"), 10)
	if err != nil || (k != 5 && k != 10 && k != 20) {
		writeAnalyticsQueryError(w, errors.New("k must be 5, 10, or 20"))
		return
	}
	duration := currentQuery.To - currentQuery.From
	previousQuery := currentQuery
	previousQuery.From = currentQuery.From - duration
	previousQuery.To = currentQuery.From
	statisticsFrom := time.Now().UTC().Add(
		-a.Config.Duration(a.Config.Observability.NetworkStatisticsRetention),
	).UnixMilli()
	fineFrom := time.Now().UTC().Add(
		-a.Config.Duration(a.Config.Observability.NetworkFineRollupRetention),
	).UnixMilli()
	if previousQuery.From < fineFrom {
		currentQuery.BaseResolution = store.AnalyticsHourlyResolutionSeconds
		previousQuery.BaseResolution = store.AnalyticsHourlyResolutionSeconds
		if currentQuery.OutputResolution < store.AnalyticsHourlyResolutionSeconds {
			currentQuery.OutputResolution = store.AnalyticsHourlyResolutionSeconds
			previousQuery.OutputResolution = store.AnalyticsHourlyResolutionSeconds
			currentMetadata.Range.ResolutionSeconds = store.AnalyticsHourlyResolutionSeconds
		}
	}
	previousMetadata := currentMetadata
	previousMetadata.Range.EffectiveFrom = previousQuery.From
	previousMetadata.Range.EffectiveTo = previousQuery.To

	ctx, cancel := context.WithTimeout(r.Context(), a.networkAnalyticsQueryTimeout())
	defer cancel()
	started := time.Now()
	data, err := a.Events.NetworkAnalyticsComparison(ctx, currentQuery, previousQuery, k)
	a.analytics.comparison.observe(time.Since(started), err)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	currentTraffic := data.Current.UpBytes + data.Current.DownBytes
	previousTraffic := data.Previous.UpBytes + data.Previous.DownBytes
	currentMetadata.Completeness = a.analyticsCompleteness(
		ctx, data.Current.EstimatedBytes, currentTraffic,
	)
	previousMetadata.Completeness = a.analyticsCompleteness(
		ctx, data.Previous.EstimatedBytes, previousTraffic,
	)
	comparable, reason := comparisonComparable(
		currentMetadata.Completeness, previousMetadata.Completeness,
		currentQuery.From, previousQuery.From, statisticsFrom,
	)
	estimatedTraffic := currentMetadata.Completeness.EstimatedRatio > 0.10 ||
		previousMetadata.Completeness.EstimatedRatio > 0.10
	response := comparisonResponse{
		CurrentMetadata: currentMetadata, PreviousMetadata: previousMetadata,
		Comparable: comparable, Reason: reason,
		CurrentSeries:  data.Current.Series,
		PreviousSeries: alignPreviousSeries(data.Previous.Series, duration),
		NewSourceCount: data.NewSourceCount, NewSources: data.NewSources,
		NewCountries:    data.NewCountries,
		ConnectionsRank: compareRankings(data.CurrentTop.ByConnections, data.PreviousTop.ByConnections, k),
		TrafficRank:     compareRankings(data.CurrentTop.ByTraffic, data.PreviousTop.ByTraffic, k),
		ProxyChanges:    proxyChangeSignals(data.CurrentDimensions, data.PreviousDimensions, estimatedTraffic),
	}
	response.Metrics = comparisonMetricValues(data.Current, data.Previous, estimatedTraffic)
	if !comparable {
		response.ProxyChanges = nil
	}
	writeJSON(w, http.StatusOK, response)
}

func comparisonComparable(
	current analyticsCompleteness,
	previous analyticsCompleteness,
	currentFrom int64,
	previousFrom int64,
	statisticsFrom int64,
) (bool, string) {
	if previousFrom < statisticsFrom {
		return false, "previous_outside_retention"
	}
	if current.Status != "ready" || previous.Status != "ready" {
		return false, "analytics_not_ready"
	}
	if current.CompleteFrom > 0 && current.CompleteFrom > currentFrom {
		return false, "current_incomplete"
	}
	if previous.CompleteFrom > 0 && previous.CompleteFrom > previousFrom {
		return false, "previous_incomplete"
	}
	return true, ""
}

func comparisonMetricValues(
	current store.AnalyticsOverview,
	previous store.AnalyticsOverview,
	estimatedTraffic bool,
) comparisonMetrics {
	currentFailureRate, previousFailureRate := float64(0), float64(0)
	if current.Connections > 0 {
		currentFailureRate = float64(current.Failures) / float64(current.Connections)
	}
	if previous.Connections > 0 {
		previousFailureRate = float64(previous.Failures) / float64(previous.Connections)
	}
	return comparisonMetrics{
		Connections: changeValue(float64(current.Connections), float64(previous.Connections), false),
		Failures:    changeValue(float64(current.Failures), float64(previous.Failures), false),
		FailureRate: rateChange(currentFailureRate, previousFailureRate),
		UniqueIPs:   changeValue(float64(current.UniqueIPs), float64(previous.UniqueIPs), false),
		UpBytes:     changeValue(float64(current.UpBytes), float64(previous.UpBytes), estimatedTraffic),
		DownBytes:   changeValue(float64(current.DownBytes), float64(previous.DownBytes), estimatedTraffic),
		TotalBytes: changeValue(
			float64(current.UpBytes+current.DownBytes),
			float64(previous.UpBytes+previous.DownBytes),
			estimatedTraffic,
		),
	}
}

func changeValue(current, previous float64, estimated bool) metricChange {
	value := metricChange{
		Current: current, Previous: previous,
		AbsoluteChange: current - previous, Estimated: estimated,
	}
	switch {
	case current == 0 && previous == 0:
		value.State = "unchanged"
		relative := float64(0)
		value.RelativeChange = &relative
	case previous == 0:
		value.State = "new"
	case current == 0:
		value.State = "inactive"
		relative := float64(-1)
		value.RelativeChange = &relative
	default:
		relative := (current - previous) / previous
		value.RelativeChange = &relative
		switch {
		case current > previous:
			value.State = "increased"
		case current < previous:
			value.State = "decreased"
		default:
			value.State = "unchanged"
		}
	}
	return value
}

func rateChange(current, previous float64) metricChange {
	value := metricChange{
		Current: current, Previous: previous,
		AbsoluteChange: current - previous,
	}
	switch {
	case current > previous:
		value.State = "increased"
	case current < previous:
		value.State = "decreased"
	default:
		value.State = "unchanged"
	}
	return value
}

func alignPreviousSeries(
	values []store.AnalyticsSeriesPoint,
	duration int64,
) []alignedAnalyticsPoint {
	result := make([]alignedAnalyticsPoint, 0, len(values))
	for _, value := range values {
		result = append(result, alignedAnalyticsPoint{
			AnalyticsSeriesPoint: value, AlignedBucketAt: value.BucketAt + duration,
		})
	}
	return result
}

func compareRankings(
	current []store.AnalyticsTopIP,
	previous []store.AnalyticsTopIP,
	k int,
) []rankChange {
	type pair struct {
		current  *store.AnalyticsTopIP
		previous *store.AnalyticsTopIP
	}
	values := make(map[string]pair)
	for index := range current {
		item := current[index]
		value := values[item.SourceIP]
		value.current = &item
		values[item.SourceIP] = value
	}
	for index := range previous {
		item := previous[index]
		value := values[item.SourceIP]
		value.previous = &item
		values[item.SourceIP] = value
	}
	result := make([]rankChange, 0, len(values))
	for sourceIP, pair := range values {
		value := rankChange{SourceIP: sourceIP}
		switch {
		case pair.current != nil && pair.previous == nil:
			value.Status = "new"
		case pair.current == nil:
			value.Status = "departed"
		default:
			change := pair.previous.Rank - pair.current.Rank
			value.RankChange = &change
			switch {
			case change > 0:
				value.Status = "up"
			case change < 0:
				value.Status = "down"
			default:
				value.Status = "same"
			}
		}
		if pair.current != nil {
			rank := pair.current.Rank
			value.CurrentRank = &rank
			value.GeoKind = pair.current.GeoKind
			value.CountryCode = pair.current.CountryCode
		}
		if pair.previous != nil {
			rank := pair.previous.Rank
			value.PreviousRank = &rank
			if value.GeoKind == "" {
				value.GeoKind = pair.previous.GeoKind
				value.CountryCode = pair.previous.CountryCode
			}
		}
		result = append(result, value)
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := rankSortValue(result[i]), rankSortValue(result[j])
		if left != right {
			return left < right
		}
		return result[i].SourceIP < result[j].SourceIP
	})
	if len(result) > 2*k {
		result = result[:2*k]
	}
	return result
}

func rankSortValue(value rankChange) int {
	if value.CurrentRank != nil {
		return *value.CurrentRank
	}
	if value.PreviousRank != nil {
		return 1000 + *value.PreviousRank
	}
	return 2000
}

func proxyChangeSignals(
	current []store.AnalyticsDimension,
	previous []store.AnalyticsDimension,
	trafficEstimated bool,
) []proxyChangeSignal {
	previousValues := make(map[string]store.AnalyticsDimension, len(previous))
	for _, item := range previous {
		previousValues[dimensionKey(item)] = item
	}
	result := make([]proxyChangeSignal, 0)
	for _, item := range current {
		before := previousValues[dimensionKey(item)]
		currentTraffic := item.UpBytes + item.DownBytes
		previousTraffic := before.UpBytes + before.DownBytes
		currentFailureRate, previousFailureRate := float64(0), float64(0)
		if item.Connections > 0 {
			currentFailureRate = float64(item.Failures) / float64(item.Connections)
		}
		if before.Connections > 0 {
			previousFailureRate = float64(before.Failures) / float64(before.Connections)
		}
		signals := make([]string, 0, 3)
		if item.Connections >= 50 && item.Failures >= 10 && currentFailureRate >= 0.10 &&
			currentFailureRate-previousFailureRate >= 0.10 {
			signals = append(signals, "failure_rate_spike")
		}
		if item.Connections >= 100 &&
			(before.Connections == 0 || item.Connections >= 2*before.Connections) {
			signals = append(signals, "connection_spike")
		}
		if !trafficEstimated && currentTraffic >= 10<<20 &&
			(previousTraffic == 0 || currentTraffic >= 2*previousTraffic) {
			signals = append(signals, "traffic_spike")
		}
		if len(signals) == 0 {
			continue
		}
		result = append(result, proxyChangeSignal{
			ClientID: item.ClientID, ProxyID: item.ProxyID, Protocol: item.Protocol,
			Signals: signals, CurrentConnections: item.Connections,
			PreviousConnections: before.Connections,
			CurrentFailureRate:  currentFailureRate, PreviousFailureRate: previousFailureRate,
			CurrentTraffic: currentTraffic, PreviousTraffic: previousTraffic,
		})
	}
	sort.Slice(result, func(i, j int) bool {
		left, right := signalPriority(result[i]), signalPriority(result[j])
		if left != right {
			return left < right
		}
		leftMagnitude, rightMagnitude := signalMagnitude(result[i]), signalMagnitude(result[j])
		if leftMagnitude != rightMagnitude {
			return leftMagnitude > rightMagnitude
		}
		if result[i].ClientID != result[j].ClientID {
			return result[i].ClientID < result[j].ClientID
		}
		if result[i].ProxyID != result[j].ProxyID {
			return result[i].ProxyID < result[j].ProxyID
		}
		return result[i].Protocol < result[j].Protocol
	})
	if len(result) > 10 {
		result = result[:10]
	}
	return result
}

func signalMagnitude(value proxyChangeSignal) float64 {
	magnitude := value.CurrentFailureRate - value.PreviousFailureRate
	if value.PreviousConnections == 0 && value.CurrentConnections > 0 {
		magnitude = max(magnitude, float64(value.CurrentConnections))
	} else if value.PreviousConnections > 0 {
		magnitude = max(magnitude, float64(value.CurrentConnections)/float64(value.PreviousConnections))
	}
	if value.PreviousTraffic == 0 && value.CurrentTraffic > 0 {
		magnitude = max(magnitude, float64(value.CurrentTraffic))
	} else if value.PreviousTraffic > 0 {
		magnitude = max(magnitude, float64(value.CurrentTraffic)/float64(value.PreviousTraffic))
	}
	return magnitude
}

func dimensionKey(item store.AnalyticsDimension) string {
	return item.ClientID + "\x00" + item.ProxyID + "\x00" + item.Protocol
}

func signalPriority(value proxyChangeSignal) int {
	for _, signal := range value.Signals {
		if signal == "failure_rate_spike" {
			return 0
		}
	}
	for _, signal := range value.Signals {
		if signal == "connection_spike" {
			return 1
		}
	}
	return 2
}
