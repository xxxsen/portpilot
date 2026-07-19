package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/portpilot/portpilot/internal/activity"
	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/geoip"
	"github.com/portpilot/portpilot/internal/store"
	"github.com/portpilot/portpilot/internal/version"
)

type App struct {
	Config         config.ServerConfig
	ConfigManager  *config.Manager
	Sessions       *SessionManager
	Tickets        *auth.TicketStore
	Challenges     *auth.ChallengeStore
	Tunnels        *TunnelManager
	UDP            *UDPManager
	Listeners      *ListenerManager
	Events         *store.Store
	Activity       *activity.Service
	GeoIP          *geoip.Resolver
	Admin          *AdminAuth
	ControlLimiter *auth.RateLimiter
	DataLimiter    *auth.RateLimiter
	Logger         *slog.Logger

	adminServer *http.Server
	dataServer  *http.Server
	analytics   analyticsQueryMetrics

	analyticsTimeout time.Duration
	storeError       atomic.Bool
	ready            atomic.Bool
	closeOnce        sync.Once
}

func New(configPath string, logger *slog.Logger) (*App, error) {
	cfg, err := config.LoadServer(configPath)
	if err != nil {
		return nil, fmt.Errorf("load server configuration: %w", err)
	}
	configManager, err := config.NewManagerWithPolicy(
		cfg.ClientConfigFile, cfg.AdminAddr, cfg.ServerAddr, cfg.TargetPolicy,
	)
	if err != nil {
		return nil, fmt.Errorf("load client proxy configuration: %w", err)
	}
	app := &App{
		Config: cfg, ConfigManager: configManager, Logger: logger,
		Tickets:        auth.NewTicketStore(cfg.Duration(cfg.Timeouts.DataConnect)),
		Challenges:     auth.NewChallengeStore(cfg.Duration(cfg.Timeouts.ControlHandshake)),
		Admin:          NewAdminAuth(cfg.Users, cfg.Limits.MaxAdminLoginAttemptsPerMinute),
		ControlLimiter: auth.NewRateLimiter(60, time.Minute),
		DataLimiter:    auth.NewRateLimiter(cfg.Limits.MaxDataHandshakesPerMinute, time.Minute),
	}
	app.Sessions = NewSessionManager(app)
	app.Tunnels = NewTunnelManager(app)
	app.UDP = NewUDPManager(app)
	app.Listeners = NewListenerManager(app)

	eventStore, err := store.Open(cfg.DataDB, logger)
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
	geoResolver, err := geoip.New(
		cfg.Observability.GeoIPCountryDatabase,
		cfg.Duration(cfg.Observability.GeoIPReloadInterval),
		logger,
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
	return app, nil
}

func (a *App) Run(ctx context.Context) error {
	errorsCh := make(chan error, 2)
	go func() {
		a.Logger.Info("admin server listening", "address", a.Config.AdminAddr)
		if err := a.adminServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsCh <- fmt.Errorf("admin server: %w", err)
		}
	}()
	go func() {
		a.Logger.Info("control/data server listening", "address", a.Config.ServerAddr)
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
		storeErr := a.Events.Close()
		result = errors.Join(adminErr, dataErr, storeErr)
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

func DefaultLogger(level slog.Level) *slog.Logger {
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level})
	return slog.New(handler)
}
