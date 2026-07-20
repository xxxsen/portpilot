package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/portpilot/portpilot/migrations"
	"go.uber.org/zap"
	"modernc.org/sqlite"
)

const backupRetention = 5

var (
	migrationsMu sync.RWMutex
	migrationsFS fs.FS = migrations.FS

	errMigrationNameFormat       = errors.New("migration filename must match NNNN_name.sql")
	errDuplicateMigrationVersion = errors.New("duplicate migration version")
	errMigrationNotDDL           = errors.New("migration SQL must contain DDL only")
	errMigrationChanged          = errors.New("applied migration differs from embedded migration")
	errSchemaDrift               = errors.New("database schema differs from registered migrations")
)

var (
	migrationNamePattern = regexp.MustCompile(`^([0-9]{4})_([a-z0-9]+(?:_[a-z0-9]+)*)\.sql$`)
	blockSQLComment      = regexp.MustCompile(`(?s)/\*.*?\*/`)
)

type migrationFile struct {
	version  int
	filename string
	checksum string
	body     []byte
}

// SetMigrations replaces the embedded migration filesystem for tests.
func SetMigrations(f fs.FS) {
	migrationsMu.Lock()
	migrationsFS = f
	migrationsMu.Unlock()
}

func migrationFilesystem() fs.FS {
	migrationsMu.RLock()
	defer migrationsMu.RUnlock()
	return migrationsFS
}

func migrate(ctx context.Context, db *sql.DB, path string, logger *zap.Logger) error {
	files, err := listMigrationFiles(migrationFilesystem())
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return errors.New("database has no embedded migrations")
	}
	if _, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS schema_migrations (
		version INTEGER PRIMARY KEY,
		filename TEXT NOT NULL,
		checksum TEXT NOT NULL,
		applied_at INTEGER NOT NULL
	)`); err != nil {
		return fmt.Errorf("ensure schema_migrations: %w", err)
	}
	applied, err := appliedMigrations(ctx, db)
	if err != nil {
		return err
	}
	pending := make([]migrationFile, 0, len(files))
	for _, file := range files {
		record, ok := applied[file.version]
		if !ok {
			pending = append(pending, file)
			continue
		}
		if record.filename != file.filename || record.checksum != file.checksum {
			return fmt.Errorf(
				"migration %04d: recorded %q/%s, embedded %q/%s: %w",
				file.version, record.filename, record.checksum,
				file.filename, file.checksum, errMigrationChanged,
			)
		}
	}
	for version := range applied {
		found := false
		for _, file := range files {
			if file.version == version {
				found = true
				break
			}
		}
		if !found {
			return fmt.Errorf("applied migration %04d is missing from the binary: %w", version, errMigrationChanged)
		}
	}
	if len(pending) > 0 && len(applied) > 0 {
		if _, err := backupDatabase(ctx, db, path, "migration", logger); err != nil {
			return fmt.Errorf("backup before migration: %w", err)
		}
	}
	for _, file := range pending {
		if err := applyMigration(ctx, db, file); err != nil {
			return err
		}
		logger.Info(
			"applied database migration",
			zap.Int("version", file.version),
			zap.String("filename", file.filename),
		)
	}
	return validateMigratedSchema(ctx, db, files, path)
}

type appliedMigration struct {
	filename string
	checksum string
}

func appliedMigrations(ctx context.Context, db *sql.DB) (map[int]appliedMigration, error) {
	rows, err := db.QueryContext(ctx, `SELECT version, filename, checksum FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("read schema migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[int]appliedMigration)
	for rows.Next() {
		var version int
		var record appliedMigration
		if err := rows.Scan(&version, &record.filename, &record.checksum); err != nil {
			return nil, fmt.Errorf("scan schema migration: %w", err)
		}
		result[version] = record
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate schema migrations: %w", err)
	}
	return result, nil
}

func listMigrationFiles(source fs.FS) ([]migrationFile, error) {
	if source == nil {
		return nil, errors.New("database migrations are not registered")
	}
	entries, err := fs.ReadDir(source, ".")
	if err != nil {
		return nil, fmt.Errorf("read migrations: %w", err)
	}
	files := make([]migrationFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		matches := migrationNamePattern.FindStringSubmatch(entry.Name())
		if matches == nil {
			return nil, fmt.Errorf("migration %q: %w", entry.Name(), errMigrationNameFormat)
		}
		version, err := strconv.Atoi(matches[1])
		if err != nil {
			return nil, fmt.Errorf("parse migration %q: %w", entry.Name(), err)
		}
		body, err := fs.ReadFile(source, entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read migration %q: %w", entry.Name(), err)
		}
		if err := validateMigrationDDL(entry.Name(), string(body)); err != nil {
			return nil, err
		}
		sum := sha256.Sum256(body)
		files = append(files, migrationFile{
			version: version, filename: entry.Name(),
			checksum: fmt.Sprintf("%x", sum[:]), body: body,
		})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].version < files[j].version })
	for index := 1; index < len(files); index++ {
		if files[index].version == files[index-1].version {
			return nil, fmt.Errorf(
				"migration version %04d is duplicated: %w",
				files[index].version, errDuplicateMigrationVersion,
			)
		}
	}
	return files, nil
}

