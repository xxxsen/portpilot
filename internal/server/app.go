package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/accesspolicy"
	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/auth"
	cachelayout "github.com/portpilot/portpilot/internal/cache"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/geoip"
	"github.com/portpilot/portpilot/internal/store"
	"github.com/portpilot/portpilot/internal/version"
	"github.com/xxxsen/common/logutil"
	"go.uber.org/zap"
)

type App struct {
	Config            config.ServerConfig
	Cache             *cachelayout.Root
	ConfigManager     *config.Manager
	Sessions          *SessionManager
	Tickets           *auth.TicketStore
	Challenges        *auth.ChallengeStore
	Tunnels           *TunnelManager
	UDP               *UDPManager
	Listeners         *ListenerManager
	Events            *store.Store
	Activity          *activity.Service
	GeoIP             *geoip.Resolver
	ProxyStatus       *ProxyStatusService
	Attention         *AttentionService
	Admin             *AdminAuth
	ControlLimiter    *auth.RateLimiter
	DataLimiter       *auth.RateLimiter
	RuntimeLogLimiter *auth.RateLimiter
	GlobalAccess      accesspolicy.Policy
	Logger            *zap.Logger

	adminServer *http.Server
	dataServer  *http.Server
	analytics   analyticsQueryMetrics
	access      externalAccessMetrics

	analyticsTimeout  time.Duration
	storeError        atomic.Bool
	ready             atomic.Bool
	closeOnce         sync.Once
	searchLimiterOnce sync.Once
	searchLimiter     *searchRateLimiter
	searchStats       searchQueryMetrics
	exportOnce        sync.Once
	exports           *exportManager
	agentSnapshots    chan store.ClientAgentSnapshot
	agentWriterWG     sync.WaitGroup
	agentWriterClose  sync.Once
}

func New(configPath string, logger *zap.Logger) (*App, error) {
	cfg, err := config.LoadServer(configPath)
	if err != nil {
		return nil, fmt.Errorf("load server configuration: %w", err)
	}
	return NewFromConfig(cfg, logger)
}

func NewFromConfig(cfg config.ServerConfig, logger *zap.Logger) (*App, error) {
	if logger == nil {
		logger = logutil.GetLogger(context.Background())
	}
	configManager, err := config.NewManager(
		cfg.ClientConfigFile, cfg.AdminAddr, cfg.ServerAddr,
	)
	if err != nil {
		return nil, fmt.Errorf("load client proxy configuration: %w", err)
	}
	cacheRoot, err := cachelayout.Open(cfg.CacheDir)
	if err != nil {
		return nil, fmt.Errorf("initialize cache directory: %w", err)
	}
	keepCache := false
	defer func() {
		if !keepCache {
			_ = cacheRoot.Close()
		}
	}()
	logger.Info("cache initialized", zap.String("cache_mode", cacheRoot.Mode()))
	app := &App{
		Config: cfg, ConfigManager: configManager, Cache: cacheRoot, Logger: logger,
		Tickets:           auth.NewTicketStore(cfg.Duration(cfg.Timeouts.DataConnect)),
		Challenges:        auth.NewChallengeStore(cfg.Duration(cfg.Timeouts.ControlHandshake)),
		Admin:             NewAdminAuth(cfg.Users, cfg.Limits.MaxAdminLoginAttemptsPerMinute),
		ControlLimiter:    auth.NewRateLimiter(60, time.Minute),
		DataLimiter:       auth.NewRateLimiter(cfg.Limits.MaxDataHandshakesPerMinute, time.Minute),
		RuntimeLogLimiter: auth.NewRateLimiter(1, time.Minute),
		GlobalAccess:      accesspolicy.Compile(cfg.AccessPolicy),
	}
	app.Sessions = NewSessionManager(app)
	app.Tunnels = NewTunnelManager(app)
	app.UDP = NewUDPManager(app)
	app.Listeners = NewListenerManager(app)

	eventStore, err := store.Open(cacheRoot.DatabasePath(), logger)
	if err != nil {
		return nil, fmt.Errorf("open observability database: %w", err)
	}
	app.Events = eventStore
	if _, err := eventStore.InterruptOpenConnections(
		context.Background(), time.Now().UTC().UnixMilli(),
	); err != nil {
		_ = eventStore.Close()
		return nil, fmt.Errorf("recover network activity: %w", err)
	}
	if _, err := eventStore.ResolvePendingAudits(
		context.Background(), configManager.Snapshot().Revision,
	); err != nil {
		_ = eventStore.Close()
		return nil, fmt.Errorf("recover pending configuration audits: %w", err)
	}
	metadataMigration, err := configManager.EnsureMetadata()
	if err != nil {
		_ = eventStore.Close()
		return nil, fmt.Errorf("migrate client metadata: %w", err)
	}
	if len(metadataMigration.ClientIDs) > 0 {
		if _, err := eventStore.WriteAudit(context.Background(), store.AuditEvent{
			Category: "config", EventType: "config_metadata_migrated",
			Outcome: "success", ActorType: "system",
			Summary: "补全旧客户端配置元数据",
			Details: map[string]any{
				"client_ids":  metadataMigration.ClientIDs,
				"time_source": metadataMigration.TimeSource,
				"revision":    metadataMigration.Revision,
			},
		}); err != nil {
			_ = eventStore.Close()
			return nil, fmt.Errorf("audit client metadata migration: %w", err)
		}
	}
	geoResolver, err := geoip.NewManaged(
		cfg.Observability.GeoIPCountrySource,
		cacheRoot.GeoIPDatabasePath(),
		cacheRoot.GeoIPMetadataPath(),
		logger,
		geoip.ManagedOptions{},
	)
	if err != nil {
		_ = eventStore.Close()
		return nil, fmt.Errorf("initialize geoip resolver: %w", err)
	}
	app.GeoIP = geoResolver
	activityService, err := activity.New(eventStore, activity.Config{
		SampleInterval:      cfg.Duration(cfg.Observability.SampleInterval),
		CleanupInterval:     cfg.Duration(cfg.Observability.CleanupInterval),
		AuditRetention:      cfg.Duration(cfg.Observability.AuditRetention),
		NetworkRetention:    cfg.Duration(cfg.Observability.NetworkRetention),
		SampleRetention:     cfg.Duration(cfg.Observability.NetworkSampleRetention),
		StatisticsRetention: cfg.Duration(cfg.Observability.NetworkStatisticsRetention),
		FineRollupRetention: cfg.Duration(cfg.Observability.NetworkFineRollupRetention),
		MaxActive:           cfg.Observability.MaxActiveConnections,
		GeoIP:               geoResolver,
	}, logger)
	if err != nil {
		geoResolver.Close()
		_ = eventStore.Close()
		return nil, fmt.Errorf("initialize network activity: %w", err)
	}
	app.Activity = activityService
	app.ProxyStatus = NewProxyStatusService(app)
	app.Attention = NewAttentionService(app)
	commit, _, err := app.Listeners.Prepare(configManager.Snapshot())
	if err != nil {
		activityService.Close()
		geoResolver.Close()
		if eventStore != nil {
			_ = eventStore.Close()
		}
		return nil, fmt.Errorf("prepare proxy listeners: %w", err)
	}
	commit()
	schemaVersion, err := eventStore.SchemaVersion(context.Background())
	if err != nil {
		activityService.Close()
		geoResolver.Close()
		_ = eventStore.Close()
		return nil, fmt.Errorf("read migration version: %w", err)
	}
	if _, err := eventStore.WriteAudit(context.Background(), store.AuditEvent{
		Category: "system", EventType: "system_start", Outcome: "success",
		ActorType: "system", Summary: "PortPilot 服务启动",
		Details: map[string]any{
			"version": version.String(), "migration_version": schemaVersion,
		},
	}); err != nil {
		activityService.Close()
		geoResolver.Close()
		_ = eventStore.Close()
		return nil, fmt.Errorf("audit system start: %w", err)
	}

	app.adminServer = &http.Server{
		Addr: cfg.AdminAddr, Handler: app.adminMux(),
		ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second,
	}
	app.dataServer = &http.Server{
		Addr: cfg.ServerAddr, Handler: http.HandlerFunc(app.serveData),
		ReadHeaderTimeout: cfg.Duration(cfg.Timeouts.ControlHandshake),
		IdleTimeout:       0,
	}
	app.startAgentWriter()
	keepCache = true
	return app, nil
}

