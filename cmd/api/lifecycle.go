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

type dependencies struct {
	loadConfig           func() (config.Config, error)
	loadAPIConfig        func() (config.API, error)
	loadDeliverySecurity func() (config.DeliverySecurity, error)
	loadEndpointLimits   func() (config.EndpointLimits, error)
	newPool              func(context.Context, config.Database) (pool, error)
	newManagement        func(pool, config.DeliverySecurity, config.EndpointLimits, *slog.Logger) (managementRoutes, error)
	listen               func(network, address string) (net.Listener, error)
	logOutput            io.Writer
	shutdownTimeout      time.Duration
	onStarted            func(net.Listener)
}

func defaultDependencies() dependencies {
	return dependencies{
		loadConfig:           config.Load,
		loadAPIConfig:        config.LoadAPI,
		loadDeliverySecurity: config.LoadDeliverySecurity,
		loadEndpointLimits:   config.LoadEndpointLimits,
		newPool: func(ctx context.Context, cfg config.Database) (pool, error) {
			return database.NewPool(ctx, cfg)
		},
		newManagement:   defaultManagementRoutes,
		listen:          net.Listen,
		logOutput:       os.Stdout,
		shutdownTimeout: defaultShutdownTimeout,
	}
}

// defaultManagementRoutes builds the Phase 6 management handler from the
// existing pool, the development file provider and the endpoint ceiling.
func defaultManagementRoutes(p pool, security config.DeliverySecurity, limits config.EndpointLimits, logger *slog.Logger) (managementRoutes, error) {
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
	// The API process has no metrics registry; the approved inexact-number
	// counter therefore stays nil here and is emitted only where a registry exists.
	ingester, err := eventapp.NewService(rawPool.Raw(), eventapp.Options{Logger: logger})
	if err != nil {
		return nil, err
	}
	handler, err := management.NewHandler(service, authenticator, ingester, logger)
	if err != nil {
		return nil, err
	}
	return handler.Register, nil
}

func run(ctx context.Context, deps dependencies) error {
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load database configuration: %w", err)
	}

	apiCfg, err := deps.loadAPIConfig()
	if err != nil {
		return fmt.Errorf("load API configuration: %w", err)
	}

	security, err := deps.loadDeliverySecurity()
	if err != nil {
		return fmt.Errorf("load delivery security configuration: %w", err)
	}

	limits, err := deps.loadEndpointLimits()
	if err != nil {
		return fmt.Errorf("load endpoint limits: %w", err)
	}

	logOutput := deps.logOutput
	if logOutput == nil {
		logOutput = os.Stdout
	}
	logger := slog.New(slog.NewJSONHandler(logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	logger.Info("starting API", "addr", apiCfg.Addr)

	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	dbPool, err := deps.newPool(startupCtx, cfg.Database)
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

	routes, err := deps.newManagement(dbPool, security, limits, logger)
	if err != nil {
		logger.Error("management route initialization failed", "error", err)
		return fmt.Errorf("initialize management routes: %w", err)
	}

	router := http.NewServeMux()
	router.Handle("/", platformhttp.NewHandler(dbPool, logger))
	routes(router)
	server := platformhttp.NewServer(apiCfg.Addr, router, logger)

	listener, err := deps.listen("tcp", apiCfg.Addr)
	if err != nil {
		logger.Error("HTTP listen failed", "addr", apiCfg.Addr, "error", err)
		return fmt.Errorf("listen on %s: %w", apiCfg.Addr, err)
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
		return shutdown(server, serveErr, closePool, deps.shutdownTimeout, logger)
	case err := <-serveErr:
		closePool()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger.Error("HTTP server failed", "error", err)
			return fmt.Errorf("serve HTTP: %w", err)
		}
		return nil
	}
}

func shutdown(server *http.Server, serveErr <-chan error, closePool func(), timeout time.Duration, logger *slog.Logger) error {
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
