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

	"github.com/portpilot/portpilot/internal/auth"
	"github.com/portpilot/portpilot/internal/config"
	"github.com/portpilot/portpilot/internal/store"
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
	Admin          *AdminAuth
	ControlLimiter *auth.RateLimiter
	DataLimiter    *auth.RateLimiter
	Logger         *slog.Logger

	adminServer *http.Server
	dataServer  *http.Server
	storeError  atomic.Bool
	ready       atomic.Bool
	closeOnce   sync.Once
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
		Admin:          NewAdminAuth(cfg.Users, 30*time.Minute, 8*time.Hour, cfg.Limits.MaxAdminLoginAttemptsPerMinute),
		ControlLimiter: auth.NewRateLimiter(60, time.Minute),
		DataLimiter:    auth.NewRateLimiter(cfg.Limits.MaxDataHandshakesPerMinute, time.Minute),
	}
	app.Sessions = NewSessionManager(app)
	app.Tunnels = NewTunnelManager(app)
	app.UDP = NewUDPManager(app)
	app.Listeners = NewListenerManager(app)

	eventStore, storeErr := store.Open(cfg.DataDB, logger)
	if storeErr != nil {
		logger.Error("sqlite unavailable; data plane will continue", "error", storeErr)
		app.storeError.Store(true)
	} else {
		app.Events = eventStore
	}
	commit, _, err := app.Listeners.Prepare(configManager.Snapshot())
	if err != nil {
		if eventStore != nil {
			_ = eventStore.Close()
		}
		return nil, fmt.Errorf("prepare proxy listeners: %w", err)
	}
	commit()

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
		adminErr := a.adminServer.Shutdown(ctx)
		dataErr := a.dataServer.Shutdown(ctx)
		storeErr := a.Events.Close()
		result = errors.Join(adminErr, dataErr, storeErr)
	})
	return result
}

func (a *App) Ready() bool {
	return a.ready.Load()
}

func (a *App) Degraded() bool {
	return a.storeError.Load() || (a.Events != nil && a.Events.Degraded())
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
