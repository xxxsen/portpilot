package store

import (
	"path/filepath"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestSearchSourceIPAndActivityPrefix(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "store.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	for _, id := range []string{
		"abcdef12000000000000000000000000",
		"abcdef12ffffffffffffffffffffffff",
		"bbbbbbbb000000000000000000000000",
	} {
		if err := database.CreateNetworkConnection(t.Context(), NetworkConnection{
			ActivityID: id, Protocol: "tcp", State: "closed",
			ClientID: "client", ProxyID: "proxy", ExternalIP: "8.8.8.8",
			AcceptedAt: 100, LastActivityAt: 200,
		}); err != nil {
			t.Fatal(err)
		}
	}
	source, err := database.SearchSourceIP(t.Context(), "8.8.8.8")
	if err != nil {
		t.Fatal(err)
	}
	if source.Connections != 3 || source.LastActivityAt != 200 {
		t.Fatalf("source=%#v", source)
	}
	activities, err := database.SearchActivityPrefix(t.Context(), "abcdef12", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(activities) != 2 || !strings.HasPrefix(activities[0].ActivityID, "abcdef12") {
		t.Fatalf("activities=%#v", activities)
	}
}

func TestSourceIPSearchUsesOperationalIndex(t *testing.T) {
	database, err := Open(filepath.Join(t.TempDir(), "store.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	rows, err := database.db.QueryContext(t.Context(), `
		EXPLAIN QUERY PLAN
		SELECT MAX(last_activity_at), COUNT(*)
		FROM network_connections
		WHERE external_ip='8.8.8.8'
	`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	defer func() {
		if err := rows.Err(); err != nil {
			t.Errorf("iterate query plan: %v", err)
		}
	}()
	plan := ""
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan += detail
	}
	if !strings.Contains(plan, "idx_network_connections_external_ip_accepted") {
		t.Fatalf("query plan does not use source IP index: %s", plan)
	}
}
