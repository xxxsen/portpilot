package knock

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"io"
	"net/netip"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

var (
	ErrNotAllowed             = errors.New("email not allowed")
	ErrRateLimited            = errors.New("rate limited")
	ErrPendingCapacity        = errors.New("pending token capacity exceeded")
	ErrInvalidOrExpiredToken  = errors.New("invalid or expired token")
	ErrSourceIPMismatch       = errors.New("source IP mismatch")
	ErrVerificationInProgress = errors.New("verification in progress")
	ErrGrantCapacity          = errors.New("grant capacity exceeded")
)

type Config struct {
	MagicLinkTTL       time.Duration
	GrantTTL           time.Duration
	ResendInterval     time.Duration
	AllowedEmails      []string
	MaxRequestsPerHour int
	MaxPendingTokens   int
	MaxGrants          int
	Clock              Clock
	Random             io.Reader
}

type PendingToken struct {
	Hash           [32]byte
	Email          string
	RequestedIP    netip.Addr
	CreatedAt      time.Time
	ExpiresAt      time.Time
	Verifying      bool
	VerificationID string
	GrantAt        time.Time
	GrantExpiresAt time.Time
	ReservedGrant  bool
}

type Grant struct {
	SourceIP  netip.Addr `json:"-"`
	GrantedBy string     `json:"granted_by"`
	GrantedAt time.Time  `json:"-"`
	ExpiresAt time.Time  `json:"-"`
	AuditID   string     `json:"-"`
}

type Verification struct {
	ID          string
	Email       string
	RequestedIP netip.Addr
	RequestedAt time.Time
	GrantedAt   time.Time
	ExpiresAt   time.Time
	Refreshed   bool
}

type RequestResult struct {
	Token     string
	TokenHash [32]byte
	ExpiresAt time.Time
	Allowed   bool
}

type window struct {
	Started time.Time
	Count   int
}

type Manager struct {
	mu               sync.Mutex
	clock            Clock
	random           io.Reader
	magicLinkTTL     time.Duration
	grantTTL         time.Duration
	resendInterval   time.Duration
	maxRequests      int
	maxPending       int
	maxGrants        int
	globalLimit      int
	rateEntryLimit   int
	allowedEmails    map[string]struct{}
	pendingByHash    map[[32]byte]*PendingToken
	pendingByEmail   map[string][32]byte
	verifyingByID    map[string][32]byte
	grants           map[netip.Addr]Grant
	requestWindows   map[string]window
	lastRequestEmail map[string]time.Time
	verifyWindows    map[string]window
	reservedGrants   int
	closeCh          chan struct{}
	closeOnce        sync.Once
	rateLimited      atomic.Uint64
	verifySuccess    atomic.Uint64
	verifyFailure    atomic.Uint64
	sourceMismatch   atomic.Uint64
	grantRevoked     atomic.Uint64
}

type Stats struct {
	ActiveTokens       int
	ActiveGrants       int
	RequestRateLimited uint64
	VerifySuccess      uint64
	VerifyFailure      uint64
	SourceMismatch     uint64
	GrantRevoked       uint64
}

func New(config Config) *Manager {
	clock := config.Clock
	if clock == nil {
		clock = realClock{}
	}
	random := config.Random
	if random == nil {
		random = rand.Reader
	}
	allowed := make(map[string]struct{}, len(config.AllowedEmails))
	for _, email := range config.AllowedEmails {
		allowed[email] = struct{}{}
	}
	globalLimit := config.MaxRequestsPerHour * max(1, len(allowed)) * 2
	manager := &Manager{
		clock: clock, random: random,
		magicLinkTTL: config.MagicLinkTTL, grantTTL: config.GrantTTL,
		resendInterval: config.ResendInterval, maxRequests: config.MaxRequestsPerHour,
		maxPending: config.MaxPendingTokens, maxGrants: config.MaxGrants,
		globalLimit: globalLimit, rateEntryLimit: min(65536, max(1024, config.MaxPendingTokens*4)),
		allowedEmails: allowed, pendingByHash: make(map[[32]byte]*PendingToken),
		pendingByEmail: make(map[string][32]byte), verifyingByID: make(map[string][32]byte),
		grants: make(map[netip.Addr]Grant), requestWindows: make(map[string]window),
		lastRequestEmail: make(map[string]time.Time), verifyWindows: make(map[string]window),
		closeCh: make(chan struct{}),
	}
	go manager.cleanupLoop()
	return manager
}

