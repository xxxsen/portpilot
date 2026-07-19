package activity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/geoip"
	"github.com/portpilot/portpilot/internal/store"
)

var ErrLimitReached = errors.New("network activity limit reached")

const rateSampleInterval = time.Second

type Config struct {
	SampleInterval      time.Duration
	CleanupInterval     time.Duration
	AuditRetention      time.Duration
	NetworkRetention    time.Duration
	SampleRetention     time.Duration
	StatisticsRetention time.Duration
	FineRollupRetention time.Duration
	MaxActive           int
	GeoIP               *geoip.Resolver
}

type Start struct {
	Protocol     string
	ClientID     string
	ClientRemark string
	ProxyID      string
	ProxyRemark  string
	ExternalAddr string
	ServerBind   string
	TargetAddr   string
	Transport    string
	SessionEpoch uint64
}

type Metrics struct {
	FinalizeSuccess         uint64 `json:"finalize_success"`
	FinalizeFailure         uint64 `json:"finalize_failure"`
	FinalizeSkipped         uint64 `json:"finalize_skipped"`
	FinalizeDurationNanos   int64  `json:"finalize_duration_nanos"`
	BackfillConnections     uint64 `json:"backfill_connections"`
	BackfillFailures        uint64 `json:"backfill_failures"`
	UnaggregatedConnections int64  `json:"unaggregated_connections"`
	BackfillLagSeconds      int64  `json:"backfill_lag_seconds"`
	EstimatedBytes          uint64 `json:"estimated_bytes"`
	SampleOverflow          uint64 `json:"sample_overflow"`
}

type Handle struct {
	service *Service

	activityID   string
	protocol     string
	clientID     string
	clientRemark string
	proxyID      string
	proxyRemark  string
	externalAddr string
	externalIP   string
	serverBind   string
	targetAddr   string
	transport    atomic.Pointer[string]
	sessionEpoch uint64
	acceptedAt   int64

	state                atomic.Pointer[string]
	connectedAt          atomic.Int64
	lastActivityAt       atomic.Int64
	closedAt             atomic.Int64
	upBytes              atomic.Int64
	downBytes            atomic.Int64
	upPackets            atomic.Int64
	downPackets          atomic.Int64
	sampledUpBytes       atomic.Int64
	sampledDownBytes     atomic.Int64
	sampledUpPackets     atomic.Int64
	sampledDownPackets   atomic.Int64
	rateSampledUpBytes   atomic.Int64
	rateSampledDownBytes atomic.Int64
	upBytesPerSecond     atomic.Int64
	downBytesPerSecond   atomic.Int64
	rateSampledAt        atomic.Int64
	rateWindowMillis     atomic.Int64
	closeReasonCode      atomic.Pointer[string]
	closeReason          atomic.Pointer[string]
	closed               atomic.Bool
}

type writeCommand struct {
	name    string
	retries int
	run     func(context.Context) error
}

type Service struct {
	store  *store.Store
	config Config
	logger *slog.Logger

	mu                      sync.RWMutex
	active                  map[string]*Handle
	high                    chan writeCommand
	overflow                chan writeCommand
	low                     chan writeCommand
	ctx                     context.Context
	cancel                  context.CancelFunc
	wg                      sync.WaitGroup
	closed                  atomic.Bool
	degraded                atomic.Bool
	backfillDegraded        atomic.Bool
	analyticsDegraded       atomic.Bool
	finalizeSuccess         atomic.Uint64
	finalizeFailure         atomic.Uint64
	finalizeSkipped         atomic.Uint64
	finalizeDurationNanos   atomic.Int64
	backfillConnections     atomic.Uint64
	backfillFailures        atomic.Uint64
	unaggregatedConnections atomic.Int64
	backfillLagSeconds      atomic.Int64
	estimatedBytes          atomic.Uint64
	sampleOverflow          atomic.Uint64
}

