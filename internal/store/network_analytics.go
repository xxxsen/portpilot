package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"sort"
	"strings"
	"time"
)

const (
	AnalyticsFineResolutionSeconds   = 300
	AnalyticsHourlyResolutionSeconds = 3600
	analyticsCacheMaxEntries         = 256
)

type analyticsCacheKey struct {
	Version          uint64
	Kind             string
	From             int64
	To               int64
	BaseResolution   int
	OutputResolution int
	ClientID         string
	ProxyID          string
	Protocol         string
	Search           string
	Page             int
	PageSize         int
	Sort             string
	Limit            int
	SourceIP         string
}

type GeoClassification struct {
	Kind                string
	CountryCode         string
	DatabaseFingerprint string
}

type AnalyticsFinalizeResult struct {
	Aggregated     bool
	SampleOverflow bool
	EstimatedBytes int64
}

type AnalyticsMeta struct {
	Status               string `json:"status"`
	CompleteFrom         int64  `json:"complete_from"`
	ProcessedConnections int64  `json:"processed_connections"`
	LastError            string `json:"-"`
	UpdatedAt            int64  `json:"updated_at"`
}

type AnalyticsQuery struct {
	From             int64
	To               int64
	BaseResolution   int
	OutputResolution int
	ClientID         string
	ProxyID          string
	Protocol         string
	Search           string
	Page             int
	PageSize         int
	Sort             string
}

type AnalyticsSeriesPoint struct {
	BucketAt       int64 `json:"bucket_at"`
	Connections    int64 `json:"connections"`
	Failures       int64 `json:"failures"`
	UpBytes        int64 `json:"up_bytes"`
	DownBytes      int64 `json:"down_bytes"`
	EstimatedBytes int64 `json:"estimated_bytes"`
}

type AnalyticsCountry struct {
	GeoKind      string  `json:"geo_kind"`
	CountryCode  string  `json:"country_code"`
	Connections  int64   `json:"connections"`
	UniqueIPs    int64   `json:"unique_ips"`
	UpBytes      int64   `json:"up_bytes"`
	DownBytes    int64   `json:"down_bytes"`
	TrafficShare float64 `json:"traffic_share"`
}

type AnalyticsOverview struct {
	Connections         int64                  `json:"connections"`
	Failures            int64                  `json:"failures"`
	UniqueIPs           int64                  `json:"unique_ips"`
	Countries           int64                  `json:"countries"`
	UpBytes             int64                  `json:"up_bytes"`
	DownBytes           int64                  `json:"down_bytes"`
	EstimatedBytes      int64                  `json:"estimated_bytes"`
	Series              []AnalyticsSeriesPoint `json:"series"`
	CountryDistribution []AnalyticsCountry     `json:"country_distribution"`
}

type AnalyticsTotals struct {
	Connections    int64
	Failures       int64
	UniqueIPs      int64
	Countries      int64
	UpBytes        int64
	DownBytes      int64
	EstimatedBytes int64
}

type AnalyticsDimension struct {
	ClientID       string `json:"client_id"`
	ClientRemark   string `json:"client_remark"`
	ProxyID        string `json:"proxy_id"`
	ProxyRemark    string `json:"proxy_remark"`
	Protocol       string `json:"protocol"`
	Connections    int64  `json:"connections"`
	Failures       int64  `json:"failures"`
	UniqueIPs      int64  `json:"unique_ips"`
	Countries      int64  `json:"countries"`
	UpBytes        int64  `json:"up_bytes"`
	DownBytes      int64  `json:"down_bytes"`
	TopCountryCode string `json:"top_country_code"`
	FirstSeenAt    int64  `json:"first_seen_at"`
	LastSeenAt     int64  `json:"last_seen_at"`
}

type AnalyticsDimensionPage struct {
	Items      []AnalyticsDimension `json:"items"`
	Page       int                  `json:"page"`
	PageSize   int                  `json:"page_size"`
	Total      int64                `json:"total"`
	TotalPages int                  `json:"total_pages"`
}

type AnalyticsTopIP struct {
	Rank            int     `json:"rank"`
	SourceIP        string  `json:"source_ip"`
	GeoKind         string  `json:"geo_kind"`
	CountryCode     string  `json:"country_code"`
	Connections     int64   `json:"connections"`
	Failures        int64   `json:"failures"`
	UpBytes         int64   `json:"up_bytes"`
	DownBytes       int64   `json:"down_bytes"`
	ConnectionShare float64 `json:"connection_share"`
	TrafficShare    float64 `json:"traffic_share"`
	FirstSeenAt     int64   `json:"first_seen_at"`
	LastSeenAt      int64   `json:"last_seen_at"`
}

type AnalyticsTopIPs struct {
	ByConnections  []AnalyticsTopIP `json:"by_connections"`
	ByTraffic      []AnalyticsTopIP `json:"by_traffic"`
	EstimatedBytes int64            `json:"-"`
	TotalTraffic   int64            `json:"-"`
}

type AnalyticsIPSeries struct {
	SourceIP    string                 `json:"source_ip"`
	GeoKind     string                 `json:"geo_kind"`
	CountryCode string                 `json:"country_code"`
	Connections int64                  `json:"connections"`
	Failures    int64                  `json:"failures"`
	UpBytes     int64                  `json:"up_bytes"`
	DownBytes   int64                  `json:"down_bytes"`
	FirstSeenAt int64                  `json:"first_seen_at"`
	LastSeenAt  int64                  `json:"last_seen_at"`
	Series      []AnalyticsSeriesPoint `json:"series"`
}

type analyticsContribution struct {
	BucketAt           int64
	Connections        int64
	Failures           int64
	UpBytes            int64
	DownBytes          int64
	UpPackets          int64
	DownPackets        int64
	EstimatedUpBytes   int64
	EstimatedDownBytes int64
	FirstSeenAt        int64
	LastSeenAt         int64
}

func (s *Store) EnsureAnalyticsMeta(ctx context.Context, now int64) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_analytics_meta (
		id, status, complete_from, processed_connections, last_error, updated_at
	) VALUES (1, 'pending', NULL, 0, '', ?)
	ON CONFLICT(id) DO NOTHING`, now)
	if err != nil {
		return fmt.Errorf("initialize network analytics metadata: %w", err)
	}
	return nil
}

func (s *Store) AnalyticsMeta(ctx context.Context) (AnalyticsMeta, error) {
	var result AnalyticsMeta
	var completeFrom sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT status, complete_from,
		processed_connections, last_error, updated_at
		FROM network_analytics_meta WHERE id=1`).Scan(
		&result.Status, &completeFrom, &result.ProcessedConnections,
		&result.LastError, &result.UpdatedAt,
	)
	if err != nil {
		return AnalyticsMeta{}, fmt.Errorf("read network analytics metadata: %w", err)
	}
	if completeFrom.Valid {
		result.CompleteFrom = completeFrom.Int64
	}
	return result, nil
}