func (m *Manager) Close() {
	m.closeOnce.Do(func() { close(m.closeCh) })
}

func (m *Manager) Request(email string, sourceIP netip.Addr) (RequestResult, time.Duration, error) {
	now := m.clock.Now()
	sourceIP = sourceIP.Unmap()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	retry, allowed := m.allowRequestLocked(now, email, sourceIP)
	if !allowed {
		m.rateLimited.Add(1)
		return RequestResult{}, retry, ErrRateLimited
	}
	if _, ok := m.allowedEmails[email]; !ok {
		return RequestResult{Allowed: false}, 0, ErrNotAllowed
	}
	previous, hasPrevious := m.pendingByEmail[email]
	if pending := m.pendingByHash[previous]; hasPrevious && pending != nil && pending.Verifying {
		return RequestResult{Allowed: true}, 0, ErrVerificationInProgress
	}
	if len(m.pendingByHash) >= m.maxPending && !hasPrevious {
		return RequestResult{Allowed: true}, 0, ErrPendingCapacity
	}
	raw := make([]byte, 32)
	if _, err := io.ReadFull(m.random, raw); err != nil {
		return RequestResult{Allowed: true}, 0, err
	}
	hash := sha256.Sum256(raw)
	if hasPrevious {
		m.deletePendingLocked(previous)
	}
	expiresAt := now.Add(m.magicLinkTTL)
	m.pendingByHash[hash] = &PendingToken{
		Hash: hash, Email: email, RequestedIP: sourceIP,
		CreatedAt: now, ExpiresAt: expiresAt,
	}
	m.pendingByEmail[email] = hash
	return RequestResult{
		Token: base64.RawURLEncoding.EncodeToString(raw), TokenHash: hash,
		ExpiresAt: expiresAt, Allowed: true,
	}, 0, nil
}

func (m *Manager) DiscardToken(hash [32]byte) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	pending, ok := m.pendingByHash[hash]
	if !ok || pending.Verifying {
		return false
	}
	m.deletePendingLocked(hash)
	return true
}

func (m *Manager) AllowVerify(sourceIP netip.Addr) (time.Duration, bool) {
	now := m.clock.Now()
	sourceIP = sourceIP.Unmap()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupWindowsLocked(now)
	sourceKey := "source:" + sourceIP.String()
	if len(m.verifyWindows) >= m.rateEntryLimit {
		if _, exists := m.verifyWindows[sourceKey]; !exists {
			m.rateLimited.Add(1)
			return time.Minute, false
		}
	}
	retrySource, sourceAllowed := consumeWindow(m.verifyWindows, sourceKey, now, time.Minute, 30)
	retryGlobal, globalAllowed := consumeWindow(m.verifyWindows, "global", now, time.Minute, 3000)
	if !sourceAllowed || !globalAllowed {
		m.rateLimited.Add(1)
		return max(retrySource, retryGlobal), false
	}
	return 0, true
}

