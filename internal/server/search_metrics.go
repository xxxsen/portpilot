package server

import (
	"sync/atomic"
	"time"
)

type searchQueryMetrics struct {
	requests      atomic.Uint64
	partial       atomic.Uint64
	timeouts      atomic.Uint64
	rateLimited   atomic.Uint64
	durationNanos atomic.Uint64
}

type searchQueryMetricsSnapshot struct {
	Requests      uint64 `json:"requests"`
	Partial       uint64 `json:"partial"`
	Timeouts      uint64 `json:"timeouts"`
	RateLimited   uint64 `json:"rate_limited"`
	DurationNanos uint64 `json:"duration_nanos"`
}

func (m *searchQueryMetrics) observeDuration(started time.Time) {
	//nolint:gosec // time.Since on a just-captured start is non-negative.
	m.durationNanos.Add(uint64(time.Since(started)))
}

func (m *searchQueryMetrics) snapshot() searchQueryMetricsSnapshot {
	return searchQueryMetricsSnapshot{
		Requests: m.requests.Load(), Partial: m.partial.Load(),
		Timeouts: m.timeouts.Load(), RateLimited: m.rateLimited.Load(),
		DurationNanos: m.durationNanos.Load(),
	}
}
