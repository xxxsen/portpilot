package knock

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"
)

type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

func newTestManager(t *testing.T, mutate func(*Config)) (*Manager, *fakeClock) {
	t.Helper()
	clock := &fakeClock{now: time.Unix(1_800_000_000, 0).UTC()}
	randomData := make([]byte, 4096)
	for index := range randomData {
		randomData[index] = byte(index)
	}
	value := Config{
		MagicLinkTTL: 10 * time.Minute, GrantTTL: 10 * time.Minute,
		ResendInterval: 30 * time.Second, AllowedEmails: []string{"user@example.com"},
		MaxRequestsPerHour: 100, MaxPendingTokens: 8, MaxGrants: 8,
		Clock: clock, Random: bytes.NewReader(randomData),
	}
	if mutate != nil {
		mutate(&value)
	}
	manager := New(value)
	t.Cleanup(manager.Close)
	return manager, clock
}

func TestTokenIsBoundSingleUseAndSourceMismatchDoesNotConsume(t *testing.T) {
	t.Parallel()
	manager, _ := newTestManager(t, nil)
	requestIP := netip.MustParseAddr("192.0.2.10")
	result, _, err := manager.Request("user@example.com", requestIP)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := base64.RawURLEncoding.DecodeString(result.Token)
	if err != nil || len(raw) != 32 {
		t.Fatalf("token is not 32-byte base64url: %q, %v", result.Token, err)
	}
	hash := sha256.Sum256(raw)
	if _, err := manager.BeginVerify(hash, netip.MustParseAddr("192.0.2.11")); !errors.Is(err, ErrSourceIPMismatch) {
		t.Fatalf("mismatch error = %v", err)
	}
	verification, err := manager.BeginVerify(hash, requestIP)
	if err != nil {
		t.Fatal(err)
	}
	grant, err := manager.CommitVerify(verification.ID, "audit-1")
	if err != nil {
		t.Fatal(err)
	}
	if grant.SourceIP != requestIP || grant.GrantedBy != "user@example.com" {
		t.Fatalf("unexpected grant: %#v", grant)
	}
	if _, err := manager.BeginVerify(hash, requestIP); !errors.Is(err, ErrInvalidOrExpiredToken) {
		t.Fatalf("used token error = %v", err)
	}
}

