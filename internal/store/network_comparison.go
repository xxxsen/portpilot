package store

import (
	"context"
	"fmt"
	"sort"
)

type ComparisonSource struct {
	AnalyticsTopIP
}

type AnalyticsComparisonData struct {
	Current            AnalyticsOverview
	Previous           AnalyticsOverview
	CurrentTop         AnalyticsTopIPs
	PreviousTop        AnalyticsTopIPs
	CurrentDimensions  []AnalyticsDimension
	PreviousDimensions []AnalyticsDimension
	NewSources         []ComparisonSource
	NewSourceCount     int64
	NewCountries       []string
}

func (s *Store) ComparisonCacheHits() uint64 {
	return s.comparisonCacheHits.Load()
}

func (s *Store) NetworkAnalyticsComparison(
	ctx context.Context,
	current AnalyticsQuery,
	previous AnalyticsQuery,
	k int,
) (AnalyticsComparisonData, error) {
	var err error
	current, err = normalizeAnalyticsQuery(current)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	previous, err = normalizeAnalyticsQuery(previous)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	if k != 5 && k != 10 && k != 20 {
		return AnalyticsComparisonData{}, fmt.Errorf("comparison k must be 5, 10, or 20")
	}
	key := s.analyticsKey(
		fmt.Sprintf("comparison:%d:%d:%d", previous.From, previous.To, k),
		current,
	)
	if result, ok := analyticsCacheGet[AnalyticsComparisonData](s, key); ok {
		s.comparisonCacheHits.Add(1)
		return result, nil
	}
	currentOverview, err := s.NetworkAnalyticsOverview(ctx, current)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	previousOverview, err := s.NetworkAnalyticsOverview(ctx, previous)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	currentTop, err := s.comparisonTopIPs(ctx, current, 2*k)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	previousTop, err := s.comparisonTopIPs(ctx, previous, 2*k)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	currentDimensions, err := s.comparisonDimensions(ctx, current)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	previousDimensions, err := s.comparisonDimensions(ctx, previous)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	newSources, newSourceCount, err := s.comparisonNewSources(ctx, current, previous)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	newCountries, err := s.comparisonNewCountries(ctx, current, previous)
	if err != nil {
		return AnalyticsComparisonData{}, err
	}
	result := AnalyticsComparisonData{
		Current: currentOverview, Previous: previousOverview,
		CurrentTop: currentTop, PreviousTop: previousTop,
		CurrentDimensions: currentDimensions, PreviousDimensions: previousDimensions,
		NewSources: newSources, NewSourceCount: newSourceCount,
		NewCountries: newCountries,
	}
	s.putAnalyticsCache(key, result)
	return result, nil
}

func (s *Store) comparisonTopIPs(
	ctx context.Context,
	query AnalyticsQuery,
	limit int,
) (AnalyticsTopIPs, error) {
	totals, err := s.networkAnalyticsTotals(ctx, query)
	if err != nil {
		return AnalyticsTopIPs{}, err
	}
	byConnections, err := s.queryAnalyticsTopIPs(ctx, query, limit, "connections")
	if err != nil {
		return AnalyticsTopIPs{}, err
	}
	byTraffic, err := s.queryAnalyticsTopIPs(ctx, query, limit, "traffic")
	if err != nil {
		return AnalyticsTopIPs{}, err
	}
	if err := s.populateLatestAnalyticsGeo(ctx, query, byConnections, byTraffic); err != nil {
		return AnalyticsTopIPs{}, err
	}
	totalTraffic := totals.UpBytes + totals.DownBytes
	for _, ranking := range [][]AnalyticsTopIP{byConnections, byTraffic} {
		for index := range ranking {
			ranking[index].Rank = index + 1
			if totals.Connections > 0 {
				ranking[index].ConnectionShare = float64(ranking[index].Connections) / float64(totals.Connections)
			}
			if totalTraffic > 0 {
				ranking[index].TrafficShare = float64(ranking[index].UpBytes+ranking[index].DownBytes) / float64(totalTraffic)
			}
		}
	}
	return AnalyticsTopIPs{
		ByConnections: byConnections, ByTraffic: byTraffic,
		EstimatedBytes: totals.EstimatedBytes, TotalTraffic: totalTraffic,
	}, nil
}

