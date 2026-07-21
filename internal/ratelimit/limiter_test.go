package ratelimit

import (
	"sync"
	"testing"
	"time"
)

func TestLimiterWaitAndDynamicDisableWithFakeClock(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	limiter := newWithClock(1024, false, 0, clock)
	done := make(chan struct {
		waited time.Duration
		err    error
	}, 1)
	go func() {
		waited, err := limiter.WaitN(t.Context(), 204)
		done <- struct {
			waited time.Duration
			err    error
		}{waited: waited, err: err}
	}()
	clock.waitForTimer(t)
	clock.advance(100 * time.Millisecond)
	result := <-done
	if result.err != nil || result.waited != 100*time.Millisecond {
		t.Fatalf("wait result = %#v", result)
	}

	done = make(chan struct {
		waited time.Duration
		err    error
	}, 1)
	go func() {
		waited, err := limiter.WaitN(t.Context(), 204)
		done <- struct {
			waited time.Duration
			err    error
		}{waited: waited, err: err}
	}()
	clock.waitForTimer(t)
	if err := limiter.Update(0, false, 0); err != nil {
		t.Fatal(err)
	}
	result = <-done
	if result.err != nil {
		t.Fatalf("dynamic disable failed: %v", result.err)
	}
}

func TestUDPLimiterDropsWithoutWaiting(t *testing.T) {
	t.Parallel()
	clock := newFakeClock()
	limiter := newWithClock(1024, true, 1200, clock)
	if !limiter.AllowN(1200) {
		t.Fatal("initial datagram was rejected")
	}
	if limiter.AllowN(1200) {
		t.Fatal("excess datagram was admitted")
	}
	clock.advance(2 * time.Second)
	if !limiter.AllowN(1200) {
		t.Fatal("datagram was not admitted after refill")
	}
}

type fakeClock struct {
	mu      sync.Mutex
	now     time.Time
	timers  []*fakeTimer
	created chan struct{}
}

type fakeTimer struct {
	clock    *fakeClock
	deadline time.Time
	channel  chan time.Time
	stopped  bool
}

func newFakeClock() *fakeClock {
	return &fakeClock{
		now: time.Unix(1000, 0), created: make(chan struct{}, 16),
	}
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) NewTimer(delay time.Duration) timer {
	c.mu.Lock()
	value := &fakeTimer{
		clock: c, deadline: c.now.Add(delay), channel: make(chan time.Time, 1),
	}
	c.timers = append(c.timers, value)
	c.mu.Unlock()
	c.created <- struct{}{}
	return value
}

func (c *fakeClock) waitForTimer(t *testing.T) {
	t.Helper()
	select {
	case <-c.created:
	case <-time.After(time.Second):
		t.Fatal("limiter did not create a timer")
	}
}

func (c *fakeClock) advance(value time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(value)
	now := c.now
	for _, item := range c.timers {
		if !item.stopped && !item.deadline.After(now) {
			item.stopped = true
			item.channel <- now
		}
	}
	c.mu.Unlock()
}

func (t *fakeTimer) Channel() <-chan time.Time {
	return t.channel
}

func (t *fakeTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	if t.stopped {
		return false
	}
	t.stopped = true
	return true
}