func New(database *store.Store, config Config, logger *slog.Logger) (*Service, error) {
	if database == nil {
		return nil, errors.New("activity database is required")
	}
	if config.SampleInterval <= 0 || config.CleanupInterval <= 0 ||
		config.AuditRetention <= 0 || config.NetworkRetention <= 0 ||
		config.SampleRetention <= 0 || config.StatisticsRetention <= 0 ||
		config.FineRollupRetention <= 0 || config.MaxActive <= 0 {
		return nil, errors.New("invalid activity service configuration")
	}
	if logger == nil {
		logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	service := &Service{
		store: database, config: config, logger: logger,
		active:   make(map[string]*Handle),
		high:     make(chan writeCommand, 8192),
		overflow: make(chan writeCommand, 1024),
		low:      make(chan writeCommand, 32768),
		ctx:      ctx, cancel: cancel,
	}
	if err := database.EnsureAnalyticsMeta(context.Background(), time.Now().UTC().UnixMilli()); err != nil {
		cancel()
		return nil, fmt.Errorf("ensure network analytics metadata: %w", err)
	}
	service.wg.Add(5)
	go service.writer()
	go service.sampler()
	go service.rateSampler()
	go service.cleaner()
	go service.backfill()
	return service, nil
}

func (s *Service) Start(input Start) (*Handle, error) {
	if s == nil || s.closed.Load() {
		return nil, errors.New("activity service is closed")
	}
	if input.Protocol != "tcp" && input.Protocol != "udp" {
		return nil, fmt.Errorf("invalid activity protocol %q", input.Protocol)
	}
	id, err := randomID()
	if err != nil {
		return nil, fmt.Errorf("create activity id: %w", err)
	}
	now := time.Now().UTC().UnixMilli()
	externalIP := "invalid"
	if ip, ok := geoip.NormalizeExternalIP(input.ExternalAddr); ok {
		externalIP = ip.String()
	}
	handle := &Handle{
		service: s, activityID: id, protocol: input.Protocol,
		clientID: input.ClientID, clientRemark: input.ClientRemark,
		proxyID: input.ProxyID, proxyRemark: input.ProxyRemark,
		externalAddr: input.ExternalAddr, externalIP: externalIP,
		serverBind:   input.ServerBind,
		targetAddr:   input.TargetAddr,
		sessionEpoch: input.SessionEpoch, acceptedAt: now,
	}
	handle.setState("accepted")
	handle.setTransport(input.Transport)
	handle.lastActivityAt.Store(now)
	handle.rateSampledAt.Store(now)
	handle.setCloseReason("", "")

	s.mu.Lock()
	if len(s.active) >= s.config.MaxActive {
		s.mu.Unlock()
		return nil, ErrLimitReached
	}
	s.active[id] = handle
	globalPeak, clientPeak := s.peaksLocked(input.Protocol, input.ClientID)
	s.mu.Unlock()

	connection := handle.Snapshot()
	s.enqueueHigh("create network connection", func(ctx context.Context) error {
		if err := s.store.CreateNetworkConnection(ctx, connection); err != nil {
			return fmt.Errorf("create network connection: %w", err)
		}
		return nil
	})
	bucket := hourBucket(now)
	s.enqueueLow("record network connection start", func(ctx context.Context) error {
		if err := s.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
			BucketAt: bucket, ScopeClientID: "", Protocol: input.Protocol,
			ConnectionsStarted: 1, PeakActive: int64(globalPeak),
		}); err != nil {
			return fmt.Errorf("update global connection rollup: %w", err)
		}
		if err := s.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
			BucketAt: bucket, ScopeClientID: input.ClientID, Protocol: input.Protocol,
			ConnectionsStarted: 1, PeakActive: int64(clientPeak),
		}); err != nil {
			return fmt.Errorf("update client connection rollup: %w", err)
		}
		return nil
	})
	return handle, nil
}

func (s *Service) peaksLocked(protocol, clientID string) (global, client int) {
	for _, handle := range s.active {
		if handle.protocol != protocol || handle.closed.Load() {
			continue
		}
		global++
		if handle.clientID == clientID {
			client++
		}
	}
	return global, client
}

func (h *Handle) ID() string {
	if h == nil {
		return ""
	}
	return h.activityID
}

func (h *Handle) Connecting() {
	if h == nil || h.closed.Load() {
		return
	}
	h.setState("connecting")
	h.persist(false)
}

func (h *Handle) Connected() {
	if h == nil || h.closed.Load() {
		return
	}
	now := time.Now().UTC().UnixMilli()
	h.connectedAt.CompareAndSwap(0, now)
	h.lastActivityAt.Store(now)
	h.setState("active")
	h.persist(false)
}