func (s *Store) comparisonDimensions(
	ctx context.Context,
	query AnalyticsQuery,
) ([]AnalyticsDimension, error) {
	where, arguments := analyticsWhere(query)
	//nolint:gosec // Both WHERE fragments are produced by normalized analytics query builders.
	rows, err := s.db.QueryContext(ctx, `
		SELECT r.client_id, r.proxy_id, r.protocol,
			COALESCE(SUM(r.connections), 0),
			COALESCE(SUM(r.failures), 0),
			COUNT(DISTINCT r.source_ip),
			COUNT(DISTINCT CASE WHEN r.geo_kind='country' THEN r.country_code END),
			COALESCE(SUM(r.up_bytes), 0),
			COALESCE(SUM(r.down_bytes), 0),
			MIN(r.first_seen_at), MAX(r.last_seen_at)
		FROM network_history_ip_rollups r`+where+`
		GROUP BY r.client_id, r.proxy_id, r.protocol
	`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query comparison proxy dimensions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]AnalyticsDimension, 0)
	for rows.Next() {
		var item AnalyticsDimension
		if err := rows.Scan(
			&item.ClientID, &item.ProxyID, &item.Protocol,
			&item.Connections, &item.Failures, &item.UniqueIPs, &item.Countries,
			&item.UpBytes, &item.DownBytes, &item.FirstSeenAt, &item.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scan comparison proxy dimension: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate comparison proxy dimensions: %w", err)
	}
	return result, nil
}

func (s *Store) comparisonNewSources(
	ctx context.Context,
	current AnalyticsQuery,
	previous AnalyticsQuery,
) ([]ComparisonSource, int64, error) {
	currentWhere, currentArgs := analyticsWhere(current)
	previousWhere, previousArgs := analyticsWhere(previous)
	arguments := append(append([]any{}, currentArgs...), previousArgs...)
	//nolint:gosec // Both WHERE fragments are produced by normalized analytics query builders.
	rows, err := s.db.QueryContext(ctx, `
		WITH current_sources AS (
			SELECT r.source_ip,
				SUM(r.connections) AS connections,
				SUM(r.failures) AS failures,
				SUM(r.up_bytes) AS up_bytes,
				SUM(r.down_bytes) AS down_bytes,
				MIN(r.first_seen_at) AS first_seen_at,
				MAX(r.last_seen_at) AS last_seen_at
			FROM network_history_ip_rollups r`+currentWhere+`
			GROUP BY r.source_ip
		), previous_sources AS (
			SELECT r.source_ip
			FROM network_history_ip_rollups r`+previousWhere+`
			GROUP BY r.source_ip
		), new_sources AS (
			SELECT current_sources.*
			FROM current_sources
			LEFT JOIN previous_sources USING(source_ip)
			WHERE previous_sources.source_ip IS NULL
		)
		SELECT source_ip, connections, failures, up_bytes, down_bytes,
			first_seen_at, last_seen_at, COUNT(*) OVER()
		FROM new_sources
		ORDER BY (up_bytes+down_bytes) DESC, connections DESC, source_ip
		LIMIT 10
	`, arguments...)
	if err != nil {
		return nil, 0, fmt.Errorf("query comparison new sources: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]ComparisonSource, 0, 10)
	var total int64
	for rows.Next() {
		var item ComparisonSource
		if err := rows.Scan(
			&item.SourceIP, &item.Connections, &item.Failures,
			&item.UpBytes, &item.DownBytes, &item.FirstSeenAt, &item.LastSeenAt,
			&total,
		); err != nil {
			return nil, 0, fmt.Errorf("scan comparison new source: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, fmt.Errorf("iterate comparison new sources: %w", err)
	}
	topItems := make([]AnalyticsTopIP, len(items))
	for index := range items {
		topItems[index] = items[index].AnalyticsTopIP
	}
	if err := s.populateLatestAnalyticsGeo(ctx, current, topItems); err != nil {
		return nil, 0, err
	}
	for index := range items {
		items[index].AnalyticsTopIP = topItems[index]
	}
	return items, total, nil
}

func (s *Store) comparisonNewCountries(
	ctx context.Context,
	current AnalyticsQuery,
	previous AnalyticsQuery,
) ([]string, error) {
	currentWhere, currentArgs := analyticsWhere(current)
	previousWhere, previousArgs := analyticsWhere(previous)
	arguments := append(append([]any{}, currentArgs...), previousArgs...)
	//nolint:gosec // Both WHERE fragments are produced by normalized analytics query builders.
	rows, err := s.db.QueryContext(ctx, `
		WITH current_countries AS (
			SELECT DISTINCT r.country_code
			FROM network_history_ip_rollups r`+currentWhere+`
			  AND r.geo_kind='country'
		), previous_countries AS (
			SELECT DISTINCT r.country_code
			FROM network_history_ip_rollups r`+previousWhere+`
			  AND r.geo_kind='country'
		)
		SELECT country_code FROM current_countries
		EXCEPT SELECT country_code FROM previous_countries
	`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query comparison new countries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]string, 0)
	for rows.Next() {
		var country string
		if err := rows.Scan(&country); err != nil {
			return nil, fmt.Errorf("scan comparison row: %w", err)
		}
		result = append(result, country)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan comparison row: %w", err)
	}
	sort.Strings(result)
	return result, nil
}
