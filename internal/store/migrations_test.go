package store

import (
	"database/sql"
	"errors"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	schemamigrations "github.com/portpilot/portpilot/migrations"
)

func TestEmbeddedMigrationsAreNamedAndDDLOnly(t *testing.T) {
	entries, err := fs.ReadDir(schemamigrations.FS, ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "0001_init.sql" {
		t.Fatalf("unexpected migration files: %#v", entries)
	}
	dml := regexp.MustCompile(`(?im)^\s*(insert|update|delete|replace|merge|select)\b`)
	for _, entry := range entries {
		body, err := fs.ReadFile(schemamigrations.FS, entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if match := dml.Find(body); match != nil {
			t.Fatalf("migration %s contains non-DDL statement %q", entry.Name(), match)
		}
		if err := validateMigrationDDL(entry.Name(), string(body)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestMigrationUpgradeChecksumDriftAndRollback(t *testing.T) {
	original := migrationFilesystem()
	t.Cleanup(func() { SetMigrations(original) })
	base := []byte(`CREATE TABLE alpha (id INTEGER PRIMARY KEY);`)
	second := []byte(`CREATE TABLE beta (id INTEGER PRIMARY KEY);`)
	SetMigrations(fstest.MapFS{
		"0001_init.sql": &fstest.MapFile{Data: base},
	})
	path := filepath.Join(t.TempDir(), "store.db")
	first, err := Open(path, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Close(); err != nil {
		t.Fatal(err)
	}

	SetMigrations(fstest.MapFS{
		"0001_init.sql":     &fstest.MapFile{Data: base},
		"0002_add_beta.sql": &fstest.MapFile{Data: second},
	})
	upgraded, err := Open(path, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	var migrations int
	if err := upgraded.db.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM schema_migrations`).Scan(&migrations); err != nil {
		t.Fatal(err)
	}
	if migrations != 2 {
		t.Fatalf("migration count=%d, want 2", migrations)
	}
	if err := upgraded.Close(); err != nil {
		t.Fatal(err)
	}

	SetMigrations(fstest.MapFS{
		"0001_init.sql":     &fstest.MapFile{Data: []byte(`CREATE TABLE alpha (id TEXT PRIMARY KEY);`)},
		"0002_add_beta.sql": &fstest.MapFile{Data: second},
	})
	if store, err := Open(path, slog.Default()); !errors.Is(err, errMigrationChanged) || store != nil {
		t.Fatalf("changed migration error=%v store=%v", err, store)
	}

	SetMigrations(fstest.MapFS{
		"0001_init.sql":     &fstest.MapFile{Data: base},
		"0002_add_beta.sql": &fstest.MapFile{Data: second},
	})
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `DROP TABLE beta`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	if store, err := Open(path, slog.Default()); !errors.Is(err, errSchemaDrift) || store != nil {
		t.Fatalf("schema drift error=%v store=%v", err, store)
	}

	rollbackPath := filepath.Join(t.TempDir(), "rollback.db")
	SetMigrations(fstest.MapFS{
		"0001_init.sql": &fstest.MapFile{Data: []byte(
			`CREATE TABLE first_table (id INTEGER); CREATE TABLE first_table (id INTEGER);`,
		)},
	})
	if store, err := Open(rollbackPath, slog.Default()); err == nil || store != nil {
		t.Fatalf("broken migration opened store=%v err=%v", store, err)
	}
	rollbackDB, err := sql.Open("sqlite", rollbackPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rollbackDB.Close() }()
	var created int
	if err := rollbackDB.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='first_table'`,
	).Scan(&created); err != nil {
		t.Fatal(err)
	}
	if created != 0 {
		t.Fatal("failed migration left partial business schema")
	}
}

func TestLegacyDatabaseIsBackedUpAndRebuilt(t *testing.T) {
	SetMigrations(schemamigrations.FS)
	dir := t.TempDir()
	path := filepath.Join(dir, "portpilot.db")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `CREATE TABLE events (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		created_at TEXT NOT NULL,
		kind TEXT NOT NULL
	)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(t.Context(), `INSERT INTO events(created_at, kind) VALUES ('now', 'legacy')`); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := Open(path, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := store.Close(); err != nil {
			t.Error(err)
		}
	}()
	var auditTable int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='audit_events'`,
	).Scan(&auditTable); err != nil {
		t.Fatal(err)
	}
	if auditTable != 1 {
		t.Fatal("rebuilt database does not contain the current schema")
	}
	var legacyTable int
	if err := store.db.QueryRowContext(t.Context(),
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='events'`,
	).Scan(&legacyTable); err != nil {
		t.Fatal(err)
	}
	if legacyTable != 0 {
		t.Fatal("legacy events table survived disposable database rebuild")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	foundBackup := false
	backupPath := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "portpilot.db.legacy-") &&
			strings.HasSuffix(entry.Name(), ".bak") {
			foundBackup = true
			backupPath = filepath.Join(dir, entry.Name())
		}
	}
	if !foundBackup {
		t.Fatal("legacy database backup was not created")
	}
	backup, err := sql.Open("sqlite", backupPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = backup.Close() }()
	var legacyRows int
	if err := backup.QueryRowContext(t.Context(), `SELECT COUNT(*) FROM events`).Scan(&legacyRows); err != nil {
		t.Fatalf("legacy backup is not a readable consistent database: %v", err)
	}
	if legacyRows != 1 {
		t.Fatalf("legacy backup rows=%d, want 1", legacyRows)
	}
}

func TestValidateMigrationDDLRejectsDML(t *testing.T) {
	for _, statement := range []string{
		"INSERT INTO example VALUES (1);",
		"UPDATE example SET id=1;",
		"DELETE FROM example;",
		"REPLACE INTO example VALUES (1);",
		"SELECT * FROM example;",
	} {
		if err := validateMigrationDDL("0001_test.sql", statement); !errors.Is(err, errMigrationNotDDL) {
			t.Fatalf("statement %q error=%v", statement, err)
		}
	}
}

func TestDatabaseIntegrityChecksForeignKeys(t *testing.T) {
	store, err := Open(filepath.Join(t.TempDir(), "store.db"), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = store.Close() }()
	if err := validateDatabase(t.Context(), store.db); err != nil {
		t.Fatal(err)
	}
}
