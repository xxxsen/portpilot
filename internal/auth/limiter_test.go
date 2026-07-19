package auth

import (
	"testing"
	"time"
)

func TestRateLimiterSeparatesKeysAndResets(t *testing.T) {
	t.Parallel()
	limiter := NewRateLimiter(2, 20*time.Millisecond)
	if !limiter.Allow("a") {
		t.Fatal("first request was rejected")
	}
	if !limiter.Allow("a") {
		t.Fatal("second request was rejected")
	}
	if limiter.Allow("a") {
		t.Fatal("rate limit was not enforced")
	}
	if !limiter.Allow("b") {
		t.Fatal("one source affected another source")
	}
	time.Sleep(30 * time.Millisecond)
	if !limiter.Allow("a") {
		t.Fatal("rate window did not reset")
	}
}
