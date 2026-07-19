package store

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func BenchmarkNetworkAnalyticsReferenceScale(b *testing.B) {
	database, err := Open(":memory:", nil)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = database.Close() })

	now := time.Now().UTC().Truncate(time.Hour).UnixMilli()
	const (
		rollupRows        = 100_000
		connectionsPerRow = 10
	)
	ctx := context.Background()
	tx, err := database.db.BeginTx(ctx, nil)
	if err != nil {
		b.Fatal(err)
	}
	statement, err := tx.PrepareContext(ctx, `INSERT INTO network_history_ip_rollups (
		resolution_seconds, bucket_at, client_id, proxy_id, protocol, source_ip,
		geo_kind, country_code, geo_database_fingerprint,
		connections, failures, up_bytes, down_bytes,
		first_seen_at, last_seen_at
	) VALUES (?, ?, ?, ?, ?, ?, 'country', ?, 'benchmark',
		?, ?, ?, ?, ?, ?)`)
	if err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	defer func() { _ = statement.Close() }()
	for index := range rollupRows {
		bucket := now - int64(index%2160)*time.Hour.Milliseconds()
		clientID := fmt.Sprintf("client-%03d", index%100)
		proxyID := fmt.Sprintf("proxy-%02d", (index/100)%20)
		protocol := "tcp"
		if index%2 == 1 {
			protocol = "udp"
		}
		sourceIP := fmt.Sprintf(
			"10.%d.%d.%d",
			(index>>16)&255,
			(index>>8)&255,
			index&255,
		)
		country := "CN"
		if index%3 == 0 {
			country = "US"
		}
		if _, err := statement.ExecContext(
			ctx,
			AnalyticsHourlyResolutionSeconds,
			bucket,
			clientID,
			proxyID,
			protocol,
			sourceIP,
			country,
			connectionsPerRow,
			index%10,
			1024+index%4096,
			2048+index%8192,
			bucket,
			bucket+time.Hour.Milliseconds()-1,
		); err != nil {
			_ = tx.Rollback()
			b.Fatal(err)
		}
	}
	if err := statement.Close(); err != nil {
		_ = tx.Rollback()
		b.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		b.Fatal(err)
	}
	if _, err := database.db.ExecContext(ctx, "ANALYZE"); err != nil {
		b.Fatal(err)
	}

	query := AnalyticsQuery{
		From:             now - 90*24*time.Hour.Milliseconds(),
		To:               now + time.Hour.Milliseconds(),
		BaseResolution:   AnalyticsHourlyResolutionSeconds,
		OutputResolution: 24 * AnalyticsHourlyResolutionSeconds,
	}
	b.ReportMetric(rollupRows*connectionsPerRow, "logical_connections")
	b.ReportMetric(rollupRows, "rollup_rows")
	b.ResetTimer()

	b.Run("overview", func(b *testing.B) {
		for range b.N {
			if _, err := database.NetworkAnalyticsOverview(context.Background(), query); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("dimensions", func(b *testing.B) {
		dimensionQuery := query
		dimensionQuery.Page = 1
		dimensionQuery.PageSize = 25
		dimensionQuery.Sort = "traffic_desc"
		for range b.N {
			if _, err := database.NetworkAnalyticsDimensions(
				context.Background(),
				dimensionQuery,
			); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("top_ips", func(b *testing.B) {
		for range b.N {
			if _, err := database.NetworkAnalyticsTopIPs(
				context.Background(),
				query,
				20,
			); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("ip_series", func(b *testing.B) {
		for range b.N {
			if _, err := database.NetworkAnalyticsIPSeries(
				context.Background(),
				query,
				"10.0.0.0",
			); err != nil {
				b.Fatal(err)
			}
		}
	})
}