func (a *App) Run(ctx context.Context) error {
	errorsCh := make(chan error, 2)
	go func() {
		a.Logger.Info("admin server listening", zap.String("address", a.Config.AdminAddr))
		if err := a.adminServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsCh <- fmt.Errorf("admin server: %w", err)
		}
	}()
	go func() {
		a.Logger.Info("control/data server listening", zap.String("address", a.Config.ServerAddr))
		if err := a.dataServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsCh <- fmt.Errorf("control/data server: %w", err)
		}
	}()
	a.ready.Store(true)
	select {
	case <-ctx.Done():
		return a.Close(ctx)
	case err := <-errorsCh:
		_ = a.Close(ctx)
		return err
	}
}

func (a *App) Close(parent context.Context) error {
	var result error
	a.closeOnce.Do(func() {
		a.ready.Store(false)
		ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 10*time.Second)
		defer cancel()
		a.Sessions.CloseAll()
		if a.exports != nil {
			result = errors.Join(result, a.exports.close(ctx))
		}
		a.Listeners.Close()
		a.UDP.Close()
		a.Activity.Close()
		a.GeoIP.Close()
		adminErr := a.adminServer.Shutdown(ctx)
		dataErr := a.dataServer.Shutdown(ctx)
		a.writeAuditBestEffort(ctx, store.AuditEvent{
			Category: "system", EventType: "system_stop", Outcome: "success",
			ActorType: "system", Summary: "PortPilot 服务停止", ReasonCode: "shutdown",
			Details: map[string]any{"version": version.String()},
		})
		a.closeAgentWriter()
		storeErr := a.Events.Close()
		cacheErr := a.Cache.Close()
		result = errors.Join(result, adminErr, dataErr, storeErr, cacheErr)
	})
	return result
}

func (a *App) Ready() bool {
	return a.ready.Load()
}

func (a *App) Degraded() bool {
	return a.storeError.Load() ||
		(a.Events != nil && a.Events.Degraded()) ||
		(a.Activity != nil && a.Activity.Degraded()) ||
		(a.GeoIP != nil && a.GeoIP.Degraded())
}

func (a *App) serveData(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimSuffix(r.URL.Path, "/")
	switch {
	case strings.HasSuffix(path, "/control"):
		a.handleControl(w, r)
	case strings.HasSuffix(path, "/data/mux"):
		a.handleMuxUpgrade(w, r)
	case strings.HasSuffix(path, "/data/direct"):
		a.handleDirectUpgrade(w, r)
	default:
		http.NotFound(w, r)
	}
}
