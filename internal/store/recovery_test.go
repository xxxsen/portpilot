package store

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	schemamigrations "github.com/portpilot/portpilot/migrations"
	"go.uber.org/zap"
)

func TestOpenQuarantinesSchemaDriftAndRebuilds(t *testing.T) {
	original := migrationFilesystem()
	SetMigrations(schemamigrations.FS)
	t.Cleanup(func() { SetMigrations(original) })

	dir := t.TempDir()
	path := filepath.Join(dir, "portpilot.db")
	opened, err := Open(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	if err := opened.Close(); err != nil {
		t.Fatal(err)
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `DROP INDEX idx_network_connections_external_ip_accepted`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	rebuilt, err := Open(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rebuilt.Close() }()

	version, err := rebuilt.SchemaVersion(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if version != 4 {
		t.Fatalf("schema version=%d, want 4", version)
	}
	var reason string
	if err := rebuilt.db.QueryRowContext(t.Context(), `
		SELECT reason_code FROM audit_events
		WHERE event_type='database_rebuilt'
		ORDER BY occurred_at DESC LIMIT 1
	`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "schema_drift" {
		t.Fatalf("reason=%q", reason)
	}
	entries, err := os.ReadDir(filepath.Join(dir, "quarantine"))
	if err != nil {
		t.Fatal(err)
	}
	mainFiles := 0
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "portpilot-") && strings.HasSuffix(entry.Name(), ".db") {
			mainFiles++
		}
	}
	if mainFiles != 1 {
		t.Fatalf("quarantine main files=%d, want 1", mainFiles)
	}
}

func TestOpenQuarantinesCorruptDatabase(t *testing.T) {
	original := migrationFilesystem()
	SetMigrations(schemamigrations.FS)
	t.Cleanup(func() { SetMigrations(original) })

	dir := t.TempDir()
	path := filepath.Join(dir, "portpilot.db")
	if err := os.WriteFile(path, []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	rebuilt, err := Open(path, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rebuilt.Close() }()
	var reason string
	if err := rebuilt.db.QueryRowContext(t.Context(), `
		SELECT reason_code FROM audit_events
		WHERE event_type='database_rebuilt'
	`).Scan(&reason); err != nil {
		t.Fatal(err)
	}
	if reason != "database_corrupt" {
		t.Fatalf("reason=%q", reason)
	}
}

func TestPruneQuarantineKeepsTwoRecentSetsAndSevenDays(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	for index, age := range []time.Duration{10 * 24 * time.Hour, 3 * time.Hour, 2 * time.Hour, time.Hour} {
		name := filepath.Join(dir, "portpilot-20260722T12000"+string(rune('0'+index))+".000000000Z.db")
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		stamp := now.Add(-age)
		if err := os.Chtimes(name, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}
	if err := pruneQuarantine(dir, now); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 {
		t.Fatalf("quarantine entries=%d, want 2", len(entries))
	}
}
