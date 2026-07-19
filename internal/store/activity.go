package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"time"
)

type NetworkConnection struct {
	ActivityID      string `json:"activity_id"`
	Protocol        string `json:"protocol"`
	State           string `json:"state"`
	ClientID        string `json:"client_id"`
	ClientRemark    string `json:"client_remark"`
	ProxyID         string `json:"proxy_id"`
	ProxyRemark     string `json:"proxy_remark"`
	ExternalAddr    string `json:"external_addr"`
	ServerBind      string `json:"server_bind"`
	TargetAddr      string `json:"target_addr"`
	Transport       string `json:"transport"`
	SessionEpoch    uint64 `json:"session_epoch"`
	AcceptedAt      int64  `json:"accepted_at"`
	ConnectedAt     *int64 `json:"connected_at,omitempty"`
	LastActivityAt  int64  `json:"last_activity_at"`
	ClosedAt        *int64 `json:"closed_at,omitempty"`
	UpBytes         int64  `json:"up_bytes"`
	DownBytes       int64  `json:"down_bytes"`
	UpPackets       int64  `json:"up_packets"`
	DownPackets     int64  `json:"down_packets"`
	CloseReasonCode string `json:"close_reason_code"`
	CloseReason     string `json:"close_reason"`
}

type NetworkSample struct {
	ActivityID       string `json:"activity_id"`
	BucketAt         int64  `json:"bucket_at"`
	UpBytesDelta     int64  `json:"up_bytes_delta"`
	DownBytesDelta   int64  `json:"down_bytes_delta"`
	UpPacketsDelta   int64  `json:"up_packets_delta"`
	DownPacketsDelta int64  `json:"down_packets_delta"`
}

type HourlyRollup struct {
	BucketAt           int64
	ScopeClientID      string
	Protocol           string
	ConnectionsStarted int64
	ConnectionsFailed  int64
	PeakActive         int64
	UpBytes            int64
	DownBytes          int64
	UpPackets          int64
	DownPackets        int64
}

type NetworkQuery struct {
	Page     int
	PageSize int
	Query    string
	ClientID string
	Protocol string
	States   []string
	DateFrom int64
	DateTo   int64
	Sort     string
}

type NetworkPage struct {
	Items      []NetworkConnection `json:"items"`
	Page       int                 `json:"page"`
	PageSize   int                 `json:"page_size"`
	Total      int64               `json:"total"`
	TotalPages int                 `json:"total_pages"`
}

type NetworkConnectionDetail struct {
	NetworkConnection
	Samples        []NetworkSample `json:"samples"`
	SamplesExpired bool            `json:"samples_expired"`
}

type NetworkSeriesPoint struct {
	BucketAt           int64 `json:"bucket_at"`
	ConnectionsStarted int64 `json:"connections_started"`
	ConnectionsFailed  int64 `json:"connections_failed"`
	PeakActive         int64 `json:"peak_active"`
	UpBytes            int64 `json:"up_bytes"`
	DownBytes          int64 `json:"down_bytes"`
	UpPackets          int64 `json:"up_packets"`
	DownPackets        int64 `json:"down_packets"`
}

type NetworkHistoricalSummary struct {
	Connections int64                `json:"connections"`
	Failures    int64                `json:"failures"`
	UpBytes     int64                `json:"up_bytes"`
	DownBytes   int64                `json:"down_bytes"`
	Series      []NetworkSeriesPoint `json:"series"`
}

type NetworkClientHistory struct {
	ClientID              string `json:"client_id"`
	ClientRemark          string `json:"client_remark"`
	LastSeenAt            int64  `json:"last_seen_at"`
	HistoricalConnections int64  `json:"historical_connections"`
	Failures              int64  `json:"failures"`
	UpBytes               int64  `json:"up_bytes"`
	DownBytes             int64  `json:"down_bytes"`
}

type NetworkClientQuery struct {
	Page     int
	PageSize int
	Query    string
}

