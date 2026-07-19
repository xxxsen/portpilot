package auth

import (
	"sync"
	"time"
)

type rateWindow struct {
	start time.Time
	count int
}

type RateLimiter struct {
	mu       sync.Mutex
	windows  map[string]rateWindow
	limit    int
	interval time.Duration
}

func NewRateLimiter(limit int, interval time.Duration) *RateLimiter {
	return &RateLimiter{
		windows: make(map[string]rateWindow),
		limit:   limit, interval: interval,
	}
}

func (l *RateLimiter) Allow(key string) bool {
	now := time.Now()
	l.mu.Lock()
	defer l.mu.Unlock()
	window := l.windows[key]
	if window.start.IsZero() || now.Sub(window.start) >= l.interval {
		window = rateWindow{start: now}
	}
	window.count++
	l.windows[key] = window
	if len(l.windows) > 4096 {
		for item, candidate := range l.windows {
			if now.Sub(candidate.start) >= l.interval {
				delete(l.windows, item)
			}
		}
	}
	return window.count <= l.limit
}
