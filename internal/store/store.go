package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"

	_ "modernc.org/sqlite"
)

type Store struct {
	db       *sql.DB
	degraded atomic.Bool
}

func Open(path string, logger *slog.Logger) (*Store, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create event database directory: %w", err)
	}
	ctx := context.Background()
	legacyBackup, err := prepareLegacyDatabase(ctx, path, logger)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open event database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`PRAGMA foreign_keys=ON`,
	} {
		if _, err := db.ExecContext(ctx, statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	if err := migrate(ctx, db, path, logger); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("migrate sqlite: %w", err)
	}
	if err := validateDatabase(ctx, db); err != nil {
		_ = db.Close()
		return nil, err
	}
	if path != ":memory:" {
		if err := os.Chmod(path, 0o600); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("set database permissions: %w", err)
		}
	}
	store := &Store{db: db}
	if legacyBackup != "" {
		version, versionErr := store.SchemaVersion(ctx)
		if versionErr != nil {
			_ = store.Close()
			return nil, versionErr
		}
		if _, auditErr := store.WriteAudit(ctx, AuditEvent{
			Category: "system", EventType: "database_rebuilt", Outcome: "success",
			ActorType: "system", Summary: "重建旧版可观测性数据库",
			ReasonCode: "legacy_schema",
			Details: map[string]any{
				"backup_file": filepath.Base(legacyBackup), "migration_version": version,
			},
		}); auditErr != nil {
			_ = store.Close()
			return nil, fmt.Errorf("audit legacy database rebuild: %w", auditErr)
		}
	}
	return store, nil
}

func validateDatabase(ctx context.Context, db *sql.DB) error {
	var integrity string
	if err := db.QueryRowContext(ctx, `PRAGMA integrity_check`).Scan(&integrity); err != nil {
		return fmt.Errorf("run database integrity check: %w", err)
	}
	if integrity != "ok" {
		return fmt.Errorf("database integrity check failed: %s", integrity)
	}
	var foreignKeyViolations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		return fmt.Errorf("run database foreign key check: %w", err)
	}
	if foreignKeyViolations != 0 {
		return fmt.Errorf("database foreign key check found %d violation(s)", foreignKeyViolations)
	}
	return nil
}

func (s *Store) Degraded() bool {
	return s != nil && s.degraded.Load()
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close event database: %w", err)
	}
	return nil
}

func randomID() (string, error) {
	data := make([]byte, 16)
	if _, err := rand.Read(data); err != nil {
		return "", err
	}
	return hex.EncodeToString(data), nil
}

func IsUnavailable(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}