func applyMigration(ctx context.Context, db *sql.DB, file migrationFile) error {
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin migration %s: %w", file.filename, err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	if _, err := tx.ExecContext(ctx, string(file.body)); err != nil {
		return fmt.Errorf("execute migration %s: %w", file.filename, err)
	}
	if _, err := tx.ExecContext(
		ctx,
		`INSERT INTO schema_migrations(version, filename, checksum, applied_at) VALUES (?, ?, ?, ?)`,
		file.version, file.filename, file.checksum, time.Now().UTC().UnixMilli(),
	); err != nil {
		return fmt.Errorf("record migration %s: %w", file.filename, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit migration %s: %w", file.filename, err)
	}
	committed = true
	return nil
}

func validateMigrationDDL(filename, body string) error {
	body = blockSQLComment.ReplaceAllString(body, "")
	lines := strings.Split(body, "\n")
	for index, line := range lines {
		if comment := strings.Index(line, "--"); comment >= 0 {
			lines[index] = line[:comment]
		}
	}
	for _, statement := range strings.Split(strings.Join(lines, "\n"), ";") {
		fields := strings.Fields(statement)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "CREATE", "ALTER", "DROP", "PRAGMA":
		default:
			return fmt.Errorf(
				"migration %s starts a statement with %q: %w",
				filename, fields[0], errMigrationNotDDL,
			)
		}
	}
	return nil
}

type schemaObject struct {
	kind string
	name string
	sql  string
}

func validateMigratedSchema(ctx context.Context, db *sql.DB, files []migrationFile, path string) error {
	expected, err := expectedSchema(ctx, files)
	if err != nil {
		return err
	}
	actual, err := readSchema(ctx, db)
	if err != nil {
		return err
	}
	var differences []string
	for key, object := range expected {
		current, ok := actual[key]
		if !ok {
			differences = append(differences, object.kind+":"+object.name+" (missing)")
			continue
		}
		if normalizeSchemaSQL(current.sql) != normalizeSchemaSQL(object.sql) {
			differences = append(differences, object.kind+":"+object.name+" (definition changed)")
		}
	}
	for key, object := range actual {
		if _, ok := expected[key]; !ok {
			differences = append(differences, object.kind+":"+object.name+" (unexpected)")
		}
	}
	if len(differences) > 0 {
		sort.Strings(differences)
		return fmt.Errorf(
			"database schema drift in %q: %s: %w",
			path, strings.Join(differences, ", "), errSchemaDrift,
		)
	}
	return nil
}

func expectedSchema(ctx context.Context, files []migrationFile) (map[string]schemaObject, error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return nil, fmt.Errorf("open expected schema database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	for _, file := range files {
		if _, err := db.ExecContext(ctx, string(file.body)); err != nil {
			return nil, fmt.Errorf("build expected schema from %s: %w", file.filename, err)
		}
	}
	return readSchema(ctx, db)
}

func readSchema(ctx context.Context, db *sql.DB) (map[string]schemaObject, error) {
	rows, err := db.QueryContext(ctx, `SELECT type, name, COALESCE(sql, '')
		FROM sqlite_master
		WHERE type IN ('table', 'index', 'view', 'trigger')
		  AND name NOT LIKE 'sqlite_%'
		  AND name <> 'schema_migrations'
		ORDER BY type, name`)
	if err != nil {
		return nil, fmt.Errorf("read database schema: %w", err)
	}
	defer func() { _ = rows.Close() }()
	result := make(map[string]schemaObject)
	for rows.Next() {
		var object schemaObject
		if err := rows.Scan(&object.kind, &object.name, &object.sql); err != nil {
			return nil, fmt.Errorf("scan database schema: %w", err)
		}
		result[object.kind+":"+object.name] = object
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate database schema: %w", err)
	}
	return result, nil
}

func normalizeSchemaSQL(value string) string {
	return strings.ToLower(strings.Join(strings.Fields(value), " "))
}