func (m *Manager) BeginVerify(hash [32]byte, currentIP netip.Addr) (Verification, error) {
	now := m.clock.Now()
	currentIP = currentIP.Unmap()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cleanupLocked(now)
	pending := m.pendingByHash[hash]
	if pending == nil || !now.Before(pending.ExpiresAt) {
		m.verifyFailure.Add(1)
		return Verification{}, ErrInvalidOrExpiredToken
	}
	if pending.Verifying {
		m.verifyFailure.Add(1)
		return Verification{}, ErrVerificationInProgress
	}
	if pending.RequestedIP != currentIP {
		m.sourceMismatch.Add(1)
		m.verifyFailure.Add(1)
		return Verification{}, ErrSourceIPMismatch
	}
	_, refreshed := m.validGrantLocked(currentIP, now)
	reserved := false
	if !refreshed {
		if len(m.grants)+m.reservedGrants >= m.maxGrants {
			m.verifyFailure.Add(1)
			return Verification{}, ErrGrantCapacity
		}
		m.reservedGrants++
		reserved = true
	}
	idBytes := make([]byte, 16)
	if _, err := io.ReadFull(m.random, idBytes); err != nil {
		if reserved {
			m.reservedGrants--
		}
		return Verification{}, err
	}
	id := base64.RawURLEncoding.EncodeToString(idBytes)
	pending.Verifying = true
	pending.VerificationID = id
	pending.GrantAt = now
	pending.GrantExpiresAt = now.Add(m.grantTTL)
	pending.ReservedGrant = reserved
	m.verifyingByID[id] = hash
	return Verification{
		ID: id, Email: pending.Email, RequestedIP: pending.RequestedIP,
		RequestedAt: pending.CreatedAt, GrantedAt: pending.GrantAt,
		ExpiresAt: pending.GrantExpiresAt, Refreshed: refreshed,
	}, nil
}

func (m *Manager) CommitVerify(verificationID, auditID string) (Grant, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hash, ok := m.verifyingByID[verificationID]
	if !ok {
		m.verifyFailure.Add(1)
		return Grant{}, ErrInvalidOrExpiredToken
	}
	pending := m.pendingByHash[hash]
	if pending == nil || pending.VerificationID != verificationID {
		delete(m.verifyingByID, verificationID)
		m.verifyFailure.Add(1)
		return Grant{}, ErrInvalidOrExpiredToken
	}
	grant := Grant{
		SourceIP: pending.RequestedIP, GrantedBy: pending.Email,
		GrantedAt: pending.GrantAt, ExpiresAt: pending.GrantExpiresAt, AuditID: auditID,
	}
	m.grants[grant.SourceIP] = grant
	m.deletePendingLocked(hash)
	m.verifySuccess.Add(1)
	return grant, nil
}

func (m *Manager) AbortVerify(verificationID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	hash, ok := m.verifyingByID[verificationID]
	if !ok {
		return
	}
	delete(m.verifyingByID, verificationID)
	if pending := m.pendingByHash[hash]; pending != nil && pending.VerificationID == verificationID {
		if pending.ReservedGrant {
			m.reservedGrants--
		}
		pending.Verifying = false
		pending.VerificationID = ""
		pending.GrantAt = time.Time{}
		pending.GrantExpiresAt = time.Time{}
		pending.ReservedGrant = false
	}
}

func (m *Manager) ValidGrant(sourceIP netip.Addr) (Grant, bool) {
	now := m.clock.Now()
	sourceIP = sourceIP.Unmap()
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.validGrantLocked(sourceIP, now)
}

func (m *Manager) ListGrants() []Grant {
	now := m.clock.Now()
	m.mu.Lock()
	m.cleanupLocked(now)
	result := make([]Grant, 0, len(m.grants))
	for _, grant := range m.grants {
		result = append(result, grant)
	}
	m.mu.Unlock()
	sort.Slice(result, func(left, right int) bool {
		if !result[left].ExpiresAt.Equal(result[right].ExpiresAt) {
			return result[left].ExpiresAt.Before(result[right].ExpiresAt)
		}
		return result[left].SourceIP.Less(result[right].SourceIP)
	})
	return result
}

func (m *Manager) Revoke(sourceIP netip.Addr) (Grant, bool) {
	sourceIP = sourceIP.Unmap()
	m.mu.Lock()
	grant, ok := m.grants[sourceIP]
	delete(m.grants, sourceIP)
	m.mu.Unlock()
	if ok {
		m.grantRevoked.Add(1)
	}
	return grant, ok
}

