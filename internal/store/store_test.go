package store

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

func TestEventDatabaseCanBeDeletedAndRebuilt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.WriteAudit(t.Context(), AuditEvent{
		Category: "system", EventType: "system_start", Outcome: "success",
		ActorType: "system", Summary: "test",
	}); err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}

	second, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := second.Close(); err != nil {
			t.Error(err)
		}
	}()
	events, err := second.ListAudit(t.Context(), AuditQuery{Page: 1, PageSize: 25})
	if err != nil {
		t.Fatal(err)
	}
	if events.Total != 0 || len(events.Items) != 0 {
		t.Fatalf("rebuilt event database retained old rows: %#v", events.Items)
	}
}

func TestOpenFailsWithoutAWritableDatabaseDirectory(t *testing.T) {
	t.Parallel()
	parent := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(parent, []byte("file"), 0o600); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(filepath.Join(parent, "events.db"), slog.Default()); err == nil || store != nil {
		t.Fatal("event store opened beneath a regular file")
	}
}
