package geoip

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
	"github.com/xxxsen/common/logutil"
	"go.uber.org/zap"
)

const defaultCacheCapacity = 65536

type Result struct {
	Kind                string
	CountryCode         string
	DatabaseFingerprint string
}

type Status struct {
	Status              string `json:"status"`
	SourceType          string `json:"source_type"`
	DatabaseFingerprint string `json:"database_fingerprint"`
	LastReloadAt        int64  `json:"last_reload_at"`
	LastCheckedAt       int64  `json:"last_checked_at"`
	LastDownloadedAt    int64  `json:"last_downloaded_at"`
	NextCheckAt         int64  `json:"next_check_at"`
	Stale               bool   `json:"stale"`
	UpdateState         string `json:"update_state"`
	LastErrorCode       string `json:"last_error_code"`
	LastError           string `json:"-"`
}

type Metrics struct {
	Check          uint64 `json:"check"`
	NotModified    uint64 `json:"not_modified"`
	Download       uint64 `json:"download"`
	DownloadBytes  uint64 `json:"download_bytes"`
	FormatRejected uint64 `json:"format_rejected"`
	ValidationFail uint64 `json:"validation_failure"`
	InstallSuccess uint64 `json:"install_success"`
	Retry          uint64 `json:"retry"`
	LookupCountry  uint64 `json:"lookup_country"`
	LookupPrivate  uint64 `json:"lookup_private"`
	LookupReserved uint64 `json:"lookup_reserved"`
	LookupUnknown  uint64 `json:"lookup_unknown"`
	LookupInvalid  uint64 `json:"lookup_invalid"`
	ReloadSuccess  uint64 `json:"reload_success"`
	ReloadFailure  uint64 `json:"reload_failure"`
}

type cacheEntry struct {
	key    string
	result Result
}

type readerState struct {
	reader      *maxminddb.Reader
	fingerprint string
	references  atomic.Int64
	retiring    atomic.Bool
	closed      atomic.Bool
	done        chan struct{}
}

type Resolver struct {
	updater        *updater
	path           string
	sourceType     SourceType
	reloadInterval time.Duration
	logger         *zap.Logger

	reader atomic.Pointer[readerState]

	cacheMu       sync.Mutex
	cache         map[string]*list.Element
	cacheOrder    *list.List
	cacheCapacity int

	ctx               context.Context
	cancel            context.CancelFunc
	wg                sync.WaitGroup
	lastReloadAt      atomic.Int64
	lastCheckedAt     atomic.Int64
	lastDownloadedAt  atomic.Int64
	nextCheckAt       atomic.Int64
	updateState       atomic.Pointer[string]
	lastErrorCode     atomic.Pointer[string]
	lastError         atomic.Pointer[string]
	checkCount        atomic.Uint64
	notModified       atomic.Uint64
	downloadCount     atomic.Uint64
	downloadBytes     atomic.Uint64
	formatRejected    atomic.Uint64
	validationFailure atomic.Uint64
	installSuccess    atomic.Uint64
	retryCount        atomic.Uint64
	lookupCountry     atomic.Uint64
	lookupPrivate     atomic.Uint64
	lookupReserved    atomic.Uint64
	lookupUnknown     atomic.Uint64
	lookupInvalid     atomic.Uint64
	reloadSuccess     atomic.Uint64
	reloadFailure     atomic.Uint64
}

func New(path string, reloadInterval time.Duration, logger *zap.Logger) (*Resolver, error) {
	if reloadInterval <= 0 {
		return nil, errors.New("geoip reload interval must be positive")
	}
	if logger == nil {
		logger = logutil.GetLogger(context.Background())
	}
	ctx, cancel := context.WithCancel(context.Background())
	resolver := &Resolver{
		path: path, reloadInterval: reloadInterval, logger: logger,
		cache: make(map[string]*list.Element), cacheOrder: list.New(),
		cacheCapacity: defaultCacheCapacity, ctx: ctx, cancel: cancel,
		sourceType: SourceDisabled,
	}
	if path == "" {
		resolver.setUpdateState("idle")
		return resolver, nil
	}
	resolver.sourceType = SourceLocal
	resolver.setUpdateState("idle")
	if err := resolver.reload(true); err != nil {
		cancel()
		return nil, err
	}
	resolver.wg.Add(1)
	go resolver.reloader()
	return resolver, nil
}

func (r *Resolver) ResolveExternalAddress(address string) (string, Result) {
	ip, ok := NormalizeExternalIP(address)
	if !ok {
		r.recordLookup("invalid")
		return "", Result{Kind: "invalid"}
	}
	return ip.String(), r.Resolve(ip)
}