func (h *Handle) SetTransport(value string) {
	if h == nil || h.closed.Load() {
		return
	}
	h.setTransport(value)
	h.persist(false)
}

func (h *Handle) AddUp(bytes int, packets int64) {
	if h == nil || h.closed.Load() || bytes <= 0 {
		return
	}
	h.upBytes.Add(int64(bytes))
	h.upPackets.Add(packets)
	h.lastActivityAt.Store(time.Now().UTC().UnixMilli())
}

func (h *Handle) AddDown(bytes int, packets int64) {
	if h == nil || h.closed.Load() || bytes <= 0 {
		return
	}
	h.downBytes.Add(int64(bytes))
	h.downPackets.Add(packets)
	h.lastActivityAt.Store(time.Now().UTC().UnixMilli())
}

func (h *Handle) Close(reasonCode, reason string) {
	h.finish("closed", reasonCode, reason)
}

func (h *Handle) Fail(reasonCode, reason string) {
	h.finish("failed", reasonCode, reason)
}

func (h *Handle) Interrupt(reasonCode, reason string) {
	h.finish("interrupted", reasonCode, reason)
}

func (h *Handle) finish(state, reasonCode, reason string) {
	if h == nil || !h.closed.CompareAndSwap(false, true) {
		return
	}
	now := time.Now().UTC().UnixMilli()
	h.setState("closing")
	h.closedAt.Store(now)
	h.lastActivityAt.Store(now)
	h.setCloseReason(reasonCode, reason)
	h.setState(state)
	h.service.mu.Lock()
	delete(h.service.active, h.activityID)
	h.service.mu.Unlock()
	h.service.sampleHandle(h, true)
	connection := h.Snapshot()
	h.service.enqueueHigh("finalize network connection", func(ctx context.Context) error {
		result, err := h.service.finalizeConnection(ctx, connection)
		if err != nil {
			return fmt.Errorf("finalize network connection: %w", err)
		}
		if result.SampleOverflow {
			h.service.logger.Warn(
				"network analytics samples exceeded terminal counters",
				"activity_id", connection.ActivityID,
			)
		}
		return nil
	})
	if state == "failed" {
		bucket := hourBucket(now)
		h.service.enqueueLow("record network connection failure", func(ctx context.Context) error {
			if err := h.service.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
				BucketAt: bucket, ScopeClientID: "", Protocol: h.protocol,
				ConnectionsFailed: 1,
			}); err != nil {
				return fmt.Errorf("update global failure rollup: %w", err)
			}
			if err := h.service.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
				BucketAt: bucket, ScopeClientID: h.clientID, Protocol: h.protocol,
				ConnectionsFailed: 1,
			}); err != nil {
				return fmt.Errorf("update client failure rollup: %w", err)
			}
			return nil
		})
	}
}

func (h *Handle) Snapshot() store.NetworkConnection {
	state := dereference(h.state.Load())
	reasonCode := dereference(h.closeReasonCode.Load())
	reason := dereference(h.closeReason.Load())
	connection := store.NetworkConnection{
		ActivityID: h.activityID, Protocol: h.protocol, State: state,
		ClientID: h.clientID, ClientRemark: h.clientRemark,
		ProxyID: h.proxyID, ProxyRemark: h.proxyRemark,
		ExternalAddr: h.externalAddr, ExternalIP: h.externalIP,
		ServerBind: h.serverBind,
		TargetAddr: h.targetAddr, Transport: dereference(h.transport.Load()),
		SessionEpoch: h.sessionEpoch, AcceptedAt: h.acceptedAt,
		LastActivityAt: h.lastActivityAt.Load(),
		UpBytes:        h.upBytes.Load(), DownBytes: h.downBytes.Load(),
		UpPackets: h.upPackets.Load(), DownPackets: h.downPackets.Load(),
		UpBytesPerSecond:   h.upBytesPerSecond.Load(),
		DownBytesPerSecond: h.downBytesPerSecond.Load(),
		RateSampledAt:      h.rateSampledAt.Load(),
		RateWindowMillis:   h.rateWindowMillis.Load(),
		CloseReasonCode:    reasonCode, CloseReason: reason,
	}
	if value := h.connectedAt.Load(); value > 0 {
		connection.ConnectedAt = &value
	}
	if value := h.closedAt.Load(); value > 0 {
		connection.ClosedAt = &value
	}
	return connection
}

