package store

import (
	"context"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestEventDatabaseCanBeDeletedAndRebuilt(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "events.db")
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	first, err := Open(path, logger)
	if err != nil {
		t.Fatal(err)
	}
	first.Record(Event{Kind: "tcp_tunnel", ClientID: "client", UpBytes: 12})
	deadline := time.Now().Add(2 * time.Second)
	for {
		events, listErr := first.List(t.Context(), 10)
		if listErr != nil {
			t.Fatal(listErr)
		}
		if len(events) == 1 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("event writer did not flush")
		}
		time.Sleep(20 * time.Millisecond)
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
	events, err := second.List(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatalf("rebuilt event database retained old rows: %#v", events)
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