func (r *Resolver) Resolve(ip netip.Addr) Result {
	ip = normalizeIP(ip)
	if result, ok := classifySpecial(ip); ok {
		r.recordLookup(result.Kind)
		return result
	}
	state := r.acquireReader()
	if state == nil {
		r.recordLookup("unknown")
		return Result{Kind: "unknown"}
	}
	defer state.release()
	fingerprint := state.fingerprint
	cacheKey := fingerprint + "/" + ip.String()
	if result, ok := r.cacheGet(cacheKey); ok {
		r.recordLookup(result.Kind)
		return result
	}
	var record struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	err := state.reader.Lookup(ip).Decode(&record)
	result := Result{Kind: "unknown", DatabaseFingerprint: fingerprint}
	if err == nil {
		code := strings.ToUpper(strings.TrimSpace(record.Country.ISOCode))
		if len(code) == 2 {
			result.Kind = "country"
			result.CountryCode = code
		}
	}
	if err != nil {
		r.setError(fmt.Errorf("lookup geoip address: %w", err))
	}
	r.cachePut(cacheKey, result)
	r.recordLookup(result.Kind)
	return result
}

func (r *Resolver) Status() Status {
	if r == nil {
		return Status{Status: "disabled", SourceType: string(SourceDisabled), UpdateState: "idle"}
	}
	state := r.reader.Load()
	fingerprint := ""
	if state != nil {
		fingerprint = state.fingerprint
	}
	status := "ready"
	if r.sourceType == SourceDisabled {
		status = "disabled"
	} else if state == nil || r.lastError.Load() != nil {
		status = "degraded"
	}
	checkedAt := r.lastCheckedAt.Load()
	stale := r.sourceType == SourceRemote && checkedAt > 0 &&
		time.Since(time.UnixMilli(checkedAt)) > 8*24*time.Hour
	if stale {
		status = "degraded"
	}
	result := Status{
		Status: status, SourceType: string(r.sourceType), DatabaseFingerprint: fingerprint,
		LastReloadAt: r.lastReloadAt.Load(), LastCheckedAt: checkedAt,
		LastDownloadedAt: r.lastDownloadedAt.Load(), NextCheckAt: r.nextCheckAt.Load(),
		Stale: stale, UpdateState: r.currentUpdateState(),
	}
	if message := r.lastError.Load(); message != nil {
		result.LastError = *message
	}
	if code := r.lastErrorCode.Load(); code != nil {
		result.LastErrorCode = *code
	}
	return result
}

func (r *Resolver) Degraded() bool {
	return r != nil && r.path != "" && r.Status().Status == "degraded"
}

func (r *Resolver) Close() {
	if r == nil {
		return
	}
	r.cancel()
	r.wg.Wait()
	state := r.reader.Swap(nil)
	if state != nil {
		state.retire()
		<-state.done
	}
}

func (r *Resolver) reloader() {
	defer r.wg.Done()
	ticker := time.NewTicker(r.reloadInterval)
	defer ticker.Stop()
	for {
		select {
		case <-r.ctx.Done():
			return
		case <-ticker.C:
			if err := r.reload(false); err != nil {
				r.reloadFailure.Add(1)
				r.setError(err)
				r.logger.Warn("geoip database reload failed", zap.Error(err))
			}
		}
	}
}

func (r *Resolver) reload(required bool) error {
	fingerprint, err := fingerprintFile(r.path)
	if err != nil {
		return fmt.Errorf("fingerprint geoip country database: %w", err)
	}
	current := r.reader.Load()
	unchanged := current != nil && fingerprint == current.fingerprint
	if unchanged {
		r.lastReloadAt.Store(time.Now().UTC().UnixMilli())
		r.lastError.Store(nil)
		r.reloadSuccess.Add(1)
		return nil
	}
	reader, err := maxminddb.Open(r.path)
	if err != nil {
		return fmt.Errorf("open geoip country database: %w", err)
	}
	databaseType := strings.ToLower(reader.Metadata.DatabaseType)
	if !strings.Contains(databaseType, "country") && !strings.Contains(databaseType, "city") {
		_ = reader.Close()
		return fmt.Errorf("geoip database type %q does not provide country data", reader.Metadata.DatabaseType)
	}
	next := &readerState{
		reader: reader, fingerprint: fingerprint, done: make(chan struct{}),
	}
	previous := r.reader.Swap(next)
	r.clearCache()
	if previous != nil {
		previous.retire()
	}
	r.lastReloadAt.Store(time.Now().UTC().UnixMilli())
	r.lastError.Store(nil)
	r.lastErrorCode.Store(nil)
	r.reloadSuccess.Add(1)
	if !required {
		r.logger.Info(
			"geoip country database reloaded",
			zap.String("fingerprint", fingerprint),
		)
	}
	return nil
}

func (r *Resolver) acquireReader() *readerState {
	for {
		state := r.reader.Load()
		if state == nil {
			return nil
		}
		state.references.Add(1)
		if r.reader.Load() == state && !state.retiring.Load() {
			return state
		}
		state.release()
	}
}

func (s *readerState) release() {
	if s.references.Add(-1) == 0 {
		s.closeIfRetired()
	}
}

func (s *readerState) retire() {
	s.retiring.Store(true)
	s.closeIfRetired()
}

