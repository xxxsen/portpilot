package main

import (
	"testing"
	"time"
)

func TestPersistentWindowStaysWithinConfiguredSpread(t *testing.T) {
	t.Parallel()

	const base = 20 * time.Second
	seen := make(map[time.Duration]struct{})
	for worker := 0; worker < 16; worker++ {
		for generation := 1; generation <= 16; generation++ {
			window := persistentWindow(base, worker, generation)
			if window < 15*time.Second || window > 25*time.Second {
				t.Fatalf("persistentWindow(%d, %d) = %s, want [15s, 25s]", worker, generation, window)
			}
			seen[window] = struct{}{}
		}
	}
	if len(seen) < 5 {
		t.Fatalf("persistentWindow produced only %d distinct windows, want distributed reconnects", len(seen))
	}
}
