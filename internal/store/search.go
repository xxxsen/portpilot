package store

import (
	"context"
	"database/sql"
	"fmt"
)

type SourceIPSearchResult struct {
	SourceIP       string
	LastActivityAt int64
	Connections    int64
	GeoKind        string
	CountryCode    string
}

func (s *Store) SearchSourceIP(
	ctx context.Context,
	sourceIP string,
) (SourceIPSearchResult, error) {
	var result SourceIPSearchResult
	result.SourceIP = sourceIP
	err := s.db.QueryRowContext(ctx, `
		SELECT COALESCE(MAX(last_activity_at), 0), COUNT(*)
		FROM network_connections
		WHERE external_ip=?
	`, sourceIP).Scan(&result.LastActivityAt, &result.Connections)
	if err != nil {
		return SourceIPSearchResult{}, fmt.Errorf("search source IP: %w", err)
	}
	if result.Connections == 0 {
		return SourceIPSearchResult{}, sql.ErrNoRows
	}
	_ = s.db.QueryRowContext(ctx, `
		SELECT geo_kind, country_code
		FROM network_history_ip_rollups
		WHERE source_ip=?
		ORDER BY bucket_at DESC
		LIMIT 1
	`, sourceIP).Scan(&result.GeoKind, &result.CountryCode)
	return result, nil
}

func (s *Store) SearchActivityPrefix(
	ctx context.Context,
	prefix string,
	limit int,
) ([]NetworkConnection, error) {
	upper := prefix + "g"
	rows, err := s.db.QueryContext(ctx, `
		SELECT `+networkConnectionColumns+`
		FROM network_connections
		WHERE activity_id>=? AND activity_id<?
		ORDER BY activity_id
		LIMIT ?
	`, prefix, upper, limit)
	if err != nil {
		return nil, fmt.Errorf("search activity prefix: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]NetworkConnection, 0, limit)
	for rows.Next() {
		value, err := scanNetworkConnection(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, value)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate activity prefix search: %w", err)
	}
	return result, nil
}
