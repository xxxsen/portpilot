package server

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/portpilot/portpilot/internal/geoip"
	"github.com/portpilot/portpilot/internal/store"
)

const analyticsQueryTimeout = 2 * time.Second

var analyticsIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$`)

var (
	errInvalidAnalyticsRange = errors.New("invalid analytics range")
	errInvalidAnalyticsScope = errors.New("invalid analytics scope")
)

type analyticsQueryCounter struct {
	queries       atomic.Uint64
	durationNanos atomic.Int64
	timeouts      atomic.Uint64
}

type analyticsQueryMetrics struct {
	overview   analyticsQueryCounter
	dimensions analyticsQueryCounter
	topIPs     analyticsQueryCounter
	ipSeries   analyticsQueryCounter
	comparison analyticsQueryCounter
}

type analyticsQueryCounterSnapshot struct {
	QueryTotal         uint64 `json:"query_total"`
	QueryDurationNanos int64  `json:"query_duration_nanos"`
	QueryTimeoutTotal  uint64 `json:"query_timeout_total"`
}

type analyticsQueryMetricsSnapshot struct {
	Overview   analyticsQueryCounterSnapshot `json:"overview"`
	Dimensions analyticsQueryCounterSnapshot `json:"dimensions"`
	TopIPs     analyticsQueryCounterSnapshot `json:"top_ips"`
	IPSeries   analyticsQueryCounterSnapshot `json:"ip_series"`
	Comparison analyticsQueryCounterSnapshot `json:"comparison"`
}

type analyticsRange struct {
	Requested         string `json:"requested"`
	EffectiveFrom     int64  `json:"effective_from"`
	EffectiveTo       int64  `json:"effective_to"`
	ResolutionSeconds int    `json:"resolution_seconds"`
	Timezone          string `json:"timezone"`
}

type analyticsScope struct {
	ClientID string `json:"client_id"`
	ProxyID  string `json:"proxy_id"`
	Protocol string `json:"protocol"`
}

type analyticsCompleteness struct {
	Status                     string  `json:"status"`
	CompleteFrom               int64   `json:"complete_from"`
	StatisticsAvailableFrom    int64   `json:"statistics_available_from"`
	StatisticsRetentionSeconds int64   `json:"statistics_retention_seconds"`
	DetailsAvailableFrom       int64   `json:"details_available_from"`
	EstimatedBytes             int64   `json:"estimated_bytes"`
	EstimatedRatio             float64 `json:"estimated_ratio"`
	GeoIPStatus                string  `json:"geoip_status"`
	GeoIPDatabaseFingerprint   string  `json:"geoip_database_fingerprint"`
}

type analyticsMetadata struct {
	Range        analyticsRange        `json:"range"`
	Scope        analyticsScope        `json:"scope"`
	Completeness analyticsCompleteness `json:"completeness"`
}

type analyticsOverviewResponse struct {
	analyticsMetadata
	store.AnalyticsOverview
	FailureRate float64 `json:"failure_rate"`
}

type analyticsDimensionView struct {
	store.AnalyticsDimension
	ClientDeleted bool `json:"client_deleted"`
	ProxyDeleted  bool `json:"proxy_deleted"`
}

type analyticsDimensionsResponse struct {
	analyticsMetadata
	Items      []analyticsDimensionView `json:"items"`
	Page       int                      `json:"page"`
	PageSize   int                      `json:"page_size"`
	Total      int64                    `json:"total"`
	TotalPages int                      `json:"total_pages"`
}

type analyticsTopIPsResponse struct {
	analyticsMetadata
	store.AnalyticsTopIPs
}

type analyticsIPSeriesResponse struct {
	analyticsMetadata
	store.AnalyticsIPSeries
}

func (a *App) handleNetworkAnalyticsOverview(w http.ResponseWriter, r *http.Request) {
	query, metadata, err := a.analyticsQuery(r)
	if err != nil {
		writeAnalyticsQueryError(w, err)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.networkAnalyticsQueryTimeout())
	defer cancel()
	started := time.Now()
	result, err := a.Events.NetworkAnalyticsOverview(ctx, query)
	a.analytics.overview.observe(time.Since(started), err)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	result.CountryDistribution = condenseAnalyticsCountries(result.CountryDistribution)
	metadata.Completeness = a.analyticsCompleteness(ctx, result.EstimatedBytes, result.UpBytes+result.DownBytes)
	failureRate := float64(0)
	if result.Connections > 0 {
		failureRate = float64(result.Failures) / float64(result.Connections)
	}
	writeJSON(w, http.StatusOK, analyticsOverviewResponse{
		analyticsMetadata: metadata,
		AnalyticsOverview: result,
		FailureRate:       failureRate,
	})
}

func (a *App) handleNetworkAnalyticsDimensions(w http.ResponseWriter, r *http.Request) {
	query, metadata, err := a.analyticsQuery(r)
	if err != nil {
		writeAnalyticsQueryError(w, err)
		return
	}
	query.Page, err = positiveInt(r.URL.Query().Get("page"), 1)
	if err != nil {
		writeAnalyticsQueryError(w, err)
		return
	}
	query.PageSize, err = positiveInt(r.URL.Query().Get("page_size"), 25)
	if err != nil || (query.PageSize != 25 && query.PageSize != 50 && query.PageSize != 100) {
		writeAnalyticsQueryError(w, err)
		return
	}
	query.Sort = strings.TrimSpace(r.URL.Query().Get("sort"))
	if query.Sort != "" &&
		query.Sort != "traffic_desc" &&
		query.Sort != "connections_desc" &&
		query.Sort != "unique_ips_desc" &&
		query.Sort != "failures_desc" {
		writeAnalyticsQueryError(w, errors.New("invalid sort"))
		return
	}
	query.Search = r.URL.Query().Get("query")
	if len(query.Search) > 128 {
		writeAnalyticsQueryError(w, errors.New("query is too long"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.networkAnalyticsQueryTimeout())
	defer cancel()
	started := time.Now()
	result, err := a.Events.NetworkAnalyticsDimensions(ctx, query)
	if err != nil {
		a.analytics.dimensions.observe(time.Since(started), err)
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	totals, err := a.Events.NetworkAnalyticsTotals(ctx, query)
	a.analytics.dimensions.observe(time.Since(started), err)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	metadata.Completeness = a.analyticsCompleteness(
		ctx,
		totals.EstimatedBytes,
		totals.UpBytes+totals.DownBytes,
	)
	deleted := a.analyticsDeletedDimensions()
	items := make([]analyticsDimensionView, 0, len(result.Items))
	for _, item := range result.Items {
		_, clientExists := deleted[item.ClientID]
		_, proxyExists := deleted[item.ClientID+"/"+item.ProxyID]
		items = append(items, analyticsDimensionView{
			AnalyticsDimension: item,
			ClientDeleted:      !clientExists,
			ProxyDeleted:       !proxyExists,
		})
	}
	writeJSON(w, http.StatusOK, analyticsDimensionsResponse{
		analyticsMetadata: metadata,
		Items:             items,
		Page:              result.Page,
		PageSize:          result.PageSize,
		Total:             result.Total,
		TotalPages:        result.TotalPages,
	})
}

func (a *App) handleNetworkAnalyticsTopIPs(w http.ResponseWriter, r *http.Request) {
	query, metadata, err := a.analyticsQuery(r)
	if err != nil {
		writeAnalyticsQueryError(w, err)
		return
	}
	limit, err := positiveInt(r.URL.Query().Get("k"), 10)
	if err != nil || (limit != 5 && limit != 10 && limit != 20) {
		writeAnalyticsQueryError(w, errors.New("k must be 5, 10, or 20"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.networkAnalyticsQueryTimeout())
	defer cancel()
	started := time.Now()
	result, err := a.Events.NetworkAnalyticsTopIPs(ctx, query, limit)
	a.analytics.topIPs.observe(time.Since(started), err)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	metadata.Completeness = a.analyticsCompleteness(
		ctx,
		result.EstimatedBytes,
		result.TotalTraffic,
	)
	writeJSON(w, http.StatusOK, analyticsTopIPsResponse{
		analyticsMetadata: metadata,
		AnalyticsTopIPs:   result,
	})
}

func (a *App) handleNetworkAnalyticsIPSeries(w http.ResponseWriter, r *http.Request) {
	query, metadata, err := a.analyticsQuery(r)
	if err != nil {
		writeAnalyticsQueryError(w, err)
		return
	}
	ip, ok := geoip.NormalizeExternalIP(r.URL.Query().Get("source_ip"))
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid_ip"})
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), a.networkAnalyticsQueryTimeout())
	defer cancel()
	started := time.Now()
	result, err := a.Events.NetworkAnalyticsIPSeries(ctx, query, ip.String())
	a.analytics.ipSeries.observe(time.Since(started), err)
	if err != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "activity_store_unavailable"})
		return
	}
	var estimatedBytes int64
	for _, point := range result.Series {
		estimatedBytes += point.EstimatedBytes
	}
	metadata.Completeness = a.analyticsCompleteness(
		ctx,
		estimatedBytes,
		result.UpBytes+result.DownBytes,
	)
	writeJSON(w, http.StatusOK, analyticsIPSeriesResponse{
		analyticsMetadata: metadata,
		AnalyticsIPSeries: result,
	})
}

func (c *analyticsQueryCounter) observe(duration time.Duration, err error) {
	c.queries.Add(1)
	c.durationNanos.Add(duration.Nanoseconds())
	if errors.Is(err, context.DeadlineExceeded) {
		c.timeouts.Add(1)
	}
}

func (c *analyticsQueryCounter) snapshot() analyticsQueryCounterSnapshot {
	return analyticsQueryCounterSnapshot{
		QueryTotal:         c.queries.Load(),
		QueryDurationNanos: c.durationNanos.Load(),
		QueryTimeoutTotal:  c.timeouts.Load(),
	}
}

func (m *analyticsQueryMetrics) snapshot() analyticsQueryMetricsSnapshot {
	return analyticsQueryMetricsSnapshot{
		Overview:   m.overview.snapshot(),
		Dimensions: m.dimensions.snapshot(),
		TopIPs:     m.topIPs.snapshot(),
		IPSeries:   m.ipSeries.snapshot(), Comparison: m.comparison.snapshot(),
	}
}

func (a *App) networkAnalyticsQueryTimeout() time.Duration {
	if a.analyticsTimeout > 0 {
		return a.analyticsTimeout
	}
	return analyticsQueryTimeout
}

func (a *App) analyticsQuery(r *http.Request) (store.AnalyticsQuery, analyticsMetadata, error) {
	rangeValue, err := a.analyticsRange(r)
	if err != nil {
		return store.AnalyticsQuery{}, analyticsMetadata{}, err
	}
	scope := analyticsScope{
		ClientID: strings.TrimSpace(r.URL.Query().Get("client_id")),
		ProxyID:  strings.TrimSpace(r.URL.Query().Get("proxy_id")),
		Protocol: strings.TrimSpace(r.URL.Query().Get("protocol")),
	}
	if scope.ClientID != "" && !analyticsIDPattern.MatchString(scope.ClientID) {
		return store.AnalyticsQuery{}, analyticsMetadata{}, errInvalidAnalyticsScope
	}
	if scope.ProxyID != "" && !validAnalyticsProxyID(scope.ProxyID) {
		return store.AnalyticsQuery{}, analyticsMetadata{}, errInvalidAnalyticsScope
	}
	if scope.ProxyID != "" && scope.ClientID == "" {
		return store.AnalyticsQuery{}, analyticsMetadata{}, errInvalidAnalyticsScope
	}
	if scope.Protocol != "" && scope.Protocol != "tcp" && scope.Protocol != "udp" {
		return store.AnalyticsQuery{}, analyticsMetadata{}, errInvalidAnalyticsScope
	}
	baseResolution := store.AnalyticsHourlyResolutionSeconds
	if rangeValue.Requested == "1h" || rangeValue.Requested == "24h" {
		baseResolution = store.AnalyticsFineResolutionSeconds
	}
	if rangeValue.Requested == "custom" &&
		rangeValue.EffectiveFrom >= time.Now().UTC().Add(
			-a.Config.Duration(a.Config.Observability.NetworkFineRollupRetention),
		).UnixMilli() {
		baseResolution = store.AnalyticsFineResolutionSeconds
	}
	return store.AnalyticsQuery{
		From:             rangeValue.EffectiveFrom,
		To:               rangeValue.EffectiveTo,
		BaseResolution:   baseResolution,
		OutputResolution: rangeValue.ResolutionSeconds,
		ClientID:         scope.ClientID,
		ProxyID:          scope.ProxyID,
		Protocol:         scope.Protocol,
	}, analyticsMetadata{Range: rangeValue, Scope: scope}, nil
}

func (a *App) analyticsRange(r *http.Request) (analyticsRange, error) {
	requested := strings.TrimSpace(r.URL.Query().Get("range"))
	if requested == "" {
		requested = "24h"
	}
	now := time.Now().UTC()
	type quickRange struct {
		duration   time.Duration
		base       int
		resolution int
	}
	quick := map[string]quickRange{
		"1h":  {time.Hour, store.AnalyticsFineResolutionSeconds, 300},
		"24h": {24 * time.Hour, store.AnalyticsFineResolutionSeconds, 3600},
		"7d":  {7 * 24 * time.Hour, store.AnalyticsHourlyResolutionSeconds, 21600},
		"30d": {30 * 24 * time.Hour, store.AnalyticsHourlyResolutionSeconds, 86400},
		"90d": {90 * 24 * time.Hour, store.AnalyticsHourlyResolutionSeconds, 86400},
	}
	if value, ok := quick[requested]; ok {
		if value.duration > a.Config.Duration(a.Config.Observability.NetworkStatisticsRetention) {
			return analyticsRange{}, errInvalidAnalyticsRange
		}
		resolutionMillis := int64(value.resolution) * 1000
		effectiveTo := alignTimestampUp(now.UnixMilli(), resolutionMillis)
		return analyticsRange{
			Requested: requested, EffectiveFrom: effectiveTo - value.duration.Milliseconds(),
			EffectiveTo: effectiveTo, ResolutionSeconds: value.resolution, Timezone: "UTC",
		}, nil
	}
	if requested != "custom" {
		return analyticsRange{}, errInvalidAnalyticsRange
	}
	from, err := strconv.ParseInt(r.URL.Query().Get("from"), 10, 64)
	if err != nil || from <= 0 {
		return analyticsRange{}, errInvalidAnalyticsRange
	}
	to, err := strconv.ParseInt(r.URL.Query().Get("to"), 10, 64)
	if err != nil || to <= from || to > now.UnixMilli() {
		return analyticsRange{}, errInvalidAnalyticsRange
	}
	durationMillis := to - from
	if durationMillis > a.Config.Duration(a.Config.Observability.NetworkStatisticsRetention).Milliseconds() {
		return analyticsRange{}, errInvalidAnalyticsRange
	}
	base := store.AnalyticsHourlyResolutionSeconds
	if from >= now.Add(-a.Config.Duration(a.Config.Observability.NetworkFineRollupRetention)).UnixMilli() {
		base = store.AnalyticsFineResolutionSeconds
	}
	baseMillis := int64(base) * 1000
	effectiveFrom := alignTimestampUp(from, baseMillis)
	effectiveTo := (to / baseMillis) * baseMillis
	if effectiveFrom >= effectiveTo {
		return analyticsRange{}, errInvalidAnalyticsRange
	}
	resolution := analyticsOutputResolution(effectiveTo-effectiveFrom, base)
	return analyticsRange{
		Requested: "custom", EffectiveFrom: effectiveFrom, EffectiveTo: effectiveTo,
		ResolutionSeconds: resolution, Timezone: "UTC",
	}, nil
}

func analyticsOutputResolution(durationMillis int64, base int) int {
	for _, resolution := range []int{300, 900, 3600, 21600, 86400, 604800} {
		if resolution < base || resolution%base != 0 {
			continue
		}
		points := durationMillis / (int64(resolution) * 1000)
		if durationMillis%(int64(resolution)*1000) != 0 {
			points++
		}
		if points <= 180 {
			return resolution
		}
	}
	return 604800
}

func alignTimestampUp(value, interval int64) int64 {
	return ((value + interval - 1) / interval) * interval
}

func validAnalyticsProxyID(value string) bool {
	return utf8.ValidString(value) &&
		len(value) <= 128 &&
		!strings.ContainsFunc(value, unicode.IsControl)
}

func (a *App) analyticsCompleteness(
	ctx context.Context,
	estimatedBytes int64,
	totalTraffic int64,
) analyticsCompleteness {
	now := time.Now().UTC()
	result := analyticsCompleteness{
		Status:                     "unavailable",
		StatisticsAvailableFrom:    now.Add(-a.Config.Duration(a.Config.Observability.NetworkStatisticsRetention)).UnixMilli(),
		StatisticsRetentionSeconds: int64(a.Config.Duration(a.Config.Observability.NetworkStatisticsRetention).Seconds()),
		DetailsAvailableFrom:       now.Add(-a.Config.Duration(a.Config.Observability.NetworkRetention)).UnixMilli(),
		EstimatedBytes:             estimatedBytes,
		GeoIPStatus:                "disabled",
	}
	if totalTraffic > 0 {
		result.EstimatedRatio = float64(estimatedBytes) / float64(totalTraffic)
	}
	if a.Activity != nil {
		if metadata, err := a.Activity.AnalyticsMeta(ctx); err == nil {
			result.Status = metadata.Status
			result.CompleteFrom = metadata.CompleteFrom
		} else {
			result.Status = "degraded"
		}
	} else if a.Events != nil {
		if metadata, err := a.Events.AnalyticsMeta(ctx); err == nil {
			result.Status = metadata.Status
			result.CompleteFrom = metadata.CompleteFrom
		}
	}
	if a.GeoIP != nil {
		status := a.GeoIP.Status()
		result.GeoIPStatus = status.Status
		result.GeoIPDatabaseFingerprint = status.DatabaseFingerprint
	}
	return result
}

func (a *App) analyticsDeletedDimensions() map[string]struct{} {
	result := make(map[string]struct{})
	if a.ConfigManager == nil {
		return result
	}
	for _, client := range a.ConfigManager.Snapshot().ClientConfig {
		result[client.ClientID] = struct{}{}
		for _, proxy := range client.ProxyList {
			result[client.ClientID+"/"+proxy.ProxyID] = struct{}{}
		}
	}
	return result
}

func condenseAnalyticsCountries(items []store.AnalyticsCountry) []store.AnalyticsCountry {
	countries := make([]store.AnalyticsCountry, 0)
	special := make([]store.AnalyticsCountry, 0)
	for _, item := range items {
		if item.GeoKind == "country" {
			countries = append(countries, item)
		} else {
			special = append(special, item)
		}
	}
	sort.Slice(countries, func(i, j int) bool {
		left := countries[i].UpBytes + countries[i].DownBytes
		right := countries[j].UpBytes + countries[j].DownBytes
		if left != right {
			return left > right
		}
		return countries[i].CountryCode < countries[j].CountryCode
	})
	result := make([]store.AnalyticsCountry, 0, min(len(countries), 10)+len(special)+1)
	result = append(result, countries[:min(len(countries), 10)]...)
	if len(countries) > 10 {
		other := store.AnalyticsCountry{GeoKind: "other", CountryCode: "OTHER"}
		for _, item := range countries[10:] {
			other.Connections += item.Connections
			other.UniqueIPs += item.UniqueIPs
			other.UpBytes += item.UpBytes
			other.DownBytes += item.DownBytes
			other.TrafficShare += item.TrafficShare
		}
		result = append(result, other)
	}
	result = append(result, special...)
	return result
}

func writeAnalyticsQueryError(w http.ResponseWriter, err error) {
	code := "invalid_query"
	switch {
	case errors.Is(err, errInvalidAnalyticsRange):
		code = "invalid_range"
	case errors.Is(err, errInvalidAnalyticsScope):
		code = "invalid_scope"
	}
	writeJSON(w, http.StatusBadRequest, map[string]string{"error": code})
}