func (s *readerState) closeIfRetired() {
	if !s.retiring.Load() || s.references.Load() != 0 ||
		!s.closed.CompareAndSwap(false, true) {
		return
	}
	_ = s.reader.Close()
	close(s.done)
}

func (r *Resolver) setError(err error) {
	r.setErrorCode("geoip_error", err)
}

func (r *Resolver) setErrorCode(code string, err error) {
	message := err.Error()
	r.lastError.Store(&message)
	r.lastErrorCode.Store(&code)
}

func (r *Resolver) setUpdateState(value string) {
	r.updateState.Store(&value)
}

func (r *Resolver) currentUpdateState() string {
	if value := r.updateState.Load(); value != nil {
		return *value
	}
	return "idle"
}

func (r *Resolver) recordLookup(kind string) {
	switch kind {
	case "country":
		r.lookupCountry.Add(1)
	case "private":
		r.lookupPrivate.Add(1)
	case "reserved":
		r.lookupReserved.Add(1)
	case "invalid":
		r.lookupInvalid.Add(1)
	default:
		r.lookupUnknown.Add(1)
	}
}

func (r *Resolver) Metrics() Metrics {
	if r == nil {
		return Metrics{}
	}
	return Metrics{
		Check: r.checkCount.Load(), NotModified: r.notModified.Load(),
		Download: r.downloadCount.Load(), DownloadBytes: r.downloadBytes.Load(),
		FormatRejected: r.formatRejected.Load(), ValidationFail: r.validationFailure.Load(),
		InstallSuccess: r.installSuccess.Load(), Retry: r.retryCount.Load(),
		LookupCountry:  r.lookupCountry.Load(),
		LookupPrivate:  r.lookupPrivate.Load(),
		LookupReserved: r.lookupReserved.Load(),
		LookupUnknown:  r.lookupUnknown.Load(),
		LookupInvalid:  r.lookupInvalid.Load(),
		ReloadSuccess:  r.reloadSuccess.Load(),
		ReloadFailure:  r.reloadFailure.Load(),
	}
}

func (r *Resolver) cacheGet(key string) (Result, bool) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	element := r.cache[key]
	if element == nil {
		return Result{}, false
	}
	r.cacheOrder.MoveToFront(element)
	return element.Value.(cacheEntry).result, true
}

func (r *Resolver) cachePut(key string, result Result) {
	r.cacheMu.Lock()
	defer r.cacheMu.Unlock()
	if element := r.cache[key]; element != nil {
		element.Value = cacheEntry{key: key, result: result}
		r.cacheOrder.MoveToFront(element)
		return
	}
	element := r.cacheOrder.PushFront(cacheEntry{key: key, result: result})
	r.cache[key] = element
	if r.cacheOrder.Len() <= r.cacheCapacity {
		return
	}
	oldest := r.cacheOrder.Back()
	delete(r.cache, oldest.Value.(cacheEntry).key)
	r.cacheOrder.Remove(oldest)
}

func (r *Resolver) clearCache() {
	r.cacheMu.Lock()
	r.cache = make(map[string]*list.Element)
	r.cacheOrder.Init()
	r.cacheMu.Unlock()
}

func NormalizeExternalIP(address string) (netip.Addr, bool) {
	value := strings.TrimSpace(address)
	if value == "" {
		return netip.Addr{}, false
	}
	if addrPort, err := netip.ParseAddrPort(value); err == nil {
		return normalizeIP(addrPort.Addr()), true
	}
	host, _, err := net.SplitHostPort(value)
	if err == nil {
		host = strings.Trim(host, "[]")
		if zone := strings.LastIndexByte(host, '%'); zone >= 0 {
			host = host[:zone]
		}
		value = host
	}
	ip, err := netip.ParseAddr(strings.Trim(value, "[]"))
	if err != nil {
		return netip.Addr{}, false
	}
	return normalizeIP(ip), true
}

func normalizeIP(ip netip.Addr) netip.Addr {
	if ip.Zone() != "" {
		ip = ip.WithZone("")
	}
	return ip.Unmap()
}

var reservedPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("2001:db8::/32"),
}

func classifySpecial(ip netip.Addr) (Result, bool) {
	if !ip.IsValid() {
		return Result{Kind: "invalid"}, true
	}
	if ip.IsLoopback() || ip.IsPrivate() {
		return Result{Kind: "private"}, true
	}
	if ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return Result{Kind: "reserved"}, true
	}
	for _, prefix := range reservedPrefixes {
		if prefix.Contains(ip) {
			return Result{Kind: "reserved"}, true
		}
	}
	return Result{}, false
}

func fingerprintFile(path string) (string, error) {
	//nolint:gosec // The path is supplied by trusted server configuration and opened read-only.
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open geoip database for fingerprint: %w", err)
	}
	defer func() { _ = file.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", fmt.Errorf("hash geoip database: %w", err)
	}
	return hex.EncodeToString(hash.Sum(nil))[:16], nil
}
