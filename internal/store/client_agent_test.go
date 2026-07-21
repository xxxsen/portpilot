package store

import (
	"path/filepath"
	"testing"

	"go.uber.org/zap"
)

func TestClientAgentSnapshotRoundTrip(t *testing.T) {
	t.Parallel()
	database, err := Open(filepath.Join(t.TempDir(), "events.db"), zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	snapshot := ClientAgentSnapshot{
		ClientID: "client", AgentStatus: "available",
		AgentVersion: "v1", ProtocolVersion: 1,
		CapabilitiesJSON: `["tcp-smux"]`, SourceAddr: "192.0.2.10",
		SourceIP: "192.0.2.10", FirstSeenAt: 1000, LastSeenAt: 2000,
		LastOnlineAt: 1000, LastSessionEpoch: 3,
	}
	if err := database.UpsertClientAgentSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	offlineAt := int64(3000)
	snapshot.LastSeenAt = 3000
	snapshot.LastOfflineAt = &offlineAt
	snapshot.LastOfflineReason = "remote_closed"
	if err := database.UpsertClientAgentSnapshot(t.Context(), snapshot); err != nil {
		t.Fatal(err)
	}
	result, err := database.ClientAgentSnapshot(t.Context(), "client")
	if err != nil {
		t.Fatal(err)
	}
	if result.AgentStatus != "available" || result.AgentVersion != "v1" ||
		len(result.Capabilities) != 1 ||
		result.LastOfflineAt == nil || *result.LastOfflineAt != offlineAt ||
		result.LastOfflineReason != "remote_closed" {
		t.Fatalf("unexpected snapshot: %#v", result)
	}
}