func (h *Handle) setState(value string) {
	copy := value
	h.state.Store(&copy)
}

func (h *Handle) setTransport(value string) {
	copy := value
	h.transport.Store(&copy)
}

func (h *Handle) setCloseReason(code, reason string) {
	codeCopy := code
	reasonCopy := reason
	h.closeReasonCode.Store(&codeCopy)
	h.closeReason.Store(&reasonCopy)
}

func (h *Handle) persist(lowPriority bool) {
	connection := h.Snapshot()
	command := writeCommand{
		name: "update network connection", retries: 3,
		run: func(ctx context.Context) error {
			if err := h.service.store.UpdateNetworkConnection(ctx, connection); err != nil {
				return fmt.Errorf("persist network connection: %w", err)
			}
			return nil
		},
	}
	if lowPriority {
		h.service.enqueueLowCommand(command)
	} else {
		h.service.enqueueHighCommand(command)
	}
}

func (s *Service) Snapshot() []store.NetworkConnection {
	if s == nil {
		return nil
	}
	s.mu.RLock()
	handles := make([]*Handle, 0, len(s.active))
	for _, handle := range s.active {
		handles = append(handles, handle)
	}
	s.mu.RUnlock()
	result := make([]store.NetworkConnection, 0, len(handles))
	for _, handle := range handles {
		result = append(result, handle.Snapshot())
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].AcceptedAt == result[j].AcceptedAt {
			return result[i].ActivityID > result[j].ActivityID
		}
		return result[i].AcceptedAt > result[j].AcceptedAt
	})
	return result
}

func (s *Service) Get(id string) (store.NetworkConnection, bool) {
	s.mu.RLock()
	handle := s.active[id]
	s.mu.RUnlock()
	if handle == nil {
		return store.NetworkConnection{}, false
	}
	return handle.Snapshot(), true
}

func (s *Service) Counts() (total int, clients map[string]int) {
	clients = make(map[string]int)
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, handle := range s.active {
		if handle.closed.Load() {
			continue
		}
		total++
		clients[handle.clientID]++
	}
	return total, clients
}

func (s *Service) Degraded() bool {
	return s != nil &&
		(s.degraded.Load() || s.backfillDegraded.Load() || s.analyticsDegraded.Load())
}

func (s *Service) sampler() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.config.SampleInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			s.sampleAll()
		}
	}
}

func (s *Service) sampleAll() {
	s.mu.RLock()
	handles := make([]*Handle, 0, len(s.active))
	for _, handle := range s.active {
		handles = append(handles, handle)
	}
	peaks := make(map[string]int)
	for _, handle := range handles {
		peaks["/"+handle.protocol]++
		peaks[handle.clientID+"/"+handle.protocol]++
	}
	s.mu.RUnlock()
	for _, handle := range handles {
		s.sampleHandle(handle, false)
		handle.persist(true)
	}
	now := time.Now().UTC().UnixMilli()
	bucket := hourBucket(now)
	for key, peak := range peaks {
		scope, protocol, _ := strings.Cut(key, "/")
		s.enqueueLow("update network activity peak", func(ctx context.Context) error {
			if err := s.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
				BucketAt: bucket, ScopeClientID: scope,
				Protocol: protocol, PeakActive: int64(peak),
			}); err != nil {
				return fmt.Errorf("update network activity peak: %w", err)
			}
			return nil
		})
	}
}

func (s *Service) rateSampler() {
	defer s.wg.Done()
	ticker := time.NewTicker(rateSampleInterval)
	defer ticker.Stop()
	handles := make([]*Handle, 0)
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
			handles = s.sampleRates(handles)
		}
	}
}

func (s *Service) sampleRates(handles []*Handle) []*Handle {
	clear(handles)
	handles = handles[:0]
	s.mu.RLock()
	for _, handle := range s.active {
		handles = append(handles, handle)
	}
	s.mu.RUnlock()
	for _, handle := range handles {
		s.sampleRate(handle)
	}
	return handles
}

