package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"testing"
	"time"
)

func TestAuditRoundTripFilteringAndPendingRecovery(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	event, err := database.WriteAudit(t.Context(), AuditEvent{
		Category: "config", EventType: "config_update", Outcome: "pending",
		ActorType: "admin", ActorID: "operator", ClientID: "branch-1",
		Summary: "更新配置",
		Details: map[string]any{"old_revision": 4, "target_revision": 5},
		ConfigChanges: []ConfigChange{{
			EntityType: "client", EntityID: "branch-1", Operation: "update",
			Before: map[string]any{"remark": "old", "token_configured": true},
			After:  map[string]any{"remark": "new", "token_configured": true},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := database.ResolvePendingAudits(context.Background(), 5); err != nil {
		t.Fatal(err)
	}
	detail, err := database.AuditDetail(t.Context(), event.ID)
	if err != nil {
		t.Fatal(err)
	}
	if detail.Outcome != "success" || detail.ReasonCode != "recovered_after_restart" {
		t.Fatalf("pending event was not recovered: %#v", detail)
	}
	if len(detail.ConfigChanges) != 1 ||
		detail.ConfigChanges[0].After["remark"] != "new" {
		t.Fatalf("config changes were not preserved: %#v", detail.ConfigChanges)
	}
	page, err := database.ListAudit(t.Context(), AuditQuery{
		Page: 1, PageSize: 25, Query: "branch-1", Category: "config",
		EventType: "config_update", Outcome: "success",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || len(page.Items) != 1 || page.Items[0].ID != event.ID {
		t.Fatalf("unexpected audit page: %#v", page)
	}
}

func TestClientAgentAuditEventIsAccepted(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	event := AuditEvent{
		Category: "security", EventType: "client_agent_invalid", Outcome: "denied",
		ActorType: "client", Summary: "invalid client agent",
	}
	if _, err := database.WriteAudit(t.Context(), event); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListAudit(t.Context(), AuditQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 {
		t.Fatalf("audit total = %d, want 1", page.Total)
	}
}

func TestRuntimeLogEventsAreRejectedByAuditStore(t *testing.T) {
	t.Parallel()
	for _, eventType := range []string{
		"external_access_denied", "udp_drop", "proxy_protocol_write_failed", "security",
	} {
		_, _, err := normalizeAuditEvent(AuditEvent{
			Category: "system", EventType: eventType, Outcome: "failure",
			ActorType: "system", Summary: "runtime log event",
		})
		if err == nil || !strings.Contains(err.Error(), "invalid audit event_type") {
			t.Fatalf("runtime event %q was accepted: %v", eventType, err)
		}
	}
}

func TestLegacyRuntimeLogEventsAreHiddenFromAuditQueries(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	const eventID = "0123456789abcdef0123456789abcdef"
	if _, err := database.db.ExecContext(t.Context(), `INSERT INTO audit_events (
		id, occurred_at, category, event_type, outcome, actor_type
	) VALUES (?, ?, ?, ?, ?, ?)`,
		eventID, time.Now().UnixMilli(), "security", "external_access_denied", "denied", "external",
	); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListAudit(t.Context(), AuditQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 0 || len(page.Items) != 0 {
		t.Fatalf("legacy runtime event leaked into audit list: %#v", page.Items)
	}
	if _, err := database.AuditDetail(t.Context(), eventID); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("legacy runtime event detail remained visible: %v", err)
	}
}

func TestNetworkQueriesAndCleanup(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	connection := NetworkConnection{
		ActivityID: "0123456789abcdef0123456789abcdef",
		Protocol:   "udp", State: "active", ClientID: "client-a", ClientRemark: "A",
		ProxyID: "dns", ProxyRemark: "DNS", ExternalAddr: "192.0.2.1:1234",
		ServerBind: ":7001", TargetAddr: "10.0.0.53:53", Transport: "smux",
		SessionEpoch: 3, AcceptedAt: 1000, LastActivityAt: 1000,
	}
	if err := database.CreateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	connection.State = "closed"
	connection.UpBytes = 42
	connection.DownBytes = 84
	connection.UpPackets = 1
	connection.DownPackets = 2
	closedAt := int64(2000)
	connection.ClosedAt = &closedAt
	connection.LastActivityAt = closedAt
	if err := database.UpdateNetworkConnection(t.Context(), connection); err != nil {
		t.Fatal(err)
	}
	emptyDetail, err := database.NetworkConnectionDetail(
		t.Context(), connection.ActivityID, 0,
	)
	if err != nil {
		t.Fatal(err)
	}
	if emptyDetail.Samples == nil || len(emptyDetail.Samples) != 0 {
		t.Fatalf("empty network samples must encode as an empty array: %#v", emptyDetail.Samples)
	}
	if err := database.UpsertNetworkSample(t.Context(), NetworkSample{
		ActivityID: connection.ActivityID, BucketAt: 1000,
		UpBytesDelta: 42, DownBytesDelta: 84, UpPacketsDelta: 1, DownPacketsDelta: 2,
	}); err != nil {
		t.Fatal(err)
	}
	page, err := database.ListNetworkConnections(t.Context(), NetworkQuery{
		Page: 1, PageSize: 25, ClientID: "client-a", Protocol: "udp",
		States: []string{"closed"}, Sort: "traffic_desc",
	})
	if err != nil {
		t.Fatal(err)
	}
	if page.Total != 1 || page.Items[0].Transport != "smux" || page.Items[0].DownBytes != 84 {
		t.Fatalf("unexpected network page: %#v", page)
	}
	detail, err := database.NetworkConnectionDetail(t.Context(), connection.ActivityID, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(detail.Samples) != 1 || detail.Samples[0].DownPacketsDelta != 2 {
		t.Fatalf("unexpected network detail: %#v", detail)
	}
	liveConnection := connection
	liveConnection.ActivityID = "fedcba9876543210fedcba9876543210"
	liveConnection.State = "active"
	liveConnection.ClosedAt = nil
	liveConnection.UpBytes = 0
	liveConnection.DownBytes = 0
	if err := database.CreateNetworkConnection(t.Context(), liveConnection); err != nil {
		t.Fatal(err)
	}
	clients, err := database.ListNetworkClients(t.Context(), NetworkClientQuery{
		Page: 1, PageSize: 25,
	})
	if err != nil {
		t.Fatal(err)
	}
	if clients.Total != 1 || len(clients.Items) != 1 ||
		clients.Items[0].HistoricalConnections != 1 {
		t.Fatalf("active connection leaked into client history: %#v", clients)
	}
}

func TestCoreQueriesUseBoundedIndexes(t *testing.T) {
	t.Parallel()
	database, err := Open(":memory:", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })

	tests := []struct {
		name  string
		query string
		args  []any
		index string
	}{
		{
			name: "network client history",
			query: `EXPLAIN QUERY PLAN SELECT activity_id FROM network_connections
				WHERE client_id=? ORDER BY accepted_at DESC LIMIT 25`,
			args: []any{"client-a"}, index: "idx_network_connections_client",
		},
		{
			name: "audit category outcome",
			query: `EXPLAIN QUERY PLAN SELECT id FROM audit_events
				WHERE category=? AND outcome=? ORDER BY occurred_at DESC LIMIT 25`,
			args: []any{"config", "success"}, index: "idx_audit_events_category_outcome",
		},
		{
			name: "sample retention",
			query: `EXPLAIN QUERY PLAN SELECT rowid FROM network_samples
				WHERE bucket_at < ? LIMIT 1000`,
			args: []any{time.Now().UTC().UnixMilli()}, index: "idx_network_samples_bucket",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rows, err := database.db.QueryContext(t.Context(), test.query, test.args...)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = rows.Close() }()
			var plan strings.Builder
			for rows.Next() {
				var id, parent, unused int
				var detail string
				if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
					t.Fatal(err)
				}
				plan.WriteString(detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(plan.String(), test.index) {
				t.Fatalf("query plan %q does not use %s", plan.String(), test.index)
			}
		})
	}
}