func (s *Store) UpdateAnalyticsMeta(
	ctx context.Context,
	status string,
	completeFrom int64,
	processedDelta int64,
	lastError string,
	now int64,
) error {
	if status != "pending" && status != "backfilling" && status != "ready" && status != "degraded" {
		return fmt.Errorf("invalid network analytics status %q", status)
	}
	var complete any
	if completeFrom > 0 {
		complete = completeFrom
	}
	_, err := s.db.ExecContext(ctx, `UPDATE network_analytics_meta SET
		status=?, complete_from=COALESCE(?, complete_from),
		processed_connections=processed_connections+?,
		last_error=?, updated_at=? WHERE id=1`,
		status, complete, processedDelta, lastError, now,
	)
	if err != nil {
		return fmt.Errorf("update network analytics metadata: %w", err)
	}
	return nil
}

func (s *Store) ListUnaggregatedConnections(
	ctx context.Context,
	limit int,
) ([]NetworkConnection, error) {
	if limit < 1 || limit > 500 {
		return nil, errors.New("analytics backfill limit must be between 1 and 500")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+networkConnectionColumns+`
		FROM network_connections
		WHERE analytics_aggregated_at IS NULL
		  AND state IN ('closed', 'failed', 'interrupted')
		ORDER BY accepted_at DESC, activity_id DESC
		LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query unaggregated network connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]NetworkConnection, 0, limit)
	for rows.Next() {
		connection, scanErr := scanNetworkConnection(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, connection)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate unaggregated network connections: %w", err)
	}
	return result, nil
}

func (s *Store) CountUnaggregatedConnections(ctx context.Context) (int64, error) {
	total, _, err := s.AnalyticsBackfillState(ctx)
	return total, err
}

func (s *Store) AnalyticsBackfillState(ctx context.Context) (int64, int64, error) {
	var total int64
	var oldest sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*), MIN(accepted_at)
		FROM network_connections
		WHERE analytics_aggregated_at IS NULL
		  AND state IN ('closed', 'failed', 'interrupted')`).Scan(&total, &oldest)
	if err != nil {
		return 0, 0, fmt.Errorf("read network analytics backfill state: %w", err)
	}
	if !oldest.Valid {
		return total, 0, nil
	}
	return total, oldest.Int64, nil
}

func (s *Store) CleanupAnalytics(
	ctx context.Context,
	now int64,
	fineRetention time.Duration,
	statisticsRetention time.Duration,
) error {
	if fineRetention <= 0 || statisticsRetention <= 0 || fineRetention > statisticsRetention {
		return errors.New("invalid network analytics retention")
	}
	s.invalidateAnalyticsCache()
	const keyColumns = `resolution_seconds, bucket_at, client_id, proxy_id, protocol,
		source_ip, geo_kind, country_code, geo_database_fingerprint`
	for _, cutoff := range []struct {
		resolution int
		before     int64
	}{
		{AnalyticsFineResolutionSeconds, now - fineRetention.Milliseconds()},
		{AnalyticsHourlyResolutionSeconds, now - statisticsRetention.Milliseconds()},
	} {
		query := `DELETE FROM network_history_ip_rollups
			WHERE (` + keyColumns + `) IN (
				SELECT ` + keyColumns + ` FROM network_history_ip_rollups
				WHERE resolution_seconds=? AND bucket_at<?
				ORDER BY bucket_at LIMIT 1000
			)`
		if err := executeAnalyticsCleanupBatches(
			ctx,
			func() (sql.Result, error) {
				return s.db.ExecContext(ctx, query, cutoff.resolution, cutoff.before)
			},
		); err != nil {
			s.degraded.Store(true)
			return err
		}
	}
	if err := executeAnalyticsCleanupBatches(
		ctx,
		func() (sql.Result, error) {
			return s.db.ExecContext(ctx, `DELETE FROM network_history_proxy_dimensions
				WHERE (client_id, proxy_id) IN (
					SELECT d.client_id, d.proxy_id
					FROM network_history_proxy_dimensions d
					WHERE NOT EXISTS (
						SELECT 1 FROM network_history_ip_rollups r
						WHERE r.client_id=d.client_id AND r.proxy_id=d.proxy_id
					)
					LIMIT 1000
				)`)
		},
	); err != nil {
		s.degraded.Store(true)
		return err
	}
	s.invalidateAnalyticsCache()
	s.degraded.Store(false)
	return nil
}

func executeAnalyticsCleanupBatches(
	ctx context.Context,
	execute func() (sql.Result, error),
) error {
	for batch := 0; batch < 10; batch++ {
		result, err := execute()
		if err != nil {
			return fmt.Errorf("clean network analytics data: %w", err)
		}
		affected, err := result.RowsAffected()
		if err != nil {
			return fmt.Errorf("read network analytics cleanup result: %w", err)
		}
		if affected < 1000 {
			return nil
		}
		runtime.Gosched()
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("clean network analytics data canceled: %w", err)
		}
	}
	return nil
}