func (s *Service) sampleRate(handle *Handle) {
	upBytes := delta(&handle.rateSampledUpBytes, handle.upBytes.Load())
	downBytes := delta(&handle.rateSampledDownBytes, handle.downBytes.Load())
	now := time.Now().UTC().UnixMilli()
	previousRateAt := handle.rateSampledAt.Swap(now)
	rateWindow := now - previousRateAt
	if rateWindow <= 0 {
		return
	}
	handle.rateWindowMillis.Store(rateWindow)
	handle.upBytesPerSecond.Store(bytesPerSecond(upBytes, rateWindow))
	handle.downBytesPerSecond.Store(bytesPerSecond(downBytes, rateWindow))
}

func (s *Service) sampleHandle(handle *Handle, final bool) {
	upBytes := delta(&handle.sampledUpBytes, handle.upBytes.Load())
	downBytes := delta(&handle.sampledDownBytes, handle.downBytes.Load())
	upPackets := delta(&handle.sampledUpPackets, handle.upPackets.Load())
	downPackets := delta(&handle.sampledDownPackets, handle.downPackets.Load())
	now := time.Now().UTC().UnixMilli()
	if upBytes == 0 && downBytes == 0 && upPackets == 0 && downPackets == 0 && !final {
		return
	}
	bucket := sampleBucket(now, s.config.SampleInterval)
	sample := store.NetworkSample{
		ActivityID: handle.activityID, BucketAt: bucket,
		UpBytesDelta: upBytes, DownBytesDelta: downBytes,
		UpPacketsDelta: upPackets, DownPacketsDelta: downPackets,
	}
	if upBytes != 0 || downBytes != 0 || upPackets != 0 || downPackets != 0 {
		s.enqueueLow("record network sample", func(ctx context.Context) error {
			if err := s.store.UpsertNetworkSample(ctx, sample); err != nil {
				return fmt.Errorf("write network sample: %w", err)
			}
			hour := hourBucket(sample.BucketAt)
			if err := s.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
				BucketAt: hour, ScopeClientID: "", Protocol: handle.protocol,
				UpBytes: upBytes, DownBytes: downBytes,
				UpPackets: upPackets, DownPackets: downPackets,
			}); err != nil {
				return fmt.Errorf("update global traffic rollup: %w", err)
			}
			if err := s.store.UpsertHourlyRollup(ctx, store.HourlyRollup{
				BucketAt: hour, ScopeClientID: handle.clientID, Protocol: handle.protocol,
				UpBytes: upBytes, DownBytes: downBytes,
				UpPackets: upPackets, DownPackets: downPackets,
			}); err != nil {
				return fmt.Errorf("update client traffic rollup: %w", err)
			}
			return nil
		})
	}
}

func delta(previous *atomic.Int64, current int64) int64 {
	old := previous.Swap(current)
	if current < old {
		return 0
	}
	return current - old
}

func bytesPerSecond(bytes, windowMillis int64) int64 {
	if bytes <= 0 || windowMillis <= 0 {
		return 0
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	whole := bytes / windowMillis
	remainder := bytes % windowMillis
	if whole > maxInt64/1000 || remainder > maxInt64/1000 {
		return maxInt64
	}
	scaledWhole := whole * 1000
	scaledRemainder := remainder * 1000 / windowMillis
	if scaledWhole > maxInt64-scaledRemainder {
		return maxInt64
	}
	return scaledWhole + scaledRemainder
}

func sampleBucket(value int64, interval time.Duration) int64 {
	size := interval.Milliseconds()
	return value - value%size
}

func hourBucket(value int64) int64 {
	const hourMilliseconds = int64(time.Hour / time.Millisecond)
	return value - value%hourMilliseconds
}

func (s *Service) cleaner() {
	defer s.wg.Done()
	ticker := time.NewTicker(s.config.CleanupInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case now := <-ticker.C:
			s.enqueueLow("clean retained observability data", func(ctx context.Context) error {
				if err := s.store.Cleanup(
					ctx, now.UTC().UnixMilli(),
					s.config.AuditRetention, s.config.NetworkRetention, s.config.SampleRetention,
				); err != nil {
					return fmt.Errorf("clean observability data: %w", err)
				}
				if err := s.store.CleanupAnalytics(
					ctx,
					now.UTC().UnixMilli(),
					s.config.FineRollupRetention,
					s.config.StatisticsRetention,
				); err != nil {
					return fmt.Errorf("clean network analytics data: %w", err)
				}
				return nil
			})
		}
	}
}

