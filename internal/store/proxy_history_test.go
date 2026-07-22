package store

import (
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestProxyHistory24hAggregatesAndKeepsLatestFailure(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "store.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	from := int64(1_000_000)
	to := from + 24*60*60*1000
	for _, values := range [][]any{
		{from, "client-a", "proxy", "198.51.100.1", int64(2), int64(1), int64(100), int64(200), from + 100, from + 100},
		{from + 300_000, "client-a", "proxy", "198.51.100.2", int64(3), int64(1), int64(50), int64(75), from + 400_000, from + 400_000},
		{from, "client-b", "proxy", "203.0.113.1", int64(1), int64(0), int64(7), int64(9), from + 200, from + 200},
	} {
		_, err := database.db.ExecContext(t.Context(), `
			INSERT INTO network_history_ip_rollups (
				resolution_seconds, bucket_at, client_id, proxy_id, protocol,
				source_ip, geo_kind, country_code, geo_database_fingerprint,
				connections, failures, up_bytes, down_bytes, up_packets, down_packets,
				estimated_up_bytes, estimated_down_bytes, first_seen_at, last_seen_at
			) VALUES (300, ?, ?, ?, 'tcp', ?, 'unknown', '', '', ?, ?, ?, ?, 0, 0, 0, 0, ?, ?)
		`, values...)
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, values := range [][]any{
		{"old", "client-a", "proxy", from + 200, "old_reason"},
		{"new", "client-a", "proxy", from + 500, "new_reason"},
	} {
		_, err := database.db.ExecContext(t.Context(), `
			INSERT INTO network_connections (
				activity_id, protocol, state, client_id, proxy_id,
				accepted_at, last_activity_at, closed_at, close_reason_code
			) VALUES (?, 'tcp', 'failed', ?, ?, ?, ?, ?, ?)
		`, values[0], values[1], values[2], values[3], values[3], values[3], values[4])
		if err != nil {
			t.Fatal(err)
		}
	}
	values, err := database.ProxyHistory24h(t.Context(), from, to)
	if err != nil {
		t.Fatal(err)
	}
	first := values[ProxyHistoryRef{ClientID: "client-a", ProxyID: "proxy"}]
	if first.Connections != 5 || first.Failures != 2 || first.UpBytes != 150 ||
		first.DownBytes != 275 || first.LastFailureReasonCode != "new_reason" {
		t.Fatalf("first=%#v", first)
	}
	second := values[ProxyHistoryRef{ClientID: "client-b", ProxyID: "proxy"}]
	if second.Connections != 1 || second.UpBytes != 7 {
		t.Fatalf("second=%#v", second)
	}
}