func (m *Manager) Stats() Stats {
	m.mu.Lock()
	m.cleanupLocked(m.clock.Now())
	tokens, grants := len(m.pendingByHash), len(m.grants)
	m.mu.Unlock()
	return Stats{
		ActiveTokens: tokens, ActiveGrants: grants,
		RequestRateLimited: m.rateLimited.Load(), VerifySuccess: m.verifySuccess.Load(),
		VerifyFailure: m.verifyFailure.Load(), SourceMismatch: m.sourceMismatch.Load(),
		GrantRevoked: m.grantRevoked.Load(),
	}
}

func (m *Manager) allowRequestLocked(now time.Time, email string, sourceIP netip.Addr) (time.Duration, bool) {
	m.cleanupWindowsLocked(now)
	keys := []struct {
		key   string
		limit int
	}{
		{"source:" + sourceIP.String(), m.maxRequests},
		{"email:" + email, m.maxRequests},
		{"global", m.globalLimit},
	}
	for _, item := range keys {
		if len(m.requestWindows) >= m.rateEntryLimit {
			if _, exists := m.requestWindows[item.key]; !exists {
				return time.Hour, false
			}
		}
	}
	var retry time.Duration
	allowed := true
	for _, item := range keys {
		nextRetry, nextAllowed := consumeWindow(m.requestWindows, item.key, now, time.Hour, item.limit)
		if !nextAllowed {
			allowed = false
			retry = max(retry, nextRetry)
		}
	}
	if last, ok := m.lastRequestEmail[email]; ok && now.Sub(last) < m.resendInterval {
		allowed = false
		retry = max(retry, m.resendInterval-now.Sub(last))
	}
	if allowed {
		m.lastRequestEmail[email] = now
	}
	return retry, allowed
}

func consumeWindow(windows map[string]window, key string, now time.Time, duration time.Duration, limit int) (time.Duration, bool) {
	current := windows[key]
	if current.Started.IsZero() || now.Sub(current.Started) >= duration {
		windows[key] = window{Started: now, Count: 1}
		return 0, true
	}
	if current.Count >= limit {
		return duration - now.Sub(current.Started), false
	}
	current.Count++
	windows[key] = current
	return 0, true
}

func (m *Manager) validGrantLocked(sourceIP netip.Addr, now time.Time) (Grant, bool) {
	grant, ok := m.grants[sourceIP]
	if !ok {
		return Grant{}, false
	}
	if !now.Before(grant.ExpiresAt) {
		delete(m.grants, sourceIP)
		return Grant{}, false
	}
	return grant, true
}

func (m *Manager) deletePendingLocked(hash [32]byte) {
	pending := m.pendingByHash[hash]
	if pending == nil {
		return
	}
	if pending.ReservedGrant {
		m.reservedGrants--
	}
	delete(m.pendingByHash, hash)
	if m.pendingByEmail[pending.Email] == hash {
		delete(m.pendingByEmail, pending.Email)
	}
	if pending.VerificationID != "" {
		delete(m.verifyingByID, pending.VerificationID)
	}
}

func (m *Manager) cleanupLocked(now time.Time) {
	for hash, pending := range m.pendingByHash {
		if pending.Verifying {
			continue
		}
		if !now.Before(pending.ExpiresAt) {
			m.deletePendingLocked(hash)
		}
	}
	for address, grant := range m.grants {
		if !now.Before(grant.ExpiresAt) {
			delete(m.grants, address)
		}
	}
	m.cleanupWindowsLocked(now)
}

func (m *Manager) cleanupWindowsLocked(now time.Time) {
	for key, current := range m.requestWindows {
		if now.Sub(current.Started) >= time.Hour {
			delete(m.requestWindows, key)
		}
	}
	for key, last := range m.lastRequestEmail {
		if now.Sub(last) >= time.Hour {
			delete(m.lastRequestEmail, key)
		}
	}
	for key, current := range m.verifyWindows {
		if now.Sub(current.Started) >= time.Minute {
			delete(m.verifyWindows, key)
		}
	}
}

func (m *Manager) cleanupLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			m.mu.Lock()
			m.cleanupLocked(m.clock.Now())
			m.mu.Unlock()
		case <-m.closeCh:
			return
		}
	}
}