type NetworkClientPage struct {
	Items      []NetworkClientHistory `json:"items"`
	Page       int                    `json:"page"`
	PageSize   int                    `json:"page_size"`
	Total      int64                  `json:"total"`
	TotalPages int                    `json:"total_pages"`
}

var networkStates = map[string]struct{}{
	"accepted": {}, "connecting": {}, "active": {}, "closing": {},
	"closed": {}, "failed": {}, "interrupted": {},
}

func (s *Store) CreateNetworkConnection(ctx context.Context, connection NetworkConnection) error {
	if err := validateNetworkConnection(connection); err != nil {
		return err
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_connections (
		activity_id, protocol, state, client_id, client_remark, proxy_id,
		proxy_remark, external_addr, server_bind, target_addr, transport,
		session_epoch, accepted_at, connected_at, last_activity_at, closed_at,
		up_bytes, down_bytes, up_packets, down_packets, close_reason_code, close_reason
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		connection.ActivityID, connection.Protocol, connection.State, connection.ClientID,
		connection.ClientRemark, connection.ProxyID, connection.ProxyRemark,
		connection.ExternalAddr, connection.ServerBind, connection.TargetAddr,
		connection.Transport, connection.SessionEpoch, connection.AcceptedAt,
		connection.ConnectedAt, connection.LastActivityAt, connection.ClosedAt,
		connection.UpBytes, connection.DownBytes, connection.UpPackets, connection.DownPackets,
		connection.CloseReasonCode, connection.CloseReason,
	)
	if err != nil {
		s.degraded.Store(true)
		return fmt.Errorf("insert network connection: %w", err)
	}
	s.degraded.Store(false)
	return nil
}

func validateNetworkConnection(connection NetworkConnection) error {
	if len(connection.ActivityID) != 32 {
		return errors.New("activity_id must be a 32-character identifier")
	}
	if connection.Protocol != "tcp" && connection.Protocol != "udp" {
		return fmt.Errorf("invalid network protocol %q", connection.Protocol)
	}
	if _, ok := networkStates[connection.State]; !ok {
		return fmt.Errorf("invalid network state %q", connection.State)
	}
	if connection.ClientID == "" || connection.ProxyID == "" {
		return errors.New("network connection client_id and proxy_id are required")
	}
	if connection.AcceptedAt <= 0 || connection.LastActivityAt <= 0 {
		return errors.New("network connection timestamps must be positive")
	}
	return nil
}

func (s *Store) UpdateNetworkConnection(ctx context.Context, connection NetworkConnection) error {
	if err := validateNetworkConnection(connection); err != nil {
		return err
	}
	result, err := s.db.ExecContext(ctx, `UPDATE network_connections SET
		state=?, transport=?, connected_at=?, last_activity_at=?, closed_at=?,
		up_bytes=?, down_bytes=?, up_packets=?, down_packets=?,
		close_reason_code=?, close_reason=?
		WHERE activity_id=?`,
		connection.State, connection.Transport, connection.ConnectedAt,
		connection.LastActivityAt, connection.ClosedAt,
		connection.UpBytes, connection.DownBytes, connection.UpPackets, connection.DownPackets,
		connection.CloseReasonCode, connection.CloseReason, connection.ActivityID,
	)
	if err != nil {
		s.degraded.Store(true)
		return fmt.Errorf("update network connection: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("read network update result: %w", err)
	}
	if affected != 1 {
		return sql.ErrNoRows
	}
	s.degraded.Store(false)
	return nil
}

func (s *Store) UpsertNetworkSample(ctx context.Context, sample NetworkSample) error {
	if len(sample.ActivityID) != 32 || sample.BucketAt <= 0 {
		return errors.New("network sample activity_id and bucket_at are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_samples (
		activity_id, bucket_at, up_bytes_delta, down_bytes_delta,
		up_packets_delta, down_packets_delta
	) VALUES (?, ?, ?, ?, ?, ?)
	ON CONFLICT(activity_id, bucket_at) DO UPDATE SET
		up_bytes_delta=up_bytes_delta+excluded.up_bytes_delta,
		down_bytes_delta=down_bytes_delta+excluded.down_bytes_delta,
		up_packets_delta=up_packets_delta+excluded.up_packets_delta,
		down_packets_delta=down_packets_delta+excluded.down_packets_delta`,
		sample.ActivityID, sample.BucketAt, sample.UpBytesDelta, sample.DownBytesDelta,
		sample.UpPacketsDelta, sample.DownPacketsDelta,
	)
	if err != nil {
		s.degraded.Store(true)
		return fmt.Errorf("upsert network sample: %w", err)
	}
	return nil
}

func (s *Store) UpsertHourlyRollup(ctx context.Context, rollup HourlyRollup) error {
	if rollup.BucketAt <= 0 || (rollup.Protocol != "tcp" && rollup.Protocol != "udp") {
		return errors.New("hourly rollup bucket and protocol are required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO network_hourly_rollups (
		bucket_at, scope_client_id, protocol, connections_started, connections_failed,
		peak_active, up_bytes, down_bytes, up_packets, down_packets
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(bucket_at, scope_client_id, protocol) DO UPDATE SET
		connections_started=connections_started+excluded.connections_started,
		connections_failed=connections_failed+excluded.connections_failed,
		peak_active=MAX(peak_active, excluded.peak_active),
		up_bytes=up_bytes+excluded.up_bytes,
		down_bytes=down_bytes+excluded.down_bytes,
		up_packets=up_packets+excluded.up_packets,
		down_packets=down_packets+excluded.down_packets`,
		rollup.BucketAt, rollup.ScopeClientID, rollup.Protocol,
		rollup.ConnectionsStarted, rollup.ConnectionsFailed, rollup.PeakActive,
		rollup.UpBytes, rollup.DownBytes, rollup.UpPackets, rollup.DownPackets,
	)
	if err != nil {
		s.degraded.Store(true)
		return fmt.Errorf("upsert network hourly rollup: %w", err)
	}
	return nil
}

func (s *Store) InterruptOpenConnections(ctx context.Context, at int64) (int64, error) {
	result, err := s.db.ExecContext(ctx, `UPDATE network_connections SET
		state='interrupted', closed_at=?, last_activity_at=?,
		close_reason_code='server_restart', close_reason='server restarted'
		WHERE state IN ('accepted', 'connecting', 'active', 'closing')`, at, at)
	if err != nil {
		return 0, fmt.Errorf("interrupt unfinished network connections: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, fmt.Errorf("read interrupted connection count: %w", err)
	}
	return affected, nil
}

func (s *Store) ListNetworkConnections(ctx context.Context, query NetworkQuery) (NetworkPage, error) {
	query, err := normalizeNetworkQuery(query)
	if err != nil {
		return NetworkPage{}, err
	}
	where, arguments := networkWhere(query)
	var total int64
	if err := s.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM network_connections`+where, arguments...,
	).Scan(&total); err != nil {
		return NetworkPage{}, fmt.Errorf("count network connections: %w", err)
	}
	order := map[string]string{
		"accepted_desc": "accepted_at DESC, activity_id DESC",
		"accepted_asc":  "accepted_at ASC, activity_id ASC",
		"traffic_desc":  "(up_bytes + down_bytes) DESC, accepted_at DESC",
		"duration_desc": "(COALESCE(closed_at, ?) - accepted_at) DESC, accepted_at DESC",
	}[query.Sort]
	if query.Sort == "duration_desc" {
		arguments = append(arguments, time.Now().UTC().UnixMilli())
	}
	arguments = append(arguments, query.PageSize, (query.Page-1)*query.PageSize)
	//nolint:gosec // The WHERE fragments and ORDER BY value are selected from server-owned allowlists.
	rows, err := s.db.QueryContext(ctx, `SELECT `+networkConnectionColumns+`
		FROM network_connections`+where+` ORDER BY `+order+` LIMIT ? OFFSET ?`, arguments...)
	if err != nil {
		return NetworkPage{}, fmt.Errorf("query network connections: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]NetworkConnection, 0, query.PageSize)
	for rows.Next() {
		connection, err := scanNetworkConnection(rows)
		if err != nil {
			return NetworkPage{}, err
		}
		items = append(items, connection)
	}
	if err := rows.Err(); err != nil {
		return NetworkPage{}, fmt.Errorf("iterate network connections: %w", err)
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(query.PageSize) - 1) / int64(query.PageSize))
	}
	return NetworkPage{
		Items: items, Page: query.Page, PageSize: query.PageSize,
		Total: total, TotalPages: totalPages,
	}, nil
}

const networkConnectionColumns = `activity_id, protocol, state, client_id, client_remark,
	proxy_id, proxy_remark, external_addr, server_bind, target_addr, transport,
	session_epoch, accepted_at, connected_at, last_activity_at, closed_at,
	up_bytes, down_bytes, up_packets, down_packets, close_reason_code, close_reason`

type rowScanner interface {
	Scan(dest ...any) error
}

func scanNetworkConnection(scanner rowScanner) (NetworkConnection, error) {
	var connection NetworkConnection
	var connectedAt, closedAt sql.NullInt64
	if err := scanner.Scan(
		&connection.ActivityID, &connection.Protocol, &connection.State,
		&connection.ClientID, &connection.ClientRemark, &connection.ProxyID,
		&connection.ProxyRemark, &connection.ExternalAddr, &connection.ServerBind,
		&connection.TargetAddr, &connection.Transport, &connection.SessionEpoch,
		&connection.AcceptedAt, &connectedAt, &connection.LastActivityAt, &closedAt,
		&connection.UpBytes, &connection.DownBytes, &connection.UpPackets,
		&connection.DownPackets, &connection.CloseReasonCode, &connection.CloseReason,
	); err != nil {
		return NetworkConnection{}, fmt.Errorf("scan network connection: %w", err)
	}
	if connectedAt.Valid {
		connection.ConnectedAt = &connectedAt.Int64
	}
	if closedAt.Valid {
		connection.ClosedAt = &closedAt.Int64
	}
	return connection, nil
}

func normalizeNetworkQuery(query NetworkQuery) (NetworkQuery, error) {
	if query.Page <= 0 {
		query.Page = 1
	}
	switch query.PageSize {
	case 0:
		query.PageSize = defaultPageSize
	case 25, 50, 100:
	default:
		return NetworkQuery{}, errors.New("page_size must be 25, 50, or 100")
	}
	query.Query = strings.TrimSpace(query.Query)
	if len(query.Query) > maxSearchLength {
		return NetworkQuery{}, fmt.Errorf("query exceeds %d characters", maxSearchLength)
	}
	if query.Protocol != "" && query.Protocol != "tcp" && query.Protocol != "udp" {
		return NetworkQuery{}, fmt.Errorf("invalid protocol %q", query.Protocol)
	}
	for _, state := range query.States {
		if _, ok := networkStates[state]; !ok {
			return NetworkQuery{}, fmt.Errorf("invalid state %q", state)
		}
	}
	if query.DateFrom > 0 && query.DateTo > 0 && query.DateFrom >= query.DateTo {
		return NetworkQuery{}, errors.New("date_from must be before date_to")
	}
	if query.Sort == "" {
		query.Sort = "accepted_desc"
	}
	switch query.Sort {
	case "accepted_desc", "accepted_asc", "traffic_desc", "duration_desc":
	default:
		return NetworkQuery{}, fmt.Errorf("invalid sort %q", query.Sort)
	}
	return query, nil
}

func networkWhere(query NetworkQuery) (string, []any) {
	var clauses []string
	var arguments []any
	if query.Query != "" {
		value := "%" + escapeLike(query.Query) + "%"
		clauses = append(clauses, `(activity_id LIKE ? ESCAPE '\' OR client_id LIKE ? ESCAPE '\'
			OR client_remark LIKE ? ESCAPE '\' OR proxy_id LIKE ? ESCAPE '\'
			OR proxy_remark LIKE ? ESCAPE '\' OR external_addr LIKE ? ESCAPE '\'
			OR target_addr LIKE ? ESCAPE '\')`)
		for range 7 {
			arguments = append(arguments, value)
		}
	}
	if query.ClientID != "" {
		clauses = append(clauses, "client_id=?")
		arguments = append(arguments, query.ClientID)
	}
	if query.Protocol != "" {
		clauses = append(clauses, "protocol=?")
		arguments = append(arguments, query.Protocol)
	}
	if len(query.States) > 0 {
		placeholders := make([]string, len(query.States))
		for index, state := range query.States {
			placeholders[index] = "?"
			arguments = append(arguments, state)
		}
		clauses = append(clauses, "state IN ("+strings.Join(placeholders, ",")+")")
	}
	if query.DateFrom > 0 {
		clauses = append(clauses, "accepted_at>=?")
		arguments = append(arguments, query.DateFrom)
	}
	if query.DateTo > 0 {
		clauses = append(clauses, "accepted_at<?")
		arguments = append(arguments, query.DateTo)
	}
	if len(clauses) == 0 {
		return "", arguments
	}
	return " WHERE " + strings.Join(clauses, " AND "), arguments
}

func (s *Store) NetworkConnectionDetail(
	ctx context.Context,
	id string,
	sampleCutoff int64,
) (NetworkConnectionDetail, error) {
	if len(id) != 32 {
		return NetworkConnectionDetail{}, sql.ErrNoRows
	}
	connection, err := scanNetworkConnection(s.db.QueryRowContext(
		ctx, `SELECT `+networkConnectionColumns+` FROM network_connections WHERE activity_id=?`, id,
	))
	if err != nil {
		return NetworkConnectionDetail{}, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT activity_id, bucket_at,
		up_bytes_delta, down_bytes_delta, up_packets_delta, down_packets_delta
		FROM network_samples WHERE activity_id=? ORDER BY bucket_at`, id)
	if err != nil {
		return NetworkConnectionDetail{}, fmt.Errorf("query network samples: %w", err)
	}
	defer func() { _ = rows.Close() }()
	samples := make([]NetworkSample, 0)
	for rows.Next() {
		var sample NetworkSample
		if err := rows.Scan(
			&sample.ActivityID, &sample.BucketAt, &sample.UpBytesDelta,
			&sample.DownBytesDelta, &sample.UpPacketsDelta, &sample.DownPacketsDelta,
		); err != nil {
			return NetworkConnectionDetail{}, fmt.Errorf("scan network sample: %w", err)
		}
		samples = append(samples, sample)
	}
	if err := rows.Err(); err != nil {
		return NetworkConnectionDetail{}, fmt.Errorf("iterate network samples: %w", err)
	}
	return NetworkConnectionDetail{
		NetworkConnection: connection,
		Samples:           samples,
		SamplesExpired:    len(samples) == 0 && connection.AcceptedAt < sampleCutoff,
	}, nil
}

func (s *Store) NetworkSummary(
	ctx context.Context,
	from int64,
	clientID string,
) (NetworkHistoricalSummary, error) {
	var result NetworkHistoricalSummary
	where := " WHERE accepted_at>=?"
	arguments := []any{from}
	if clientID != "" {
		where += " AND client_id=?"
		arguments = append(arguments, clientID)
	}
	if err := s.db.QueryRowContext(ctx, `SELECT
		COUNT(*),
		COALESCE(SUM(CASE WHEN state='failed' THEN 1 ELSE 0 END), 0),
		COALESCE(SUM(up_bytes), 0), COALESCE(SUM(down_bytes), 0)
		FROM network_connections`+where, arguments...).Scan(
		&result.Connections, &result.Failures, &result.UpBytes, &result.DownBytes,
	); err != nil {
		return NetworkHistoricalSummary{}, fmt.Errorf("query network summary: %w", err)
	}
	scope := clientID
	if time.Now().UTC().UnixMilli()-from <= (24 * time.Hour).Milliseconds() {
		sampleWhere := " WHERE s.bucket_at>=?"
		sampleArguments := []any{from}
		if clientID != "" {
			sampleWhere += " AND c.client_id=?"
			sampleArguments = append(sampleArguments, clientID)
		}
		rows, err := s.db.QueryContext(ctx, `SELECT s.bucket_at,
			0, 0, 0,
			SUM(s.up_bytes_delta), SUM(s.down_bytes_delta),
			SUM(s.up_packets_delta), SUM(s.down_packets_delta)
			FROM network_samples s
			JOIN network_connections c ON c.activity_id=s.activity_id`+sampleWhere+`
			GROUP BY s.bucket_at ORDER BY s.bucket_at`, sampleArguments...)
		if err != nil {
			return NetworkHistoricalSummary{}, fmt.Errorf("query network sample series: %w", err)
		}
		defer func() { _ = rows.Close() }()
		for rows.Next() {
			var point NetworkSeriesPoint
			if err := rows.Scan(
				&point.BucketAt, &point.ConnectionsStarted, &point.ConnectionsFailed,
				&point.PeakActive, &point.UpBytes, &point.DownBytes,
				&point.UpPackets, &point.DownPackets,
			); err != nil {
				return NetworkHistoricalSummary{}, fmt.Errorf("scan network sample series: %w", err)
			}
			result.Series = append(result.Series, point)
		}
		if err := rows.Err(); err != nil {
			return NetworkHistoricalSummary{}, fmt.Errorf("iterate network sample series: %w", err)
		}
		return result, nil
	}
	rows, err := s.db.QueryContext(ctx, `SELECT bucket_at,
		SUM(connections_started), SUM(connections_failed), MAX(peak_active),
		SUM(up_bytes), SUM(down_bytes), SUM(up_packets), SUM(down_packets)
		FROM network_hourly_rollups
		WHERE bucket_at>=? AND scope_client_id=?
		GROUP BY bucket_at ORDER BY bucket_at`, from, scope)
	if err != nil {
		return NetworkHistoricalSummary{}, fmt.Errorf("query network series: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var point NetworkSeriesPoint
		if err := rows.Scan(
			&point.BucketAt, &point.ConnectionsStarted, &point.ConnectionsFailed,
			&point.PeakActive, &point.UpBytes, &point.DownBytes,
			&point.UpPackets, &point.DownPackets,
		); err != nil {
			return NetworkHistoricalSummary{}, fmt.Errorf("scan network series: %w", err)
		}
		result.Series = append(result.Series, point)
	}
	if err := rows.Err(); err != nil {
		return NetworkHistoricalSummary{}, fmt.Errorf("iterate network series: %w", err)
	}
	return result, nil
}

func (s *Store) ListNetworkClients(
	ctx context.Context,
	query NetworkClientQuery,
) (NetworkClientPage, error) {
	if query.Page <= 0 {
		query.Page = 1
	}
	switch query.PageSize {
	case 0:
		query.PageSize = 50
	case 25, 50, 100:
	default:
		return NetworkClientPage{}, errors.New("page_size must be 25, 50, or 100")
	}
	query.Query = strings.TrimSpace(query.Query)
	if len(query.Query) > maxSearchLength {
		return NetworkClientPage{}, fmt.Errorf("query exceeds %d characters", maxSearchLength)
	}
	where := ""
	arguments := []any{}
	if query.Query != "" {
		value := "%" + escapeLike(query.Query) + "%"
		where = ` WHERE client_id LIKE ? ESCAPE '\' OR client_remark LIKE ? ESCAPE '\'`
		arguments = append(arguments, value, value)
	}
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT client_id FROM network_connections`+where+` GROUP BY client_id
	)`, arguments...).Scan(&total); err != nil {
		return NetworkClientPage{}, fmt.Errorf("count network clients: %w", err)
	}
	arguments = append(arguments, query.PageSize, (query.Page-1)*query.PageSize)
	rows, err := s.db.QueryContext(ctx, `SELECT
		client_id,
		SUBSTR(MAX(printf('%020d', accepted_at) || client_remark), 21),
		MAX(last_activity_at),
		COUNT(*),
		SUM(CASE WHEN state='failed' THEN 1 ELSE 0 END),
		SUM(up_bytes), SUM(down_bytes)
		FROM network_connections`+where+`
		GROUP BY client_id ORDER BY MAX(last_activity_at) DESC
		LIMIT ? OFFSET ?`, arguments...)
	if err != nil {
		return NetworkClientPage{}, fmt.Errorf("query network clients: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]NetworkClientHistory, 0, query.PageSize)
	for rows.Next() {
		var item NetworkClientHistory
		if err := rows.Scan(
			&item.ClientID, &item.ClientRemark, &item.LastSeenAt,
			&item.HistoricalConnections, &item.Failures, &item.UpBytes, &item.DownBytes,
		); err != nil {
			return NetworkClientPage{}, fmt.Errorf("scan network client: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return NetworkClientPage{}, fmt.Errorf("iterate network clients: %w", err)
	}
	totalPages := 0
	if total > 0 {
		totalPages = int((total + int64(query.PageSize) - 1) / int64(query.PageSize))
	}
	return NetworkClientPage{
		Items: items, Page: query.Page, PageSize: query.PageSize,
		Total: total, TotalPages: totalPages,
	}, nil
}

func (s *Store) Cleanup(
	ctx context.Context,
	now int64,
	auditRetention, networkRetention, sampleRetention time.Duration,
) error {
	sampleBefore := now - sampleRetention.Milliseconds()
	networkBefore := now - networkRetention.Milliseconds()
	auditBefore := now - auditRetention.Milliseconds()
	for _, statement := range []struct {
		query string
		value int64
	}{
		{`DELETE FROM network_samples WHERE rowid IN (
			SELECT rowid FROM network_samples WHERE bucket_at < ? LIMIT 1000
		)`, sampleBefore},
		{`DELETE FROM network_hourly_rollups WHERE rowid IN (
			SELECT rowid FROM network_hourly_rollups WHERE bucket_at < ? LIMIT 1000
		)`, networkBefore},
		{`DELETE FROM network_connections WHERE rowid IN (
			SELECT rowid FROM network_connections
			WHERE accepted_at < ? AND state IN ('closed', 'failed', 'interrupted') LIMIT 1000
		)`, networkBefore},
		{`DELETE FROM audit_events WHERE rowid IN (
			SELECT rowid FROM audit_events WHERE occurred_at < ? LIMIT 1000
		)`, auditBefore},
	} {
		for batch := 0; batch < 10; batch++ {
			result, err := s.db.ExecContext(ctx, statement.query, statement.value)
			if err != nil {
				s.degraded.Store(true)
				return fmt.Errorf("clean retained data: %w", err)
			}
			affected, err := result.RowsAffected()
			if err != nil {
				return fmt.Errorf("read retained cleanup result: %w", err)
			}
			if affected < 1000 {
				break
			}
			runtime.Gosched()
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("clean retained data canceled: %w", err)
			}
		}
	}
	s.degraded.Store(false)
	return nil
}

func (s *Store) SchemaVersion(ctx context.Context) (int, error) {
	var version int
	if err := s.db.QueryRowContext(ctx, `SELECT COALESCE(MAX(version), 0) FROM schema_migrations`).Scan(&version); err != nil {
		return 0, fmt.Errorf("read schema version: %w", err)
	}
	return version, nil
}