func (s *Store) FinalizeNetworkConnectionAndAggregate(
	ctx context.Context,
	connection NetworkConnection,
	geo GeoClassification,
	now int64,
) (AnalyticsFinalizeResult, error) {
	if connection.State != "closed" && connection.State != "failed" && connection.State != "interrupted" {
		return AnalyticsFinalizeResult{}, fmt.Errorf("cannot aggregate non-terminal state %q", connection.State)
	}
	if connection.ClosedAt == nil {
		return AnalyticsFinalizeResult{}, errors.New("terminal network connection requires closed_at")
	}
	if geo.Kind == "" {
		geo.Kind = "unknown"
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return AnalyticsFinalizeResult{}, fmt.Errorf("begin network analytics transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var aggregatedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT analytics_aggregated_at
		FROM network_connections WHERE activity_id=?`, connection.ActivityID).Scan(&aggregatedAt)
	if err != nil {
		return AnalyticsFinalizeResult{}, fmt.Errorf("read network analytics marker: %w", err)
	}
	result, err := tx.ExecContext(ctx, `UPDATE network_connections SET
		state=?, transport=?, connected_at=?, last_activity_at=?, closed_at=?,
		external_ip=?, up_bytes=?, down_bytes=?, up_packets=?, down_packets=?,
		close_reason_code=?, close_reason=?,
		up_limit_bytes_per_second=?, down_limit_bytes_per_second=?,
		up_throttled_millis=?, down_throttled_millis=?,
		up_rate_limited_packets=?, down_rate_limited_packets=?,
		up_rate_limited_bytes=?, down_rate_limited_bytes=?,
		proxy_protocol_enabled=?, proxy_protocol_version=?, proxy_protocol_header_sent=?
		WHERE activity_id=?`,
		connection.State, connection.Transport, connection.ConnectedAt,
		connection.LastActivityAt, connection.ClosedAt, connection.ExternalIP,
		connection.UpBytes, connection.DownBytes, connection.UpPackets, connection.DownPackets,
		connection.CloseReasonCode, connection.CloseReason,
		connection.UpLimitBytesPerSecond, connection.DownLimitBytesPerSecond,
		connection.UpThrottledMillis, connection.DownThrottledMillis,
		connection.UpRateLimitedPackets, connection.DownRateLimitedPackets,
		connection.UpRateLimitedBytes, connection.DownRateLimitedBytes,
		connection.ProxyProtocolEnabled, connection.ProxyProtocolVersion,
		connection.ProxyProtocolHeaderSent, connection.ActivityID,
	)
	if err != nil {
		return AnalyticsFinalizeResult{}, fmt.Errorf("finalize network connection: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil || affected != 1 {
		return AnalyticsFinalizeResult{}, sql.ErrNoRows
	}
	if aggregatedAt.Valid {
		if err := tx.Commit(); err != nil {
			return AnalyticsFinalizeResult{}, fmt.Errorf("commit already aggregated connection: %w", err)
		}
		return AnalyticsFinalizeResult{}, nil
	}

	samples, err := networkSamplesForUpdate(ctx, tx, connection.ActivityID)
	if err != nil {
		return AnalyticsFinalizeResult{}, err
	}
	contributions, overflow := buildAnalyticsContributions(connection, samples)
	var estimatedBytes int64
	for _, contribution := range contributions[AnalyticsFineResolutionSeconds] {
		estimatedBytes += contribution.EstimatedUpBytes + contribution.EstimatedDownBytes
	}
	for _, resolution := range []int{
		AnalyticsFineResolutionSeconds,
		AnalyticsHourlyResolutionSeconds,
	} {
		for _, contribution := range contributions[resolution] {
			if err := upsertAnalyticsContribution(
				ctx, tx, resolution, connection, geo, contribution,
			); err != nil {
				return AnalyticsFinalizeResult{}, err
			}
		}
	}
	if err := upsertAnalyticsDimension(ctx, tx, connection); err != nil {
		return AnalyticsFinalizeResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE network_connections
		SET analytics_aggregated_at=? WHERE activity_id=?`,
		now, connection.ActivityID,
	); err != nil {
		return AnalyticsFinalizeResult{}, fmt.Errorf("mark network analytics aggregation: %w", err)
	}
	s.invalidateAnalyticsCache()
	if err := tx.Commit(); err != nil {
		return AnalyticsFinalizeResult{}, fmt.Errorf("commit network analytics transaction: %w", err)
	}
	s.degraded.Store(false)
	return AnalyticsFinalizeResult{
		Aggregated: true, SampleOverflow: overflow, EstimatedBytes: estimatedBytes,
	}, nil
}

func networkSamplesForUpdate(
	ctx context.Context,
	tx *sql.Tx,
	activityID string,
) ([]NetworkSample, error) {
	rows, err := tx.QueryContext(ctx, `SELECT activity_id, bucket_at,
		up_bytes_delta, down_bytes_delta, up_packets_delta, down_packets_delta
		FROM network_samples WHERE activity_id=? ORDER BY bucket_at`, activityID)
	if err != nil {
		return nil, fmt.Errorf("query samples for network analytics: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]NetworkSample, 0)
	for rows.Next() {
		var sample NetworkSample
		if err := rows.Scan(
			&sample.ActivityID, &sample.BucketAt, &sample.UpBytesDelta,
			&sample.DownBytesDelta, &sample.UpPacketsDelta, &sample.DownPacketsDelta,
		); err != nil {
			return nil, fmt.Errorf("scan sample for network analytics: %w", err)
		}
		result = append(result, sample)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate samples for network analytics: %w", err)
	}
	return result, nil
}

func buildAnalyticsContributions(
	connection NetworkConnection,
	samples []NetworkSample,
) (map[int]map[int64]*analyticsContribution, bool) {
	fine := make(map[int64]*analyticsContribution)
	upRemaining := connection.UpBytes
	downRemaining := connection.DownBytes
	upPacketsRemaining := connection.UpPackets
	downPacketsRemaining := connection.DownPackets
	overflow := false
	for _, sample := range samples {
		up := consumeAnalyticsValue(&upRemaining, sample.UpBytesDelta, &overflow)
		down := consumeAnalyticsValue(&downRemaining, sample.DownBytesDelta, &overflow)
		upPackets := consumeAnalyticsValue(&upPacketsRemaining, sample.UpPacketsDelta, &overflow)
		downPackets := consumeAnalyticsValue(&downPacketsRemaining, sample.DownPacketsDelta, &overflow)
		if up == 0 && down == 0 && upPackets == 0 && downPackets == 0 {
			continue
		}
		bucket := analyticsBucket(sample.BucketAt, AnalyticsFineResolutionSeconds)
		item := analyticsContributionAt(fine, bucket, bucket)
		item.UpBytes += up
		item.DownBytes += down
		item.UpPackets += upPackets
		item.DownPackets += downPackets
		item.LastSeenAt = maxInt64(item.LastSeenAt, sample.BucketAt)
	}
	closedAt := *connection.ClosedAt
	if upRemaining > 0 || downRemaining > 0 || upPacketsRemaining > 0 || downPacketsRemaining > 0 {
		bucket := analyticsBucket(closedAt, AnalyticsFineResolutionSeconds)
		item := analyticsContributionAt(fine, bucket, closedAt)
		item.UpBytes += upRemaining
		item.DownBytes += downRemaining
		item.UpPackets += upPacketsRemaining
		item.DownPackets += downPacketsRemaining
		item.EstimatedUpBytes += upRemaining
		item.EstimatedDownBytes += downRemaining
	}
	acceptedBucket := analyticsBucket(connection.AcceptedAt, AnalyticsFineResolutionSeconds)
	accepted := analyticsContributionAt(fine, acceptedBucket, connection.AcceptedAt)
	accepted.Connections++
	if connection.State == "failed" {
		accepted.Failures++
	}
	accepted.FirstSeenAt = minPositive(accepted.FirstSeenAt, connection.AcceptedAt)
	accepted.LastSeenAt = maxInt64(accepted.LastSeenAt, connection.LastActivityAt)

	hourly := make(map[int64]*analyticsContribution)
	for _, item := range fine {
		bucket := analyticsBucket(item.BucketAt, AnalyticsHourlyResolutionSeconds)
		hour := analyticsContributionAt(hourly, bucket, item.FirstSeenAt)
		hour.Connections += item.Connections
		hour.Failures += item.Failures
		hour.UpBytes += item.UpBytes
		hour.DownBytes += item.DownBytes
		hour.UpPackets += item.UpPackets
		hour.DownPackets += item.DownPackets
		hour.EstimatedUpBytes += item.EstimatedUpBytes
		hour.EstimatedDownBytes += item.EstimatedDownBytes
		hour.FirstSeenAt = minPositive(hour.FirstSeenAt, item.FirstSeenAt)
		hour.LastSeenAt = maxInt64(hour.LastSeenAt, item.LastSeenAt)
	}
	return map[int]map[int64]*analyticsContribution{
		AnalyticsFineResolutionSeconds:   fine,
		AnalyticsHourlyResolutionSeconds: hourly,
	}, overflow
}

func consumeAnalyticsValue(remaining *int64, value int64, overflow *bool) int64 {
	if value <= 0 || *remaining <= 0 {
		if value > 0 && *remaining <= 0 {
			*overflow = true
		}
		return 0
	}
	if value > *remaining {
		value = *remaining
		*overflow = true
	}
	*remaining -= value
	return value
}

func analyticsContributionAt(
	values map[int64]*analyticsContribution,
	bucket int64,
	seenAt int64,
) *analyticsContribution {
	item := values[bucket]
	if item == nil {
		item = &analyticsContribution{
			BucketAt: bucket, FirstSeenAt: seenAt, LastSeenAt: seenAt,
		}
		values[bucket] = item
	}
	return item
}

func upsertAnalyticsContribution(
	ctx context.Context,
	tx *sql.Tx,
	resolution int,
	connection NetworkConnection,
	geo GeoClassification,
	value *analyticsContribution,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO network_history_ip_rollups (
		resolution_seconds, bucket_at, client_id, proxy_id, protocol, source_ip,
		geo_kind, country_code, geo_database_fingerprint,
		connections, failures, up_bytes, down_bytes, up_packets, down_packets,
		estimated_up_bytes, estimated_down_bytes, first_seen_at, last_seen_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT (
		resolution_seconds, bucket_at, client_id, proxy_id, protocol, source_ip,
		geo_kind, country_code, geo_database_fingerprint
	) DO UPDATE SET
		connections=connections+excluded.connections,
		failures=failures+excluded.failures,
		up_bytes=up_bytes+excluded.up_bytes,
		down_bytes=down_bytes+excluded.down_bytes,
		up_packets=up_packets+excluded.up_packets,
		down_packets=down_packets+excluded.down_packets,
		estimated_up_bytes=estimated_up_bytes+excluded.estimated_up_bytes,
		estimated_down_bytes=estimated_down_bytes+excluded.estimated_down_bytes,
		first_seen_at=MIN(first_seen_at, excluded.first_seen_at),
		last_seen_at=MAX(last_seen_at, excluded.last_seen_at)`,
		resolution, value.BucketAt, connection.ClientID, connection.ProxyID,
		connection.Protocol, connection.ExternalIP, geo.Kind, geo.CountryCode,
		geo.DatabaseFingerprint, value.Connections, value.Failures,
		value.UpBytes, value.DownBytes, value.UpPackets, value.DownPackets,
		value.EstimatedUpBytes, value.EstimatedDownBytes,
		value.FirstSeenAt, value.LastSeenAt,
	)
	if err != nil {
		return fmt.Errorf("upsert network analytics contribution: %w", err)
	}
	return nil
}

func upsertAnalyticsDimension(
	ctx context.Context,
	tx *sql.Tx,
	connection NetworkConnection,
) error {
	_, err := tx.ExecContext(ctx, `INSERT INTO network_history_proxy_dimensions (
		client_id, proxy_id, client_remark, proxy_remark, first_seen_at, last_seen_at
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(client_id, proxy_id) DO UPDATE SET
		client_remark=CASE WHEN excluded.last_seen_at >= last_seen_at
			THEN excluded.client_remark ELSE client_remark END,
		proxy_remark=CASE WHEN excluded.last_seen_at >= last_seen_at
			THEN excluded.proxy_remark ELSE proxy_remark END,
		first_seen_at=MIN(first_seen_at, excluded.first_seen_at),
		last_seen_at=MAX(last_seen_at, excluded.last_seen_at)`,
		connection.ClientID, connection.ProxyID, connection.ClientRemark,
		connection.ProxyRemark, connection.AcceptedAt, connection.LastActivityAt,
	)
	if err != nil {
		return fmt.Errorf("upsert network analytics dimension: %w", err)
	}
	return nil
}

func (s *Store) NetworkAnalyticsOverview(
	ctx context.Context,
	query AnalyticsQuery,
) (AnalyticsOverview, error) {
	query, err := normalizeAnalyticsQuery(query)
	if err != nil {
		return AnalyticsOverview{}, err
	}
	key := s.analyticsKey("overview", query)
	if result, ok := analyticsCacheGet[AnalyticsOverview](s, key); ok {
		return result, nil
	}
	totals, err := s.networkAnalyticsTotals(ctx, query)
	if err != nil {
		return AnalyticsOverview{}, err
	}
	result := AnalyticsOverview{
		Connections: totals.Connections, Failures: totals.Failures,
		UniqueIPs: totals.UniqueIPs, Countries: totals.Countries,
		UpBytes: totals.UpBytes, DownBytes: totals.DownBytes,
		EstimatedBytes: totals.EstimatedBytes,
	}
	result.Series, err = s.networkAnalyticsSeries(ctx, query, "")
	if err != nil {
		return AnalyticsOverview{}, err
	}
	result.CountryDistribution, err = s.networkAnalyticsCountries(ctx, query)
	if err != nil {
		return AnalyticsOverview{}, err
	}
	s.putAnalyticsCache(key, result)
	return result, nil
}

func (s *Store) NetworkAnalyticsTotals(
	ctx context.Context,
	query AnalyticsQuery,
) (AnalyticsTotals, error) {
	query, err := normalizeAnalyticsQuery(query)
	if err != nil {
		return AnalyticsTotals{}, err
	}
	return s.networkAnalyticsTotals(ctx, query)
}

func (s *Store) networkAnalyticsTotals(
	ctx context.Context,
	query AnalyticsQuery,
) (AnalyticsTotals, error) {
	key := s.analyticsKey("totals", query)
	if result, ok := analyticsCacheGet[AnalyticsTotals](s, key); ok {
		return result, nil
	}
	where, arguments := analyticsWhere(query)
	var result AnalyticsTotals
	err := s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(r.connections), 0), COALESCE(SUM(r.failures), 0),
		COUNT(DISTINCT r.source_ip),
		COUNT(DISTINCT CASE WHEN r.geo_kind='country' THEN r.country_code END),
		COALESCE(SUM(r.up_bytes), 0), COALESCE(SUM(r.down_bytes), 0),
		COALESCE(SUM(r.estimated_up_bytes+r.estimated_down_bytes), 0)
		FROM network_history_ip_rollups r`+where, arguments...).Scan(
		&result.Connections, &result.Failures, &result.UniqueIPs, &result.Countries,
		&result.UpBytes, &result.DownBytes, &result.EstimatedBytes,
	)
	if err != nil {
		return AnalyticsTotals{}, fmt.Errorf("query network analytics totals: %w", err)
	}
	s.putAnalyticsCache(key, result)
	return result, nil
}

func (s *Store) networkAnalyticsSeries(
	ctx context.Context,
	query AnalyticsQuery,
	sourceIP string,
) ([]AnalyticsSeriesPoint, error) {
	where, arguments := analyticsWhere(query)
	if sourceIP != "" {
		where += " AND r.source_ip=?"
		arguments = append(arguments, sourceIP)
	}
	outputMillis := int64(query.OutputResolution) * 1000
	seriesArguments := []any{query.From, outputMillis, outputMillis, query.From}
	seriesArguments = append(seriesArguments, arguments...)
	//nolint:gosec // The WHERE clause is assembled exclusively from fixed server-side SQL fragments.
	rows, err := s.db.QueryContext(ctx, `SELECT
		((((r.bucket_at - ?) / ?) * ?) + ?) AS output_bucket,
		COALESCE(SUM(r.connections), 0), COALESCE(SUM(r.failures), 0),
		COALESCE(SUM(r.up_bytes), 0), COALESCE(SUM(r.down_bytes), 0),
		COALESCE(SUM(r.estimated_up_bytes+r.estimated_down_bytes), 0)
		FROM network_history_ip_rollups r`+where+`
		GROUP BY output_bucket ORDER BY output_bucket`, seriesArguments...)
	if err != nil {
		return nil, fmt.Errorf("query network analytics series: %w", err)
	}
	defer func() { _ = rows.Close() }()
	points := make(map[int64]AnalyticsSeriesPoint)
	for rows.Next() {
		var point AnalyticsSeriesPoint
		if err := rows.Scan(
			&point.BucketAt, &point.Connections, &point.Failures,
			&point.UpBytes, &point.DownBytes, &point.EstimatedBytes,
		); err != nil {
			return nil, fmt.Errorf("scan network analytics series: %w", err)
		}
		points[point.BucketAt] = point
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate network analytics series: %w", err)
	}
	result := make([]AnalyticsSeriesPoint, 0)
	for bucket := query.From; bucket < query.To; bucket += outputMillis {
		point := points[bucket]
		point.BucketAt = bucket
		result = append(result, point)
	}
	return result, nil
}

func (s *Store) networkAnalyticsCountries(
	ctx context.Context,
	query AnalyticsQuery,
) ([]AnalyticsCountry, error) {
	where, arguments := analyticsWhere(query)
	//nolint:gosec // The WHERE clause is assembled exclusively from fixed server-side SQL fragments.
	rows, err := s.db.QueryContext(ctx, `SELECT r.geo_kind, r.country_code,
		COALESCE(SUM(r.connections), 0), COUNT(DISTINCT r.source_ip),
		COALESCE(SUM(r.up_bytes), 0), COALESCE(SUM(r.down_bytes), 0)
		FROM network_history_ip_rollups r`+where+`
		GROUP BY r.geo_kind, r.country_code`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query network analytics countries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]AnalyticsCountry, 0)
	var totalTraffic int64
	for rows.Next() {
		var item AnalyticsCountry
		if err := rows.Scan(
			&item.GeoKind, &item.CountryCode, &item.Connections,
			&item.UniqueIPs, &item.UpBytes, &item.DownBytes,
		); err != nil {
			return nil, fmt.Errorf("scan network analytics country: %w", err)
		}
		totalTraffic += item.UpBytes + item.DownBytes
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate network analytics countries: %w", err)
	}
	sort.Slice(result, func(i, j int) bool {
		left := result[i].UpBytes + result[i].DownBytes
		right := result[j].UpBytes + result[j].DownBytes
		if left != right {
			return left > right
		}
		return result[i].CountryCode < result[j].CountryCode
	})
	for index := range result {
		if totalTraffic > 0 {
			result[index].TrafficShare = float64(result[index].UpBytes+result[index].DownBytes) /
				float64(totalTraffic)
		}
	}
	return result, nil
}

func (s *Store) NetworkAnalyticsDimensions(
	ctx context.Context,
	query AnalyticsQuery,
) (AnalyticsDimensionPage, error) {
	query, err := normalizeAnalyticsQuery(query)
	if err != nil {
		return AnalyticsDimensionPage{}, err
	}
	key := s.analyticsKey("dimensions", query)
	if result, ok := analyticsCacheGet[AnalyticsDimensionPage](s, key); ok {
		return result, nil
	}
	where, arguments := analyticsWhere(query)
	where, arguments = analyticsDimensionSearch(where, arguments, query.Search)
	var total int64
	err = s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT r.client_id, r.proxy_id, r.protocol
		FROM network_history_ip_rollups r`+where+`
		GROUP BY r.client_id, r.proxy_id, r.protocol
	)`, arguments...).Scan(&total)
	if err != nil {
		return AnalyticsDimensionPage{}, fmt.Errorf("count network analytics dimensions: %w", err)
	}
	order := map[string]string{
		"traffic_desc":     "traffic_bytes DESC, last_seen_at DESC",
		"connections_desc": "connections DESC, traffic_bytes DESC",
		"unique_ips_desc":  "unique_ips DESC, traffic_bytes DESC",
		"failures_desc":    "failures DESC, connections DESC",
	}[query.Sort]
	pageArguments := append([]any{}, arguments...)
	pageArguments = append(pageArguments, query.PageSize, (query.Page-1)*query.PageSize)
	//nolint:gosec // WHERE and ORDER BY are assembled exclusively from fixed server-side SQL fragments.
	rows, err := s.db.QueryContext(ctx, `SELECT
			r.client_id, COALESCE(d.client_remark, ''),
			r.proxy_id, COALESCE(d.proxy_remark, ''), r.protocol,
			COALESCE(SUM(r.connections), 0) AS connections,
			COALESCE(SUM(r.failures), 0) AS failures,
			COUNT(DISTINCT r.source_ip) AS unique_ips,
			COUNT(DISTINCT CASE WHEN r.geo_kind='country' THEN r.country_code END) AS countries,
			COALESCE(SUM(r.up_bytes), 0) AS up_bytes,
			COALESCE(SUM(r.down_bytes), 0) AS down_bytes,
			COALESCE(SUM(r.up_bytes+r.down_bytes), 0) AS traffic_bytes,
			MIN(r.first_seen_at), MAX(r.last_seen_at) AS last_seen_at
		FROM network_history_ip_rollups r
		LEFT JOIN network_history_proxy_dimensions d
		  ON d.client_id=r.client_id AND d.proxy_id=r.proxy_id`+where+`
		GROUP BY r.client_id, r.proxy_id, r.protocol
		ORDER BY `+order+` LIMIT ? OFFSET ?`, pageArguments...)
	if err != nil {
		return AnalyticsDimensionPage{}, fmt.Errorf("query network analytics dimensions: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]AnalyticsDimension, 0, query.PageSize)
	for rows.Next() {
		var item AnalyticsDimension
		var trafficBytes int64
		if err := rows.Scan(
			&item.ClientID, &item.ClientRemark, &item.ProxyID, &item.ProxyRemark,
			&item.Protocol, &item.Connections, &item.Failures, &item.UniqueIPs,
			&item.Countries, &item.UpBytes, &item.DownBytes, &trafficBytes,
			&item.FirstSeenAt, &item.LastSeenAt,
		); err != nil {
			return AnalyticsDimensionPage{}, fmt.Errorf("scan network analytics dimension: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return AnalyticsDimensionPage{}, fmt.Errorf("iterate network analytics dimensions: %w", err)
	}
	if err := rows.Close(); err != nil {
		return AnalyticsDimensionPage{}, fmt.Errorf("close network analytics dimensions: %w", err)
	}
	if err := s.populateAnalyticsTopCountries(ctx, query, items); err != nil {
		return AnalyticsDimensionPage{}, err
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(query.PageSize) - 1) / int64(query.PageSize))
	}
	result := AnalyticsDimensionPage{
		Items: items, Page: query.Page, PageSize: query.PageSize,
		Total: total, TotalPages: totalPages,
	}
	s.putAnalyticsCache(key, result)
	return result, nil
}

func (s *Store) populateAnalyticsTopCountries(
	ctx context.Context,
	query AnalyticsQuery,
	items []AnalyticsDimension,
) error {
	if len(items) == 0 {
		return nil
	}
	valueRows := make([]string, 0, len(items))
	arguments := make([]any, 0, len(items)*3+4)
	for _, item := range items {
		valueRows = append(valueRows, "(?, ?, ?)")
		arguments = append(arguments, item.ClientID, item.ProxyID, item.Protocol)
	}
	arguments = append(
		arguments,
		query.BaseResolution,
		query.From,
		query.To,
	)
	rows, err := s.db.QueryContext(ctx, `WITH selected (
			client_id, proxy_id, protocol
		) AS (VALUES `+strings.Join(valueRows, ",")+`),
		ranked AS (
			SELECT r.client_id, r.proxy_id, r.protocol, r.country_code,
				ROW_NUMBER() OVER (
					PARTITION BY r.client_id, r.proxy_id, r.protocol
					ORDER BY SUM(r.up_bytes+r.down_bytes) DESC, r.country_code ASC
				) AS country_rank
			FROM selected s
			JOIN network_history_ip_rollups r
			  ON r.client_id=s.client_id AND r.proxy_id=s.proxy_id
			  AND r.protocol=s.protocol
			WHERE r.resolution_seconds=? AND r.bucket_at>=? AND r.bucket_at<?
			  AND r.geo_kind='country'
			GROUP BY r.client_id, r.proxy_id, r.protocol, r.country_code
		)
		SELECT client_id, proxy_id, protocol, country_code
		FROM ranked WHERE country_rank=1`, arguments...)
	if err != nil {
		return fmt.Errorf("query network analytics top countries: %w", err)
	}
	defer func() { _ = rows.Close() }()
	topCountries := make(map[string]string, len(items))
	for rows.Next() {
		var clientID, proxyID, protocol, countryCode string
		if err := rows.Scan(&clientID, &proxyID, &protocol, &countryCode); err != nil {
			return fmt.Errorf("scan network analytics top country: %w", err)
		}
		topCountries[clientID+"\x00"+proxyID+"\x00"+protocol] = countryCode
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate network analytics top countries: %w", err)
	}
	for index := range items {
		key := items[index].ClientID + "\x00" + items[index].ProxyID + "\x00" + items[index].Protocol
		items[index].TopCountryCode = topCountries[key]
	}
	return nil
}

func (s *Store) NetworkAnalyticsTopIPs(
	ctx context.Context,
	query AnalyticsQuery,
	limit int,
) (AnalyticsTopIPs, error) {
	query, err := normalizeAnalyticsQuery(query)
	if err != nil {
		return AnalyticsTopIPs{}, err
	}
	if limit != 5 && limit != 10 && limit != 20 {
		return AnalyticsTopIPs{}, errors.New("analytics top IP limit must be 5, 10, or 20")
	}
	key := s.analyticsKey("top_ips", query)
	key.Limit = limit
	if result, ok := analyticsCacheGet[AnalyticsTopIPs](s, key); ok {
		return result, nil
	}
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
	for _, items := range [][]AnalyticsTopIP{byConnections, byTraffic} {
		for index := range items {
			items[index].Rank = index + 1
			if totals.Connections > 0 {
				items[index].ConnectionShare = float64(items[index].Connections) /
					float64(totals.Connections)
			}
			if totalTraffic > 0 {
				items[index].TrafficShare = float64(items[index].UpBytes+items[index].DownBytes) /
					float64(totalTraffic)
			}
		}
	}
	result := AnalyticsTopIPs{
		ByConnections: byConnections, ByTraffic: byTraffic,
		EstimatedBytes: totals.EstimatedBytes, TotalTraffic: totalTraffic,
	}
	s.putAnalyticsCache(key, result)
	return result, nil
}

func (s *Store) queryAnalyticsTopIPs(
	ctx context.Context,
	query AnalyticsQuery,
	limit int,
	orderName string,
) ([]AnalyticsTopIP, error) {
	where, arguments := analyticsWhere(query)
	order := "connections DESC, traffic_bytes DESC, r.source_ip ASC"
	if orderName == "traffic" {
		order = "traffic_bytes DESC, connections DESC, r.source_ip ASC"
	}
	arguments = append(arguments, limit)
	//nolint:gosec // WHERE and ORDER BY are assembled exclusively from fixed server-side SQL fragments.
	rows, err := s.db.QueryContext(ctx, `SELECT r.source_ip,
			COALESCE(SUM(r.connections), 0) AS connections,
			COALESCE(SUM(r.failures), 0) AS failures,
			COALESCE(SUM(r.up_bytes), 0) AS up_bytes,
			COALESCE(SUM(r.down_bytes), 0) AS down_bytes,
			COALESCE(SUM(r.up_bytes+r.down_bytes), 0) AS traffic_bytes,
			MIN(r.first_seen_at), MAX(r.last_seen_at)
		FROM network_history_ip_rollups r`+where+`
		GROUP BY r.source_ip
		ORDER BY `+order+` LIMIT ?`, arguments...)
	if err != nil {
		return nil, fmt.Errorf("query network analytics top IPs: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]AnalyticsTopIP, 0, limit)
	for rows.Next() {
		var item AnalyticsTopIP
		var trafficBytes int64
		if err := rows.Scan(
			&item.SourceIP, &item.Connections, &item.Failures,
			&item.UpBytes, &item.DownBytes, &trafficBytes,
			&item.FirstSeenAt, &item.LastSeenAt,
		); err != nil {
			return nil, fmt.Errorf("scan network analytics top IP: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate network analytics top IPs: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("close network analytics top IPs: %w", err)
	}
	return result, nil
}

func (s *Store) populateLatestAnalyticsGeo(
	ctx context.Context,
	query AnalyticsQuery,
	rankings ...[]AnalyticsTopIP,
) error {
	unique := make(map[string]struct{})
	for _, items := range rankings {
		for _, item := range items {
			unique[item.SourceIP] = struct{}{}
		}
	}
	if len(unique) == 0 {
		return nil
	}
	valueRows := make([]string, 0, len(unique))
	arguments := make([]any, 0, len(unique)+8)
	for sourceIP := range unique {
		valueRows = append(valueRows, "(?)")
		arguments = append(arguments, sourceIP)
	}
	where, rangeArguments := analyticsWhere(query)
	arguments = append(arguments, rangeArguments...)
	rows, err := s.db.QueryContext(ctx, `WITH selected (source_ip) AS (
			VALUES `+strings.Join(valueRows, ",")+`
		), ranked AS (
			SELECT r.source_ip, r.geo_kind, r.country_code,
				ROW_NUMBER() OVER (
					PARTITION BY r.source_ip
					ORDER BY r.last_seen_at DESC, r.geo_database_fingerprint DESC,
						r.geo_kind ASC, r.country_code ASC
				) AS geo_rank
			FROM selected s
			JOIN network_history_ip_rollups r ON r.source_ip=s.source_ip`+where+`
		)
		SELECT source_ip, geo_kind, country_code
		FROM ranked WHERE geo_rank=1`, arguments...)
	if err != nil {
		return fmt.Errorf("query latest network analytics geolocation batch: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type classification struct {
		kind    string
		country string
	}
	values := make(map[string]classification, len(unique))
	for rows.Next() {
		var sourceIP string
		var value classification
		if err := rows.Scan(&sourceIP, &value.kind, &value.country); err != nil {
			return fmt.Errorf("scan latest network analytics geolocation batch: %w", err)
		}
		values[sourceIP] = value
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate latest network analytics geolocation batch: %w", err)
	}
	for _, items := range rankings {
		for index := range items {
			value, ok := values[items[index].SourceIP]
			if !ok {
				items[index].GeoKind = "unknown"
				continue
			}
			items[index].GeoKind = value.kind
			items[index].CountryCode = value.country
		}
	}
	return nil
}

func (s *Store) latestAnalyticsGeo(
	ctx context.Context,
	query AnalyticsQuery,
	sourceIP string,
) (string, string, error) {
	where, arguments := analyticsWhere(query)
	where += " AND r.source_ip=?"
	arguments = append(arguments, sourceIP)
	var kind, country string
	err := s.db.QueryRowContext(ctx, `SELECT r.geo_kind, r.country_code
		FROM network_history_ip_rollups r`+where+`
		ORDER BY r.last_seen_at DESC, r.geo_database_fingerprint DESC
		LIMIT 1`, arguments...).Scan(&kind, &country)
	if err != nil {
		return "", "", fmt.Errorf("query latest network analytics geolocation: %w", err)
	}
	return kind, country, nil
}

func (s *Store) NetworkAnalyticsIPSeries(
	ctx context.Context,
	query AnalyticsQuery,
	sourceIP string,
) (AnalyticsIPSeries, error) {
	query, err := normalizeAnalyticsQuery(query)
	if err != nil {
		return AnalyticsIPSeries{}, err
	}
	sourceIP = strings.TrimSpace(sourceIP)
	if sourceIP == "" {
		return AnalyticsIPSeries{}, errors.New("analytics source IP is required")
	}
	key := s.analyticsKey("ip_series", query)
	key.SourceIP = sourceIP
	if result, ok := analyticsCacheGet[AnalyticsIPSeries](s, key); ok {
		return result, nil
	}
	where, arguments := analyticsWhere(query)
	where += " AND r.source_ip=?"
	arguments = append(arguments, sourceIP)
	var result AnalyticsIPSeries
	result.SourceIP = sourceIP
	err = s.db.QueryRowContext(ctx, `SELECT
		COALESCE(SUM(r.connections), 0), COALESCE(SUM(r.failures), 0),
		COALESCE(SUM(r.up_bytes), 0), COALESCE(SUM(r.down_bytes), 0),
		COALESCE(MIN(r.first_seen_at), 0), COALESCE(MAX(r.last_seen_at), 0)
		FROM network_history_ip_rollups r`+where, arguments...).Scan(
		&result.Connections, &result.Failures, &result.UpBytes, &result.DownBytes,
		&result.FirstSeenAt, &result.LastSeenAt,
	)
	if err != nil {
		return AnalyticsIPSeries{}, fmt.Errorf("query network analytics IP summary: %w", err)
	}
	result.GeoKind, result.CountryCode, err = s.latestAnalyticsGeo(ctx, query, sourceIP)
	if errors.Is(err, sql.ErrNoRows) {
		result.GeoKind = "unknown"
		err = nil
	}
	if err != nil {
		return AnalyticsIPSeries{}, err
	}
	result.Series, err = s.networkAnalyticsSeries(ctx, query, sourceIP)
	if err != nil {
		return AnalyticsIPSeries{}, err
	}
	s.putAnalyticsCache(key, result)
	return result, nil
}

func (s *Store) analyticsKey(kind string, query AnalyticsQuery) analyticsCacheKey {
	return analyticsCacheKey{
		Version: s.analyticsVersion.Load(),
		Kind:    kind, From: query.From, To: query.To,
		BaseResolution: query.BaseResolution, OutputResolution: query.OutputResolution,
		ClientID: query.ClientID, ProxyID: query.ProxyID, Protocol: query.Protocol,
		Search: query.Search, Page: query.Page, PageSize: query.PageSize, Sort: query.Sort,
	}
}

func analyticsCacheGet[T any](s *Store, key analyticsCacheKey) (T, bool) {
	s.analyticsCacheMu.RLock()
	value, ok := s.analyticsCache[key]
	s.analyticsCacheMu.RUnlock()
	if !ok {
		var empty T
		return empty, false
	}
	result, ok := value.(T)
	return result, ok
}

func (s *Store) putAnalyticsCache(key analyticsCacheKey, value any) {
	s.analyticsCacheMu.Lock()
	if len(s.analyticsCache) >= analyticsCacheMaxEntries {
		clear(s.analyticsCache)
	}
	s.analyticsCache[key] = value
	s.analyticsCacheMu.Unlock()
}

func (s *Store) invalidateAnalyticsCache() {
	s.analyticsVersion.Add(1)
	s.analyticsCacheMu.Lock()
	clear(s.analyticsCache)
	s.analyticsCacheMu.Unlock()
}

func normalizeAnalyticsQuery(query AnalyticsQuery) (AnalyticsQuery, error) {
	if query.From <= 0 || query.To <= query.From {
		return AnalyticsQuery{}, errors.New("analytics from must be before to")
	}
	if query.BaseResolution != AnalyticsFineResolutionSeconds &&
		query.BaseResolution != AnalyticsHourlyResolutionSeconds {
		return AnalyticsQuery{}, errors.New("invalid analytics base resolution")
	}
	if query.OutputResolution < query.BaseResolution ||
		query.OutputResolution%query.BaseResolution != 0 {
		return AnalyticsQuery{}, errors.New("invalid analytics output resolution")
	}
	if query.ProxyID != "" && query.ClientID == "" {
		return AnalyticsQuery{}, errors.New("analytics proxy_id requires client_id")
	}
	if query.Protocol != "" && query.Protocol != "tcp" && query.Protocol != "udp" {
		return AnalyticsQuery{}, errors.New("invalid analytics protocol")
	}
	query.Search = strings.TrimSpace(query.Search)
	if len(query.Search) > maxSearchLength {
		return AnalyticsQuery{}, fmt.Errorf("analytics search exceeds %d characters", maxSearchLength)
	}
	if query.Page <= 0 {
		query.Page = 1
	}
	switch query.PageSize {
	case 0:
		query.PageSize = 25
	case 25, 50, 100:
	default:
		return AnalyticsQuery{}, errors.New("analytics page_size must be 25, 50, or 100")
	}
	if query.Sort == "" {
		query.Sort = "traffic_desc"
	}
	switch query.Sort {
	case "traffic_desc", "connections_desc", "unique_ips_desc", "failures_desc":
	default:
		return AnalyticsQuery{}, errors.New("invalid analytics sort")
	}
	return query, nil
}

func NormalizeAnalyticsQuery(query AnalyticsQuery) (AnalyticsQuery, error) {
	return normalizeAnalyticsQuery(query)
}

func analyticsDimensionSearch(where string, arguments []any, search string) (string, []any) {
	search = strings.TrimSpace(search)
	if search == "" {
		return where, arguments
	}
	value := "%" + escapeLike(search) + "%"
	where += ` AND (
		r.client_id LIKE ? ESCAPE '\' OR r.proxy_id LIKE ? ESCAPE '\'
		OR EXISTS (
			SELECT 1 FROM network_history_proxy_dimensions search_dimension
			WHERE search_dimension.client_id=r.client_id
			  AND search_dimension.proxy_id=r.proxy_id
			  AND (
				search_dimension.client_remark LIKE ? ESCAPE '\'
				OR search_dimension.proxy_remark LIKE ? ESCAPE '\'
			  )
		)
	)`
	return where, append(arguments, value, value, value, value)
}

func analyticsWhere(query AnalyticsQuery) (string, []any) {
	const prefix = "r."
	clauses := []string{
		prefix + "resolution_seconds=?",
		prefix + "bucket_at>=?",
		prefix + "bucket_at<?",
	}
	arguments := []any{query.BaseResolution, query.From, query.To}
	if query.ClientID != "" {
		clauses = append(clauses, prefix+"client_id=?")
		arguments = append(arguments, query.ClientID)
	}
	if query.ProxyID != "" {
		clauses = append(clauses, prefix+"proxy_id=?")
		arguments = append(arguments, query.ProxyID)
	}
	if query.Protocol != "" {
		clauses = append(clauses, prefix+"protocol=?")
		arguments = append(arguments, query.Protocol)
	}
	return " WHERE " + strings.Join(clauses, " AND "), arguments
}

func analyticsBucket(value int64, resolutionSeconds int) int64 {
	size := int64(resolutionSeconds) * int64(time.Second/time.Millisecond)
	return value - value%size
}

func minPositive(left, right int64) int64 {
	if left <= 0 || right < left {
		return right
	}
	return left
}

func maxInt64(left, right int64) int64 {
	if right > left {
		return right
	}
	return left
}