func (s *Service) backfill() {
	defer s.wg.Done()
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	for {
		if err := s.backfillBatch(); err != nil {
			if s.ctx.Err() != nil {
				return
			}
			s.backfillDegraded.Store(true)
			s.logger.Error("network analytics backfill failed", "error", err)
		} else {
			s.backfillDegraded.Store(false)
		}
		select {
		case <-s.ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (s *Service) backfillBatch() error {
	ctx, cancel := context.WithTimeout(s.ctx, 15*time.Second)
	defer cancel()
	now := time.Now().UTC().UnixMilli()
	connections, err := s.store.ListUnaggregatedConnections(ctx, 500)
	if err != nil {
		_ = s.store.UpdateAnalyticsMeta(ctx, "degraded", 0, 0, err.Error(), now)
		return fmt.Errorf("list unaggregated network connections: %w", err)
	}
	if len(connections) > 0 {
		if err := s.store.UpdateAnalyticsMeta(ctx, "backfilling", 0, 0, "", now); err != nil {
			return fmt.Errorf("mark network analytics backfill active: %w", err)
		}
	}
	var processed int64
	var completeFrom int64
	for index := range connections {
		connection := connections[index]
		if connection.ExternalIP == "" {
			if ip, ok := geoip.NormalizeExternalIP(connection.ExternalAddr); ok {
				connection.ExternalIP = ip.String()
			} else {
				connection.ExternalIP = "invalid"
			}
		}
		result, aggregateErr := s.finalizeConnection(ctx, connection)
		if aggregateErr != nil {
			s.backfillFailures.Add(1)
			_ = s.store.UpdateAnalyticsMeta(
				ctx,
				"degraded",
				completeFrom,
				processed,
				aggregateErr.Error(),
				time.Now().UTC().UnixMilli(),
			)
			return aggregateErr
		}
		if result.Aggregated {
			processed++
			completeFrom = connection.AcceptedAt
			s.backfillConnections.Add(1)
		}
		runtime.Gosched()
	}
	pending, oldest, err := s.store.AnalyticsBackfillState(ctx)
	if err != nil {
		return fmt.Errorf("read network analytics backfill state: %w", err)
	}
	s.unaggregatedConnections.Store(pending)
	if pending > 0 && oldest > 0 {
		s.backfillLagSeconds.Store(max((now-oldest)/1000, 0))
	} else {
		s.backfillLagSeconds.Store(0)
	}
	status := "backfilling"
	if pending == 0 {
		status = "ready"
		completeFrom = now - s.config.StatisticsRetention.Milliseconds()
	}
	err = s.store.UpdateAnalyticsMeta(
		ctx,
		status,
		completeFrom,
		processed,
		"",
		time.Now().UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("update network analytics backfill metadata: %w", err)
	}
	return nil
}

func (s *Service) geoClassification(connection store.NetworkConnection) store.GeoClassification {
	result := geoip.Result{Kind: "invalid"}
	if connection.ExternalIP != "" && connection.ExternalIP != "invalid" {
		if ip, ok := geoip.NormalizeExternalIP(connection.ExternalIP); ok {
			if s.config.GeoIP != nil {
				result = s.config.GeoIP.Resolve(ip)
			} else {
				result = geoip.Result{Kind: "unknown"}
			}
		}
	}
	return store.GeoClassification{
		Kind: result.Kind, CountryCode: result.CountryCode,
		DatabaseFingerprint: result.DatabaseFingerprint,
	}
}

func (s *Service) finalizeConnection(
	ctx context.Context,
	connection store.NetworkConnection,
) (store.AnalyticsFinalizeResult, error) {
	started := time.Now()
	result, err := s.store.FinalizeNetworkConnectionAndAggregate(
		ctx,
		connection,
		s.geoClassification(connection),
		time.Now().UTC().UnixMilli(),
	)
	s.finalizeDurationNanos.Add(time.Since(started).Nanoseconds())
	if err != nil {
		s.finalizeFailure.Add(1)
		return store.AnalyticsFinalizeResult{}, fmt.Errorf("finalize network analytics: %w", err)
	}
	if result.Aggregated {
		s.finalizeSuccess.Add(1)
	} else {
		s.finalizeSkipped.Add(1)
	}
	if result.EstimatedBytes > 0 {
		s.estimatedBytes.Add(uint64(result.EstimatedBytes))
	}
	if result.SampleOverflow {
		s.sampleOverflow.Add(1)
		s.analyticsDegraded.Store(true)
	}
	return result, nil
}

func (s *Service) AnalyticsMeta(ctx context.Context) (store.AnalyticsMeta, error) {
	if s == nil {
		return store.AnalyticsMeta{}, errors.New("activity service is unavailable")
	}
	result, err := s.store.AnalyticsMeta(ctx)
	if err == nil && s.analyticsDegraded.Load() {
		result.Status = "degraded"
	}
	if err != nil {
		return store.AnalyticsMeta{}, fmt.Errorf("read network analytics metadata: %w", err)
	}
	return result, nil
}

func (s *Service) Metrics() Metrics {
	if s == nil {
		return Metrics{}
	}
	return Metrics{
		FinalizeSuccess:         s.finalizeSuccess.Load(),
		FinalizeFailure:         s.finalizeFailure.Load(),
		FinalizeSkipped:         s.finalizeSkipped.Load(),
		FinalizeDurationNanos:   s.finalizeDurationNanos.Load(),
		BackfillConnections:     s.backfillConnections.Load(),
		BackfillFailures:        s.backfillFailures.Load(),
		UnaggregatedConnections: s.unaggregatedConnections.Load(),
		BackfillLagSeconds:      s.backfillLagSeconds.Load(),
		EstimatedBytes:          s.estimatedBytes.Load(),
		SampleOverflow:          s.sampleOverflow.Load(),
	}
}

func (s *Service) enqueueHigh(name string, run func(context.Context) error) {
	s.enqueueHighCommand(writeCommand{name: name, retries: 3, run: run})
}

func (s *Service) enqueueLow(name string, run func(context.Context) error) {
	s.enqueueLowCommand(writeCommand{name: name, run: run})
}

func (s *Service) enqueueHighCommand(command writeCommand) {
	select {
	case s.high <- command:
	default:
		select {
		case s.overflow <- command:
			s.degraded.Store(true)
			s.logger.Warn("activity high-priority overflow queue used", "operation", command.name)
		default:
			s.degraded.Store(true)
			s.logger.Error("activity high-priority queues full", "operation", command.name)
		}
	}
}

func (s *Service) enqueueLowCommand(command writeCommand) {
	select {
	case s.low <- command:
	default:
		s.degraded.Store(true)
		s.logger.Warn("activity sample queue full", "operation", command.name)
	}
}

func (s *Service) writer() {
	defer s.wg.Done()
	run := func(command writeCommand) {
		attempts := command.retries + 1
		for attempt := 1; attempt <= attempts; attempt++ {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := command.run(ctx)
			cancel()
			if err == nil {
				s.degraded.Store(false)
				return
			}
			if attempt == attempts {
				s.degraded.Store(true)
				s.logger.Error("activity persistence failed", "operation", command.name, "attempts", attempts, "error", err)
				return
			}
			time.Sleep(time.Duration(attempt) * 25 * time.Millisecond)
		}
	}
	for {
		select {
		case command := <-s.high:
			run(command)
			continue
		default:
		}
		select {
		case command := <-s.overflow:
			run(command)
			continue
		default:
		}
		select {
		case command := <-s.high:
			run(command)
		case command := <-s.overflow:
			run(command)
		case command := <-s.low:
			run(command)
		case <-s.ctx.Done():
			for {
				select {
				case command := <-s.high:
					run(command)
				case command := <-s.overflow:
					run(command)
				case command := <-s.low:
					run(command)
				default:
					return
				}
			}
		}
	}
}

func (s *Service) Close() {
	if s == nil || !s.closed.CompareAndSwap(false, true) {
		return
	}
	s.mu.RLock()
	handles := make([]*Handle, 0, len(s.active))
	for _, handle := range s.active {
		handles = append(handles, handle)
	}
	s.mu.RUnlock()
	for _, handle := range handles {
		handle.Interrupt("server_stop", "server stopped")
	}
	s.cancel()
	s.wg.Wait()
}

func randomID() (string, error) {
	value := make([]byte, 16)
	if _, err := rand.Read(value); err != nil {
		return "", err
	}
	return hex.EncodeToString(value), nil
}

func dereference(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
