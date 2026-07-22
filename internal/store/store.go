package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xxxsen/common/logutil"
	"go.uber.org/zap"
	_ "modernc.org/sqlite"
)

type Store struct {
	db                  *sql.DB
	degraded            atomic.Bool
	analyticsVersion    atomic.Uint64
	analyticsCacheMu    sync.RWMutex
	analyticsCache      map[analyticsCacheKey]any
	comparisonCacheHits atomic.Uint64
}

var errIntegrityFailed = errors.New("database integrity validation failed")

func Open(path string, logger *zap.Logger) (*Store, error) {
	if logger == nil {
		logger = logutil.GetLogger(context.Background())
	}
	hadDatabase := false
	if path != ":memory:" {
		if info, err := os.Stat(path); err == nil && info.Size() > 0 {
			hadDatabase = true
		}
	}
	opened, err := openOnce(path, logger)
	if err == nil || !hadDatabase || path == ":memory:" {
		return opened, err
	}
	reason := rebuildReason(err)
	if reason == "" {
		return nil, err
	}
	quarantineSet, quarantineErr := quarantineDatabase(path, time.Now().UTC())
	if quarantineErr != nil {
		return nil, fmt.Errorf("quarantine observability database after %s: %w", reason, quarantineErr)
	}
	rebuilt, rebuildErr := openOnce(path, logger)
	if rebuildErr != nil {
		return nil, fmt.Errorf("rebuild observability database after %s: %w", reason, rebuildErr)
	}
	version, versionErr := rebuilt.SchemaVersion(context.Background())
	if versionErr != nil {
		_ = rebuilt.Close()
		return nil, versionErr
	}
	if _, auditErr := rebuilt.WriteAudit(context.Background(), AuditEvent{
		Category: "system", EventType: "database_rebuilt", Outcome: "success",
		ActorType: "system", Summary: "重建可观测性数据库", ReasonCode: reason,
		Details: map[string]any{
			"quarantine_set": quarantineSet, "migration_version": version,
		},
	}); auditErr != nil {
		_ = rebuilt.Close()
		return nil, fmt.Errorf("audit database rebuild: %w", auditErr)
	}
	logger.Warn("rebuilt disposable observability database", zap.String("reason_code", reason))
	return rebuilt, nil
}

func rebuildReason(err error) string {
	switch {
	case errors.Is(err, errMigrationChanged):
		return "checksum_mismatch"
	case errors.Is(err, errSchemaDrift):
		return "schema_drift"
	case errors.Is(err, errIntegrityFailed):
		return "integrity_failed"
	}
	message := strings.ToLower(err.Error())
	if strings.Contains(message, "database disk image is malformed") ||
		strings.Contains(message, "file is not a database") {
		return "database_corrupt"
	}
	return ""
}

func quarantineDatabase(path string, now time.Time) (string, error) {
	dir := filepath.Join(filepath.Dir(path), "quarantine")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create quarantine directory: %w", err)
	}
	if info, err := os.Lstat(dir); err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("quarantine path must be a real directory")
	}
	set := "portpilot-" + now.Format("20060102T150405.000000000Z") + ".db"
	for _, suffix := range []string{"", "-wal", "-shm"} {
		source := path + suffix
		if _, err := os.Lstat(source); errors.Is(err, os.ErrNotExist) {
			continue
		} else if err != nil {
			return "", fmt.Errorf("inspect database component: %w", err)
		}
		target := filepath.Join(dir, set+suffix)
		if err := os.Rename(source, target); err != nil {
			return "", fmt.Errorf("move database component to quarantine: %w", err)
		}
	}
	if err := pruneQuarantine(dir, now); err != nil {
		return "", err
	}
	return set, nil
}

func pruneQuarantine(dir string, now time.Time) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read quarantine directory: %w", err)
	}
	type setInfo struct {
		name    string
		modTime time.Time
	}
	sets := make([]setInfo, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasPrefix(name, "portpilot-") || !strings.HasSuffix(name, ".db") {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			return fmt.Errorf("inspect quarantine set: %w", infoErr)
		}
		sets = append(sets, setInfo{name: name, modTime: info.ModTime()})
	}
	sort.Slice(sets, func(i, j int) bool {
		if sets[i].modTime.Equal(sets[j].modTime) {
			return sets[i].name < sets[j].name
		}
		return sets[i].modTime.Before(sets[j].modTime)
	})
	for index, set := range sets {
		if len(sets)-index <= 2 && now.Sub(set.modTime) <= 7*24*time.Hour {
			continue
		}
		for _, suffix := range []string{"", "-wal", "-shm"} {
			if err := os.Remove(filepath.Join(dir, set.name+suffix)); err != nil && !errors.Is(err, os.ErrNotExist) {
				return fmt.Errorf("remove expired quarantine component: %w", err)
			}
		}
	}
	return nil
}

func openOnce(path string, logger *zap.Logger) (*Store, error) {
	if logger == nil {
		logger = logutil.GetLogger(context.Background())
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
	store := &Store{db: db, analyticsCache: make(map[analyticsCacheKey]any)}
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
				"quarantine_set": legacyBackup, "migration_version": version,
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
		return fmt.Errorf("run database integrity check: %w: %w", errIntegrityFailed, err)
	}
	if integrity != "ok" {
		return fmt.Errorf("%w: %s", errIntegrityFailed, integrity)
	}
	var foreignKeyViolations int
	if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM pragma_foreign_key_check`).Scan(&foreignKeyViolations); err != nil {
		return fmt.Errorf("run database foreign key check: %w: %w", errIntegrityFailed, err)
	}
	if foreignKeyViolations != 0 {
		return fmt.Errorf("%w: foreign key check found %d violation(s)", errIntegrityFailed, foreignKeyViolations)
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