func prepareLegacyDatabase(ctx context.Context, path string, logger *zap.Logger) (string, error) {
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) || (err == nil && info.Size() == 0) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("stat database: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return "", fmt.Errorf("open existing database: %w", err)
	}
	db.SetMaxOpenConns(1)
	defer func() { _ = db.Close() }()
	var ledger int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='schema_migrations'`,
	).Scan(&ledger); err != nil {
		return "", fmt.Errorf("inspect migration ledger: %w", err)
	}
	if ledger > 0 {
		return "", nil
	}
	var legacyEvents int
	if err := db.QueryRowContext(
		ctx,
		`SELECT COUNT(*) FROM sqlite_master WHERE type='table' AND name='events'`,
	).Scan(&legacyEvents); err != nil {
		return "", fmt.Errorf("inspect legacy database: %w", err)
	}
	if legacyEvents == 0 {
		return "", errors.New("existing database has no migration ledger and is not a recognized PortPilot legacy database")
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", fmt.Errorf("checkpoint legacy database: %w", err)
	}
	backupPath, err := backupDatabase(ctx, db, path, "legacy", logger)
	if err != nil {
		return "", fmt.Errorf("backup legacy database: %w", err)
	}
	if err := db.Close(); err != nil {
		return "", fmt.Errorf("close legacy database: %w", err)
	}
	for _, suffix := range []string{"", "-wal", "-shm"} {
		if err := os.Remove(path + suffix); err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("remove legacy database file %q: %w", path+suffix, err)
		}
	}
	return backupPath, nil
}

func backupDatabase(ctx context.Context, db *sql.DB, path, reason string, logger *zap.Logger) (string, error) {
	if path == ":memory:" {
		return "", nil
	}
	if _, err := db.ExecContext(ctx, `PRAGMA wal_checkpoint(TRUNCATE)`); err != nil {
		return "", fmt.Errorf("checkpoint database before backup: %w", err)
	}
	var pages int
	if err := db.QueryRowContext(ctx, `PRAGMA page_count`).Scan(&pages); err != nil {
		return "", fmt.Errorf("read database page count: %w", err)
	}
	if pages == 0 {
		return "", nil
	}
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	timestamp := time.Now().UTC().Format("20060102T150405.000000000Z")
	backupPath := filepath.Join(dir, fmt.Sprintf("%s.%s-%s.bak", base, reason, timestamp))
	if _, err := os.Stat(backupPath); err == nil {
		return "", fmt.Errorf("backup path already exists: %s", backupPath)
	}
	conn, err := db.Conn(ctx)
	if err != nil {
		return "", fmt.Errorf("reserve database connection for backup: %w", err)
	}
	defer func() { _ = conn.Close() }()
	success := false
	defer func() {
		if !success {
			_ = os.Remove(backupPath)
		}
	}()
	if err := conn.Raw(func(driverConn any) error {
		backuper, ok := driverConn.(interface {
			NewBackup(string) (*sqlite.Backup, error)
		})
		if !ok {
			return errors.New("sqlite driver does not support online backup")
		}
		backup, err := backuper.NewBackup(backupPath)
		if err != nil {
			return fmt.Errorf("start online backup: %w", err)
		}
		finished := false
		defer func() {
			if !finished {
				_ = backup.Finish()
			}
		}()
		for more := true; more; {
			if err := ctx.Err(); err != nil {
				return fmt.Errorf("cancel online backup: %w", err)
			}
			more, err = backup.Step(256)
			if err != nil {
				return fmt.Errorf("copy online backup pages: %w", err)
			}
		}
		if err := backup.Finish(); err != nil {
			return fmt.Errorf("finish online backup: %w", err)
		}
		finished = true
		return nil
	}); err != nil {
		return "", fmt.Errorf("backup database: %w", err)
	}
	if err := os.Chmod(backupPath, 0o600); err != nil {
		return "", fmt.Errorf("set database backup permissions: %w", err)
	}
	success = true
	logger.Info(
		"created database backup",
		zap.String("reason", reason),
		zap.String("file", filepath.Base(backupPath)),
	)
	if err := pruneBackups(dir, base); err != nil {
		return "", err
	}
	return backupPath, nil
}

func pruneBackups(dir, base string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("read database backup directory: %w", err)
	}
	var backups []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), base+".") || !strings.HasSuffix(entry.Name(), ".bak") {
			continue
		}
		backups = append(backups, filepath.Join(dir, entry.Name()))
	}
	sort.Strings(backups)
	for len(backups) > backupRetention {
		if err := os.Remove(backups[0]); err != nil {
			return fmt.Errorf("remove old database backup: %w", err)
		}
		backups = backups[1:]
	}
	return nil
}
