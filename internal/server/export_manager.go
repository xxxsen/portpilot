package server

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

type exportMetrics struct {
	Requested            atomic.Uint64
	Started              atomic.Uint64
	Succeeded            atomic.Uint64
	Failed               atomic.Uint64
	Rejected             atomic.Uint64
	Canceled             atomic.Uint64
	Busy                 atomic.Uint64
	RateLimited          atomic.Uint64
	Rows                 atomic.Uint64
	UncompressedBytes    atomic.Uint64
	ArchiveBytes         atomic.Uint64
	GeneratedNanos       atomic.Uint64
	ArchiveTooLarge      atomic.Uint64
	UncompressedTooLarge atomic.Uint64
	Current              atomic.Int64
}

type exportMetricsSnapshot struct {
	Requested            uint64 `json:"requested"`
	Started              uint64 `json:"started"`
	Succeeded            uint64 `json:"succeeded"`
	Failed               uint64 `json:"failed"`
	Rejected             uint64 `json:"rejected"`
	Canceled             uint64 `json:"canceled"`
	Busy                 uint64 `json:"busy"`
	RateLimited          uint64 `json:"rate_limited"`
	Rows                 uint64 `json:"rows"`
	UncompressedBytes    uint64 `json:"uncompressed_bytes"`
	ArchiveBytes         uint64 `json:"archive_bytes"`
	GeneratedNanos       uint64 `json:"generated_nanos"`
	ArchiveTooLarge      uint64 `json:"archive_too_large"`
	UncompressedTooLarge uint64 `json:"uncompressed_too_large"`
	Current              int64  `json:"current"`
}

func (m *exportMetrics) snapshot() exportMetricsSnapshot {
	return exportMetricsSnapshot{
		Requested: m.Requested.Load(), Started: m.Started.Load(),
		Succeeded: m.Succeeded.Load(), Failed: m.Failed.Load(),
		Rejected: m.Rejected.Load(), Canceled: m.Canceled.Load(),
		Busy: m.Busy.Load(), RateLimited: m.RateLimited.Load(),
		Rows: m.Rows.Load(), UncompressedBytes: m.UncompressedBytes.Load(),
		ArchiveBytes: m.ArchiveBytes.Load(), GeneratedNanos: m.GeneratedNanos.Load(),
		ArchiveTooLarge:      m.ArchiveTooLarge.Load(),
		UncompressedTooLarge: m.UncompressedTooLarge.Load(),
		Current:              m.Current.Load(),
	}
}

type exportRateWindow struct {
	started time.Time
	count   int
}

type exportManager struct {
	ctx    context.Context
	cancel context.CancelFunc
	now    func() time.Time

	mu       sync.Mutex
	active   bool
	sessions map[string]bool
	windows  map[string]exportRateWindow
	wg       sync.WaitGroup
	metrics  exportMetrics
}

func newExportManager() *exportManager {
	ctx, cancel := context.WithCancel(context.Background())
	return &exportManager{
		ctx: ctx, cancel: cancel, now: time.Now,
		sessions: make(map[string]bool), windows: make(map[string]exportRateWindow),
	}
}

func (m *exportManager) acquire(sessionID string) (func(), string) {
	m.metrics.Requested.Add(1)
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	window := m.windows[sessionID]
	if window.started.IsZero() || now.Sub(window.started) >= time.Minute {
		window = exportRateWindow{started: now}
	}
	if window.count >= 5 {
		m.metrics.RateLimited.Add(1)
		m.metrics.Rejected.Add(1)
		return nil, "rate_limited"
	}
	window.count++
	m.windows[sessionID] = window
	if m.sessions[sessionID] || m.active {
		m.metrics.Busy.Add(1)
		m.metrics.Rejected.Add(1)
		return nil, "busy"
	}
	m.active = true
	m.sessions[sessionID] = true
	m.wg.Add(1)
	m.metrics.Started.Add(1)
	m.metrics.Current.Add(1)
	var once sync.Once
	return func() {
		once.Do(func() {
			m.mu.Lock()
			m.active = false
			delete(m.sessions, sessionID)
			m.mu.Unlock()
			m.metrics.Current.Add(-1)
			m.wg.Done()
		})
	}, ""
}

func (m *exportManager) operationContext(parent context.Context) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithTimeout(parent, 30*time.Second)
	stop := context.AfterFunc(m.ctx, cancel)
	return ctx, func() {
		stop()
		cancel()
	}
}

func (m *exportManager) close(ctx context.Context) error {
	m.cancel()
	done := make(chan struct{})
	go func() {
		m.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("wait for active exports: %w", ctx.Err())
	}
}
