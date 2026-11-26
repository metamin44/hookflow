// Command api runs the HookFlow HTTP process: health endpoints plus the
// authenticated Phase 6 management API.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	eventapp "github.com/metamin44/hookflow/internal/app/event"
	"github.com/metamin44/hookflow/internal/app/management"
	"github.com/metamin44/hookflow/internal/modules/apikey"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	platformhttp "github.com/metamin44/hookflow/internal/platform/http"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

const (
	startupTimeout         = 10 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

type pool interface {
	Ping(context.Context) error
	Close()
}

// managementRoutes registers the Phase 6 routes on the API mux.
type managementRoutes func(*http.ServeMux)

type metricsServer interface {
	Close(context.Context) error
	Addr() string
}

type dependencies struct {
	loadConfig           func() (config.Config, error)
	loadAPIConfig        func() (config.API, error)
	loadDeliverySecurity func() (config.DeliverySecurity, error)
	loadEndpointLimits   func() (config.EndpointLimits, error)
	loadMetricsAddr      func() (string, error)
	newPool              func(context.Context, config.Database) (pool, error)
	newManagement        func(pool, config.DeliverySecurity, config.EndpointLimits, *observability.Metrics, *slog.Logger) (managementRoutes, error)
	listen               func(network, address string) (net.Listener, error)
	startMetrics         func(context.Context, string, *observability.Metrics, *slog.Logger) (metricsServer, error)
	logOutput            io.Writer
	shutdownTimeout      time.Duration
	onStarted            func(net.Listener)
	onMetricsStarted     func(string)
}

func defaultDependencies() dependencies {
	return dependencies{
		loadConfig:           config.Load,
		loadAPIConfig:        config.LoadAPI,
		loadDeliverySecurity: config.LoadDeliverySecurity,
		loadEndpointLimits:   config.LoadEndpointLimits,
		loadMetricsAddr:      config.LoadMetricsAddr,
		newPool: func(ctx context.Context, cfg config.Database) (pool, error) {
			return database.NewPool(ctx, cfg)
		},
		newManagement: defaultManagementRoutes,
		listen:        net.Listen,
		startMetrics: func(ctx context.Context, address string, metrics *observability.Metrics, logger *slog.Logger) (metricsServer, error) {
			return observability.StartServer(ctx, address, metrics, logger)
		},
		logOutput:       os.Stdout,
		shutdownTimeout: defaultShutdownTimeout,
	}
}

// defaultManagementRoutes builds the Phase 6 management handler from the
// existing pool, the development file provider and the endpoint ceiling.
func defaultManagementRoutes(p pool, security config.DeliverySecurity, limits config.EndpointLimits, metrics *observability.Metrics, logger *slog.Logger) (managementRoutes, error) {
	rawPool, ok := p.(interface{ Raw() *pgxpool.Pool })
	if !ok {
		return nil, errors.New("management routes require the PostgreSQL pool")
	}
	provider, err := kek.NewFileProvider(security.KeyringPath, security.CurrentKID, kek.FileOptions{})
	if err != nil {
		return nil, fmt.Errorf("initialize secret provider: %w", err)
	}
	if err := provider.ValidateKIDs(security.AllowedKIDs); err != nil {
		return nil, fmt.Errorf("validate secret keyring: %w", err)
	}
	service, err := management.NewService(rawPool.Raw(), provider, limits.MaxActivePerOrganization, logger, secrets.DefaultReader())
	if err != nil {
		return nil, err
	}
	authenticator, err := management.NewAuthenticator(rawPool.Raw(), apikey.NewVerifier())
	if err != nil {
		return nil, err
	}
	ingester, err := eventapp.NewService(rawPool.Raw(), eventapp.Options{Metrics: metrics, Logger: logger})
	if err != nil {
		return nil, err
	}
	handler, err := management.NewHandler(service, authenticator, ingester, logger)
	if err != nil {
		return nil, err
	}
	return handler.Register, nil
}

type apiStartupConfig struct {
	database    config.Database
	api         config.API
	security    config.DeliverySecurity
	limits      config.EndpointLimits
	metricsAddr string
}

func loadAPIStartupConfig(deps dependencies) (apiStartupConfig, error) {
	var startup apiStartupConfig
	var err error
	cfg, err := deps.loadConfig()
	if err != nil {
		return startup, fmt.Errorf("load database configuration: %w", err)
	}
	startup.database = cfg.Database
	startup.api, err = deps.loadAPIConfig()
	if err != nil {
		return startup, fmt.Errorf("load API configuration: %w", err)
	}
	startup.security, err = deps.loadDeliverySecurity()
	if err != nil {
		return startup, fmt.Errorf("load delivery security configuration: %w", err)
	}
	startup.limits, err = deps.loadEndpointLimits()
	if err != nil {
		return startup, fmt.Errorf("load endpoint limits: %w", err)
	}
	startup.metricsAddr, err = deps.loadMetricsAddr()
	if err != nil {
		return startup, fmt.Errorf("load metrics configuration: %w", err)
	}
	return startup, nil
}

func run(ctx context.Context, deps dependencies) error {
	startup, err := loadAPIStartupConfig(deps)
	if err != nil {
		return err
	}
	metrics := observability.New()

	logOutput := deps.logOutput
	if logOutput == nil {
		logOutput = os.Stdout
	}
	logger := slog.New(slog.NewJSONHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting API", "addr", startup.api.Addr)

	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	dbPool, err := deps.newPool(startupCtx, startup.database)
	cancelStartup()
	if err != nil {
		logger.Error("PostgreSQL initialization failed", "error", err)
		return fmt.Errorf("initialize PostgreSQL: %w", err)
	}

	poolClosed := false
	closePool := func() {
		if poolClosed {
			return
		}
		poolClosed = true
		dbPool.Close()
	}
	defer closePool()

	logger.Info("PostgreSQL pool initialized")

	routes, err := deps.newManagement(dbPool, startup.security, startup.limits, metrics, logger)
	if err != nil {
		logger.Error("management route initialization failed", "error", err)
		return fmt.Errorf("initialize management routes: %w", err)
	}

	router := http.NewServeMux()
	router.Handle("/", platformhttp.NewHandler(dbPool, logger))
	routes(router)
	server := platformhttp.NewServer(startup.api.Addr, router, logger)

	listener, err := deps.listen("tcp", startup.api.Addr)
	if err != nil {
		logger.Error("HTTP listen failed", "addr", startup.api.Addr, "error", err)
		return fmt.Errorf("listen on %s: %w", startup.api.Addr, err)
	}

	metricsHandle, err := startOptionalMetrics(ctx, deps, startup.metricsAddr, metrics, logger)
	if err != nil {
		_ = listener.Close()
		logger.Error("metrics listener startup failed", "error", err)
		return fmt.Errorf("start API metrics listener: %w", err)
	}
	if metricsHandle != nil && deps.onMetricsStarted != nil {
		deps.onMetricsStarted(metricsHandle.Addr())
	}

	serveErr := make(chan error, 1)
	go func() {
		serveErr <- server.Serve(listener)
	}()

	if deps.onStarted != nil {
		deps.onStarted(listener)
	}
	logger.Info("HTTP server listening", "addr", listener.Addr().String())

	select {
	case <-ctx.Done():
		logger.Info("shutdown requested")
		return shutdown(server, metricsHandle, serveErr, closePool, deps.shutdownTimeout, logger)
	case err := <-serveErr:
		if metricsHandle != nil {
			metricsCtx, cancelMetrics := context.WithTimeout(context.Background(), deps.shutdownTimeout)
			metricsErr := metricsHandle.Close(metricsCtx)
			cancelMetrics()
			if metricsErr != nil {
				logger.Error("metrics listener close failed", "error", metricsErr)
			}
		}
		closePool()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}

func startOptionalMetrics(ctx context.Context, deps dependencies, address string, metrics *observability.Metrics, logger *slog.Logger) (metricsServer, error) {
	if address == "" {
		return nil, nil
	}
	return deps.startMetrics(ctx, address, metrics, logger)
}

func shutdown(server *http.Server, metrics metricsServer, serveErr <-chan error, closePool func(), timeout time.Duration, logger *slog.Logger) error {
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	shutdownErr := server.Shutdown(ctx)
	if shutdownErr != nil {
		logger.Warn("graceful shutdown failed; forcing close", "error", shutdownErr)
		if closeErr := server.Close(); closeErr != nil {
			shutdownErr = errors.Join(shutdownErr, closeErr)
		}
	}
	if metrics != nil {
		shutdownErr = errors.Join(shutdownErr, metrics.Close(ctx))
	}

	if err := <-serveErr; err != nil && !errors.Is(err, http.ErrServerClosed) {
		shutdownErr = errors.Join(shutdownErr, fmt.Errorf("serve HTTP: %w", err))
	}

	closePool()

	if shutdownErr != nil {
		logger.Error("shutdown failed", "error", shutdownErr)
		return fmt.Errorf("shutdown API server: %w", shutdownErr)
	}
	logger.Info("shutdown completed")
	return nil
}
