package store

import (
	"context"
	"fmt"
)

type ProxyHistoryRef struct {
	ClientID string
	ProxyID  string
}

type ProxyHistory struct {
	Connections           int64
	Failures              int64
	UpBytes               int64
	DownBytes             int64
	LastActivityAt        int64
	LastFailureAt         int64
	LastFailureReasonCode string
}

func (s *Store) ProxyHistory24h(
	ctx context.Context,
	from int64,
	to int64,
) (map[ProxyHistoryRef]ProxyHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT client_id, proxy_id,
			COALESCE(SUM(connections), 0),
			COALESCE(SUM(failures), 0),
			COALESCE(SUM(up_bytes), 0),
			COALESCE(SUM(down_bytes), 0),
			COALESCE(MAX(last_seen_at), 0)
		FROM network_history_ip_rollups
		WHERE resolution_seconds=? AND bucket_at>=? AND bucket_at<?
		GROUP BY client_id, proxy_id
	`, AnalyticsFineResolutionSeconds, from, to)
	if err != nil {
		return nil, fmt.Errorf("query proxy history observations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[ProxyHistoryRef]ProxyHistory)
	for rows.Next() {
		var key ProxyHistoryRef
		var value ProxyHistory
		if err := rows.Scan(
			&key.ClientID, &key.ProxyID, &value.Connections, &value.Failures,
			&value.UpBytes, &value.DownBytes, &value.LastActivityAt,
		); err != nil {
			return nil, fmt.Errorf("scan proxy history observation: %w", err)
		}
		result[key] = value
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate proxy history observations: %w", err)
	}

	failureRows, err := s.db.QueryContext(ctx, `
		WITH ranked AS (
			SELECT client_id, proxy_id, closed_at, close_reason_code,
				ROW_NUMBER() OVER (
					PARTITION BY client_id, proxy_id
					ORDER BY closed_at DESC, activity_id DESC
				) AS position
			FROM network_connections
			WHERE state='failed' AND closed_at>=? AND closed_at<?
		)
		SELECT client_id, proxy_id, closed_at, close_reason_code
		FROM ranked WHERE position=1
	`, from, to)
	if err != nil {
		return nil, fmt.Errorf("query recent proxy failures: %w", err)
	}
	defer func() { _ = failureRows.Close() }()
	for failureRows.Next() {
		var key ProxyHistoryRef
		var at int64
		var code string
		if err := failureRows.Scan(&key.ClientID, &key.ProxyID, &at, &code); err != nil {
			return nil, fmt.Errorf("scan recent proxy failure: %w", err)
		}
		value := result[key]
		value.LastFailureAt = at
		value.LastFailureReasonCode = code
		result[key] = value
	}
	if err := failureRows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent proxy failures: %w", err)
	}
	return result, nil
}
