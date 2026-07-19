package activity

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/store"
)

var ErrLimitReached = errors.New("network activity limit reached")

type Config struct {
	SampleInterval   time.Duration
	CleanupInterval  time.Duration
	AuditRetention   time.Duration
	NetworkRetention time.Duration
	SampleRetention  time.Duration
	MaxActive        int
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

type Handle struct {
	service *Service

	activityID   string
	protocol     string
	clientID     string
	clientRemark string
	proxyID      string
	proxyRemark  string
	externalAddr string
	serverBind   string
	targetAddr   string
	transport    atomic.Pointer[string]
	sessionEpoch uint64
	acceptedAt   int64

	state              atomic.Pointer[string]
	connectedAt        atomic.Int64
	lastActivityAt     atomic.Int64
	closedAt           atomic.Int64
	upBytes            atomic.Int64
	downBytes          atomic.Int64
	upPackets          atomic.Int64
	downPackets        atomic.Int64
	sampledUpBytes     atomic.Int64
	sampledDownBytes   atomic.Int64
	sampledUpPackets   atomic.Int64
	sampledDownPackets atomic.Int64
	closeReasonCode    atomic.Pointer[string]
	closeReason        atomic.Pointer[string]
	closed             atomic.Bool
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

	mu       sync.RWMutex
	active   map[string]*Handle
	high     chan writeCommand
	overflow chan writeCommand
	low      chan writeCommand
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	closed   atomic.Bool
	degraded atomic.Bool
}

func New(database *store.Store, config Config, logger *slog.Logger) (*Service, error) {
	if database == nil {
		return nil, errors.New("activity database is required")
	}
	if config.SampleInterval <= 0 || config.CleanupInterval <= 0 ||
		config.AuditRetention <= 0 || config.NetworkRetention <= 0 ||
		config.SampleRetention <= 0 || config.MaxActive <= 0 {
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
	service.wg.Add(3)
	go service.writer()
	go service.sampler()
	go service.cleaner()
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
	handle := &Handle{
		service: s, activityID: id, protocol: input.Protocol,
		clientID: input.ClientID, clientRemark: input.ClientRemark,
		proxyID: input.ProxyID, proxyRemark: input.ProxyRemark,
		externalAddr: input.ExternalAddr, serverBind: input.ServerBind,
		targetAddr:   input.TargetAddr,
		sessionEpoch: input.SessionEpoch, acceptedAt: now,
	}
	handle.setState("accepted")
	handle.setTransport(input.Transport)
	handle.lastActivityAt.Store(now)
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
		if err := h.service.store.UpdateNetworkConnection(ctx, connection); err != nil {
			return fmt.Errorf("finalize network connection: %w", err)
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
		ExternalAddr: h.externalAddr, ServerBind: h.serverBind,
		TargetAddr: h.targetAddr, Transport: dereference(h.transport.Load()),
		SessionEpoch: h.sessionEpoch, AcceptedAt: h.acceptedAt,
		LastActivityAt: h.lastActivityAt.Load(),
		UpBytes:        h.upBytes.Load(), DownBytes: h.downBytes.Load(),
		UpPackets: h.upPackets.Load(), DownPackets: h.downPackets.Load(),
		CloseReasonCode: reasonCode, CloseReason: reason,
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
	return s != nil && s.degraded.Load()
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

func (s *Service) sampleHandle(handle *Handle, final bool) {
	upBytes := delta(&handle.sampledUpBytes, handle.upBytes.Load())
	downBytes := delta(&handle.sampledDownBytes, handle.downBytes.Load())
	upPackets := delta(&handle.sampledUpPackets, handle.upPackets.Load())
	downPackets := delta(&handle.sampledDownPackets, handle.downPackets.Load())
	if upBytes == 0 && downBytes == 0 && upPackets == 0 && downPackets == 0 && !final {
		return
	}
	now := time.Now().UTC().UnixMilli()
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
				return nil
			})
		}
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
