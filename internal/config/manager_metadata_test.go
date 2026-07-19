package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEnsureMetadataAndServerOwnedTimestamps(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "clients.json")
	const legacy = `{
  "revision": 7,
  "client_config": [{
    "client_id": "branch-1",
    "client_token": "` + testToken + `",
    "enabled": true,
    "proxy_list": []
  }]
}
`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	knownTime := time.Date(2026, time.July, 19, 10, 0, 0, 0, time.UTC)
	if err := os.Chtimes(path, knownTime, knownTime); err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(path, "", "")
	if err != nil {
		t.Fatal(err)
	}
	migration, err := manager.EnsureMetadata()
	if err != nil {
		t.Fatal(err)
	}
	if migration.Revision != 8 || len(migration.ClientIDs) != 1 ||
		migration.TimeSource != "client_config_mtime" {
		t.Fatalf("unexpected metadata migration: %#v", migration)
	}
	snapshot := manager.Snapshot()
	client := snapshot.ClientConfig[0]
	if client.Remark != client.ClientID ||
		client.CreatedAt != knownTime.UnixMilli() || client.UpdatedAt != knownTime.UnixMilli() {
		t.Fatalf("legacy metadata was not normalized: %#v", client)
	}

	unchanged := snapshot
	unchanged.ClientConfig[0].CreatedAt = 1
	unchanged.ClientConfig[0].UpdatedAt = 2
	updated, err := manager.Replace(snapshot.Revision, unchanged, nil)
	if err != nil {
		t.Fatal(err)
	}
	if updated.ClientConfig[0].CreatedAt != knownTime.UnixMilli() ||
		updated.ClientConfig[0].UpdatedAt != knownTime.UnixMilli() {
		t.Fatalf("request overwrote server timestamps: %#v", updated.ClientConfig[0])
	}

	changed := updated
	changed.ClientConfig[0].Remark = "Branch office"
	changed.ClientConfig[0].CreatedAt = 3
	changed.ClientConfig[0].UpdatedAt = 4
	final, err := manager.Replace(updated.Revision, changed, nil)
	if err != nil {
		t.Fatal(err)
	}
	if final.ClientConfig[0].CreatedAt != knownTime.UnixMilli() ||
		final.ClientConfig[0].UpdatedAt <= knownTime.UnixMilli() {
		t.Fatalf("semantic update did not advance updated_at: %#v", final.ClientConfig[0])
	}
}
