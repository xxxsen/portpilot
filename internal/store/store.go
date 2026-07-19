package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	_ "modernc.org/sqlite"
)

type Event struct {
	Kind         string
	ClientID     string
	ProxyID      string
	ConnectionID string
	UpBytes      int64
	DownBytes    int64
	Reason       string
	Metadata     map[string]any
}

type Store struct {
	db       *sql.DB
	events   chan Event
	degraded atomic.Bool
	logger   *slog.Logger
	cancel   context.CancelFunc
	done     chan struct{}
}

func Open(path string, logger *slog.Logger) (*Store, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create event database directory: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open event database: %w", err)
	}
	db.SetMaxOpenConns(1)
	for _, statement := range []string{
		`PRAGMA journal_mode=WAL`,
		`PRAGMA busy_timeout=5000`,
		`CREATE TABLE IF NOT EXISTS events (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			created_at TEXT NOT NULL,
			kind TEXT NOT NULL,
			client_id TEXT NOT NULL DEFAULT '',
			proxy_id TEXT NOT NULL DEFAULT '',
			connection_id TEXT NOT NULL DEFAULT '',
			up_bytes INTEGER NOT NULL DEFAULT 0,
			down_bytes INTEGER NOT NULL DEFAULT 0,
			reason TEXT NOT NULL DEFAULT '',
			metadata TEXT NOT NULL DEFAULT '{}'
		)`,
		`CREATE INDEX IF NOT EXISTS idx_events_created_at ON events(created_at)`,
		`CREATE INDEX IF NOT EXISTS idx_events_client_proxy ON events(client_id, proxy_id)`,
	} {
		if _, err := db.ExecContext(context.Background(), statement); err != nil {
			_ = db.Close()
			return nil, fmt.Errorf("initialize sqlite: %w", err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	store := &Store{
		db: db, events: make(chan Event, 2048), logger: logger,
		cancel: cancel, done: make(chan struct{}),
	}
	go store.writer(ctx)
	return store, nil
}

func (s *Store) Record(event Event) {
	if s == nil {
		return
	}
	select {
	case s.events <- event:
	default:
		s.degraded.Store(true)
		s.logger.Warn("event store queue full", "kind", event.Kind)
	}
}

func (s *Store) List(ctx context.Context, limit int) ([]map[string]any, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT created_at, kind, client_id, proxy_id, connection_id,
		       up_bytes, down_bytes, reason, metadata
		FROM events ORDER BY id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query events: %w", err)
	}
	defer func() { _ = rows.Close() }()
	var result []map[string]any
	for rows.Next() {
		var created, kind, clientID, proxyID, connectionID, reason, metadata string
		var upBytes, downBytes int64
		if err := rows.Scan(&created, &kind, &clientID, &proxyID, &connectionID, &upBytes, &downBytes, &reason, &metadata); err != nil {
			return nil, fmt.Errorf("scan event: %w", err)
		}
		item := map[string]any{
			"created_at": created, "kind": kind, "client_id": clientID,
			"proxy_id": proxyID, "connection_id": connectionID,
			"up_bytes": upBytes, "down_bytes": downBytes, "reason": reason,
		}
		var extra map[string]any
		if json.Unmarshal([]byte(metadata), &extra) == nil && len(extra) > 0 {
			item["metadata"] = extra
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate events: %w", err)
	}
	return result, nil
}

func (s *Store) Degraded() bool {
	return s != nil && s.degraded.Load()
}

func (s *Store) Close() error {
	if s == nil {
		return nil
	}
	s.cancel()
	<-s.done
	if err := s.db.Close(); err != nil {
		return fmt.Errorf("close event database: %w", err)
	}
	return nil
}

func (s *Store) flushBatch(ctx context.Context, batch []Event) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin event batch: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_ = tx.Rollback()
		}
	}()
	statement, err := tx.PrepareContext(ctx, `
		INSERT INTO events (
			created_at, kind, client_id, proxy_id, connection_id,
			up_bytes, down_bytes, reason, metadata
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		return fmt.Errorf("prepare event batch: %w", err)
	}
	defer func() { _ = statement.Close() }()
	for _, event := range batch {
		metadata, err := json.Marshal(event.Metadata)
		if err != nil {
			return fmt.Errorf("encode event metadata: %w", err)
		}
		if _, err := statement.ExecContext(ctx,
			time.Now().UTC().Format(time.RFC3339Nano), event.Kind,
			event.ClientID, event.ProxyID, event.ConnectionID,
			event.UpBytes, event.DownBytes, event.Reason, string(metadata),
		); err != nil {
			return fmt.Errorf("insert event: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit event batch: %w", err)
	}
	committed = true
	return nil
}

func (s *Store) writer(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	batch := make([]Event, 0, 128)
	flush := func() {
		if len(batch) == 0 {
			return
		}
		err := s.flushBatch(ctx, batch)
		if err != nil {
			s.degraded.Store(true)
			s.logger.Error("event store batch failed", "error", err)
		} else {
			s.degraded.Store(false)
		}
		batch = batch[:0]
	}
	for {
		select {
		case <-ctx.Done():
			for {
				select {
				case event := <-s.events:
					batch = append(batch, event)
				default:
					flush()
					return
				}
			}
		case event := <-s.events:
			batch = append(batch, event)
			if len(batch) >= cap(batch) {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

func IsUnavailable(err error) bool {
	return err != nil && !errors.Is(err, context.Canceled)
}