func TestTokenExpiryBoundaryAndAbortRetry(t *testing.T) {
	t.Parallel()
	manager, clock := newTestManager(t, nil)
	source := netip.MustParseAddr("2001:db8::10")
	first, _, err := manager.Request("user@example.com", source)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, _ := base64.RawURLEncoding.DecodeString(first.Token)
	firstHash := sha256.Sum256(firstRaw)
	verification, err := manager.BeginVerify(firstHash, source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginVerify(firstHash, source); !errors.Is(err, ErrVerificationInProgress) {
		t.Fatalf("parallel verify error = %v", err)
	}
	manager.AbortVerify(verification.ID)
	retry, err := manager.BeginVerify(firstHash, source)
	if err != nil {
		t.Fatalf("verify after abort: %v", err)
	}
	manager.AbortVerify(retry.ID)

	clock.Advance(10 * time.Minute)
	if _, err := manager.BeginVerify(firstHash, source); !errors.Is(err, ErrInvalidOrExpiredToken) {
		t.Fatalf("expiry boundary error = %v", err)
	}
}

func TestVerificationRemainsAtomicAcrossTokenExpiry(t *testing.T) {
	t.Parallel()
	manager, clock := newTestManager(t, nil)
	sourceIP := netip.MustParseAddr("192.0.2.12")
	result, _, err := manager.Request("user@example.com", sourceIP)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(result.Token)
	clock.Advance(10*time.Minute - time.Second)
	verification, err := manager.BeginVerify(sha256.Sum256(raw), sourceIP)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(2 * time.Second)
	if manager.DiscardToken(sha256.Sum256(raw)) {
		t.Fatal("in-progress token was discarded")
	}
	if _, _, err := manager.Request("user@example.com", sourceIP); !errors.Is(err, ErrVerificationInProgress) {
		t.Fatalf("replacement request error = %v", err)
	}
	if _, err := manager.CommitVerify(verification.ID, "audit-near-expiry"); err != nil {
		t.Fatalf("commit after token expiry: %v", err)
	}
}

func TestNewTokenReplacesOldAndGrantRefreshes(t *testing.T) {
	t.Parallel()
	manager, clock := newTestManager(t, nil)
	source := netip.MustParseAddr("::ffff:192.0.2.20").Unmap()
	first, _, err := manager.Request("user@example.com", source)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, _ := base64.RawURLEncoding.DecodeString(first.Token)
	clock.Advance(30 * time.Second)
	second, _, err := manager.Request("user@example.com", source)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.BeginVerify(sha256.Sum256(firstRaw), source); !errors.Is(err, ErrInvalidOrExpiredToken) {
		t.Fatalf("replaced token error = %v", err)
	}
	secondRaw, _ := base64.RawURLEncoding.DecodeString(second.Token)
	verification, err := manager.BeginVerify(sha256.Sum256(secondRaw), source)
	if err != nil {
		t.Fatal(err)
	}
	firstGrant, err := manager.CommitVerify(verification.ID, "audit-1")
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	third, _, err := manager.Request("user@example.com", source)
	if err != nil {
		t.Fatal(err)
	}
	thirdRaw, _ := base64.RawURLEncoding.DecodeString(third.Token)
	refresh, err := manager.BeginVerify(sha256.Sum256(thirdRaw), source)
	if err != nil {
		t.Fatal(err)
	}
	if !refresh.Refreshed {
		t.Fatal("existing source grant was not marked refreshed")
	}
	refreshed, err := manager.CommitVerify(refresh.ID, "audit-2")
	if err != nil {
		t.Fatal(err)
	}
	if !refreshed.ExpiresAt.After(firstGrant.ExpiresAt) {
		t.Fatalf("grant was not extended: first=%v refreshed=%v", firstGrant.ExpiresAt, refreshed.ExpiresAt)
	}
	if _, ok := manager.Revoke(source); !ok {
		t.Fatal("grant revoke did not find active grant")
	}
	if _, ok := manager.ValidGrant(source); ok {
		t.Fatal("revoked grant remains valid")
	}
}

func TestCapacityAndRateLimits(t *testing.T) {
	t.Parallel()
	manager, clock := newTestManager(t, func(value *Config) {
		value.MaxPendingTokens = 1
		value.MaxRequestsPerHour = 2
		value.AllowedEmails = []string{"user@example.com", "other@example.com"}
	})
	firstIP := netip.MustParseAddr("192.0.2.30")
	if _, _, err := manager.Request("user@example.com", firstIP); err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	if _, _, err := manager.Request("other@example.com", firstIP); !errors.Is(err, ErrPendingCapacity) {
		t.Fatalf("pending capacity error = %v", err)
	}
	if _, _, err := manager.Request("unknown@example.com", firstIP); !errors.Is(err, ErrRateLimited) {
		t.Fatalf("source rate error = %v", err)
	}
}

func TestPendingCapacityAllowsSameEmailReplacement(t *testing.T) {
	t.Parallel()
	manager, clock := newTestManager(t, func(value *Config) {
		value.MaxPendingTokens = 1
	})
	sourceIP := netip.MustParseAddr("192.0.2.31")
	first, _, err := manager.Request("user@example.com", sourceIP)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(time.Minute)
	second, _, err := manager.Request("user@example.com", sourceIP)
	if err != nil {
		t.Fatalf("replace token at capacity: %v", err)
	}
	firstRaw, _ := base64.RawURLEncoding.DecodeString(first.Token)
	if _, err := manager.BeginVerify(sha256.Sum256(firstRaw), sourceIP); !errors.Is(err, ErrInvalidOrExpiredToken) {
		t.Fatalf("old token error = %v", err)
	}
	secondRaw, _ := base64.RawURLEncoding.DecodeString(second.Token)
	if _, err := manager.BeginVerify(sha256.Sum256(secondRaw), sourceIP); err != nil {
		t.Fatalf("new token error = %v", err)
	}
}

func TestGrantCapacityReservationIsAtomic(t *testing.T) {
	t.Parallel()
	manager, clock := newTestManager(t, func(value *Config) {
		value.MaxGrants = 1
		value.AllowedEmails = []string{"user@example.com", "other@example.com"}
	})
	firstIP := netip.MustParseAddr("192.0.2.40")
	first, _, err := manager.Request("user@example.com", firstIP)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, _ := base64.RawURLEncoding.DecodeString(first.Token)
	firstVerification, err := manager.BeginVerify(sha256.Sum256(firstRaw), firstIP)
	if err != nil {
		t.Fatal(err)
	}
	clock.Advance(30 * time.Second)
	secondIP := netip.MustParseAddr("192.0.2.41")
	second, _, err := manager.Request("other@example.com", secondIP)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, _ := base64.RawURLEncoding.DecodeString(second.Token)
	if _, err := manager.BeginVerify(sha256.Sum256(secondRaw), secondIP); !errors.Is(err, ErrGrantCapacity) {
		t.Fatalf("reserved capacity error = %v", err)
	}
	manager.AbortVerify(firstVerification.ID)
	if _, err := manager.BeginVerify(sha256.Sum256(secondRaw), secondIP); err != nil {
		t.Fatalf("capacity was not released after abort: %v", err)
	}
}
