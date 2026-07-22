package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

var ErrExportTooManyRows = errors.New("export matched too many rows")

func (s *Store) ExportNetworkConnections(
	ctx context.Context,
	query NetworkQuery,
	maxRows int,
	yield func(NetworkConnection) error,
) (int64, error) {
	query, err := NormalizeNetworkQuery(query)
	if err != nil {
		return 0, err
	}
	where, arguments := networkWhere(query)
	var total int64
	if err := s.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM network_connections`+where, arguments...,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("count network connection export: %w", err)
	}
	if total > int64(maxRows) {
		return total, ErrExportTooManyRows
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
	//nolint:gosec // WHERE and ORDER BY are produced by normalized fixed allowlists.
	rows, err := s.db.QueryContext(ctx, `SELECT `+networkConnectionColumns+`
		FROM network_connections`+where+` ORDER BY `+order, arguments...)
	if err != nil {
		return total, fmt.Errorf("query network connection export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		value, err := scanNetworkConnection(rows)
		if err != nil {
			return total, fmt.Errorf("scan export row: %w", err)
		}
		if err := yield(value); err != nil {
			return total, fmt.Errorf("scan export row: %w", err)
		}
	}
	if err := rows.Err(); err != nil {
		return total, fmt.Errorf("iterate network connection export: %w", err)
	}
	return total, nil
}

func (s *Store) ExportAuditEvents(
	ctx context.Context,
	query AuditQuery,
	maxRows int,
	yield func(AuditEvent) error,
) (int64, error) {
	query, err := normalizeAuditQuery(query)
	if err != nil {
		return 0, err
	}
	where, arguments := auditWhere(query)
	var total int64
	if err := s.db.QueryRowContext(
		ctx, `SELECT COUNT(*) FROM audit_events`+where, arguments...,
	).Scan(&total); err != nil {
		return 0, fmt.Errorf("count audit export: %w", err)
	}
	if total > int64(maxRows) {
		return total, ErrExportTooManyRows
	}
	//nolint:gosec // WHERE is produced by the normalized audit query builder.
	//nolint:gosec // WHERE and ORDER BY are produced by normalized analytics allowlists.
	rows, err := s.db.QueryContext(ctx, `SELECT
		id, occurred_at, category, event_type, outcome, actor_type, actor_id,
		source_addr, client_id, proxy_id, connection_id, summary, reason_code,
		reason, details_json
		FROM audit_events`+where+`
		ORDER BY occurred_at DESC, id DESC`, arguments...)
	if err != nil {
		return total, fmt.Errorf("query audit export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	events := make([]AuditEvent, 0, total)
	for rows.Next() {
		var event AuditEvent
		var details string
		if err := rows.Scan(
			&event.ID, &event.OccurredAt, &event.Category, &event.EventType,
			&event.Outcome, &event.ActorType, &event.ActorID, &event.SourceAddr,
			&event.ClientID, &event.ProxyID, &event.ConnectionID, &event.Summary,
			&event.ReasonCode, &event.Reason, &details,
		); err != nil {
			_ = rows.Close()
			return total, fmt.Errorf("scan audit export: %w", err)
		}
		if err := json.Unmarshal([]byte(details), &event.Details); err != nil {
			_ = rows.Close()
			return total, fmt.Errorf("decode audit export details: %w", err)
		}
		events = append(events, event)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return total, fmt.Errorf("iterate audit export: %w", err)
	}
	if err := rows.Close(); err != nil {
		return total, fmt.Errorf("scan export row: %w", err)
	}
	changeWhere := ` WHERE event_id IN (SELECT id FROM audit_events` + where + `)`
	//nolint:gosec // The subquery WHERE is produced by the normalized audit query builder.
	changeRows, err := s.db.QueryContext(ctx, `SELECT
		id, event_id, entity_type, entity_id, operation, before_json, after_json
		FROM audit_config_changes`+changeWhere+` ORDER BY event_id, id`, arguments...)
	if err != nil {
		return total, fmt.Errorf("query audit export changes: %w", err)
	}
	defer func() { _ = changeRows.Close() }()
	changes := make(map[string][]ConfigChange)
	for changeRows.Next() {
		var value ConfigChange
		var before, after sql.NullString
		if err := changeRows.Scan(
			&value.ID, &value.EventID, &value.EntityType, &value.EntityID,
			&value.Operation, &before, &after,
		); err != nil {
			_ = changeRows.Close()
			return total, fmt.Errorf("scan export row: %w", err)
		}
		if before.Valid {
			if err := json.Unmarshal([]byte(before.String), &value.Before); err != nil {
				_ = changeRows.Close()
				return total, fmt.Errorf("scan export row: %w", err)
			}
		}
		if after.Valid {
			if err := json.Unmarshal([]byte(after.String), &value.After); err != nil {
				_ = changeRows.Close()
				return total, fmt.Errorf("scan export row: %w", err)
			}
		}
		changes[value.EventID] = append(changes[value.EventID], value)
	}
	if err := changeRows.Err(); err != nil {
		_ = changeRows.Close()
		return total, fmt.Errorf("scan export row: %w", err)
	}
	if err := changeRows.Close(); err != nil {
		return total, fmt.Errorf("scan export row: %w", err)
	}
	for _, event := range events {
		event.ConfigChanges = changes[event.ID]
		if err := yield(event); err != nil {
			return total, fmt.Errorf("scan export row: %w", err)
		}
	}
	return total, nil
}

func (s *Store) ExportNetworkDimensions(
	ctx context.Context,
	query AnalyticsQuery,
	maxRows int,
	yield func(AnalyticsDimension) error,
) (int64, error) {
	query, err := normalizeAnalyticsQuery(query)
	if err != nil {
		return 0, err
	}
	where, arguments := analyticsWhere(query)
	where, arguments = analyticsDimensionSearch(where, arguments, query.Search)
	var total int64
	if err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM (
		SELECT r.client_id, r.proxy_id, r.protocol
		FROM network_history_ip_rollups r`+where+`
		GROUP BY r.client_id, r.proxy_id, r.protocol
	)`, arguments...).Scan(&total); err != nil {
		return 0, fmt.Errorf("count network dimension export: %w", err)
	}
	if total > int64(maxRows) {
		return total, ErrExportTooManyRows
	}
	order := map[string]string{
		"traffic_desc":     "traffic_bytes DESC, last_seen_at DESC",
		"connections_desc": "connections DESC, traffic_bytes DESC",
		"unique_ips_desc":  "unique_ips DESC, traffic_bytes DESC",
		"failures_desc":    "failures DESC, connections DESC",
	}[query.Sort]
	//nolint:gosec // WHERE and ORDER BY are produced by normalized fixed allowlists.
	//nolint:gosec // WHERE is produced by the normalized audit query builder.
	//nolint:gosec // WHERE and ORDER BY are produced by normalized analytics allowlists.
	rows, err := s.db.QueryContext(ctx, `SELECT
			r.client_id, COALESCE(d.client_remark, ''),
			r.proxy_id, COALESCE(d.proxy_remark, ''), r.protocol,
			COALESCE(SUM(r.connections), 0),
			COALESCE(SUM(r.failures), 0),
			COUNT(DISTINCT r.source_ip),
			COUNT(DISTINCT CASE WHEN r.geo_kind='country' THEN r.country_code END),
			COALESCE(SUM(r.up_bytes), 0),
			COALESCE(SUM(r.down_bytes), 0),
			COALESCE(SUM(r.up_bytes+r.down_bytes), 0) AS traffic_bytes,
			MIN(r.first_seen_at), MAX(r.last_seen_at) AS last_seen_at
		FROM network_history_ip_rollups r
		LEFT JOIN network_history_proxy_dimensions d
		  ON d.client_id=r.client_id AND d.proxy_id=r.proxy_id`+where+`
		GROUP BY r.client_id, r.proxy_id, r.protocol
		ORDER BY `+order, arguments...)
	if err != nil {
		return total, fmt.Errorf("query network dimension export: %w", err)
	}
	defer func() { _ = rows.Close() }()
	items := make([]AnalyticsDimension, 0, total)
	for rows.Next() {
		var item AnalyticsDimension
		var traffic int64
		if err := rows.Scan(
			&item.ClientID, &item.ClientRemark, &item.ProxyID, &item.ProxyRemark,
			&item.Protocol, &item.Connections, &item.Failures,
			&item.UniqueIPs, &item.Countries, &item.UpBytes, &item.DownBytes,
			&traffic, &item.FirstSeenAt, &item.LastSeenAt,
		); err != nil {
			_ = rows.Close()
			return total, fmt.Errorf("scan export row: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return total, fmt.Errorf("scan export row: %w", err)
	}
	if err := rows.Close(); err != nil {
		return total, fmt.Errorf("scan export row: %w", err)
	}
	if err := s.populateAnalyticsTopCountries(ctx, query, items); err != nil {
		return total, fmt.Errorf("scan export row: %w", err)
	}
	for _, item := range items {
		if err := yield(item); err != nil {
			return total, fmt.Errorf("scan export row: %w", err)
		}
	}
	return total, nil
}
