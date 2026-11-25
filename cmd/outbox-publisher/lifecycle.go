package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/app/publisher"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

const (
	startupTimeout         = 15 * time.Second
	defaultShutdownTimeout = 10 * time.Second
)

type pool interface {
	Ping(context.Context) error
	Close()
	Raw() *pgxpool.Pool
}

type relayRuntime interface {
	Start(context.Context) error
	Run(context.Context) error
	Close()
}

type dependencies struct {
	loadConfig      func() (config.Config, error)
	loadPublisher   func() (config.Publisher, error)
	loadRabbitMQ    func() (config.RabbitMQ, error)
	loadMetricsAddr func() (string, error)
	newPool         func(context.Context, config.Database) (pool, error)
	newRelay        func(context.Context, pool, config.Publisher, config.RabbitMQ, string, *slog.Logger, *observability.Metrics) (relayRuntime, error)
	startMetrics    func(context.Context, string, *observability.Metrics, *slog.Logger) (metricsServer, error)
	logOutput       io.Writer
	shutdownTimeout time.Duration
	onStarted       func()
}

type metricsServer interface {
	Close(context.Context) error
}

func defaultDependencies() dependencies {
	return dependencies{
		loadConfig: config.Load, loadPublisher: config.LoadPublisher,
		loadRabbitMQ: config.LoadRabbitMQ, loadMetricsAddr: config.LoadMetricsAddr,
		newPool: func(ctx context.Context, cfg config.Database) (pool, error) { return database.NewPool(ctx, cfg) },
		newRelay: func(ctx context.Context, db pool, cfg config.Publisher, rabbit config.RabbitMQ, processID string, logger *slog.Logger, metrics *observability.Metrics) (relayRuntime, error) {
			store := outbox.NewStore(db.Raw(), logger)
			session, err := publisher.NewSession(rabbit, cfg.ConfirmTimeout, logger, metrics)
			if err != nil {
				return nil, err
			}
			relay, err := publisher.NewRelay(publisher.Options{
				Config: cfg, RabbitMQ: rabbit, ProcessID: processID, Store: store,
				NewToken: outbox.RandomToken, Broker: session, Logger: logger, Metrics: metrics,
			})
			if err != nil {
				session.Close()
				return nil, err
			}
			if err := relay.Start(ctx); err != nil {
				session.Close()
				return nil, err
			}
			return relay, nil
		},
		startMetrics: func(ctx context.Context, address string, metrics *observability.Metrics, logger *slog.Logger) (metricsServer, error) {
			return observability.StartServer(ctx, address, metrics, logger)
		},
		logOutput: os.Stdout, shutdownTimeout: defaultShutdownTimeout,
	}
}

func run(ctx context.Context, deps dependencies) error {
	cfg, err := deps.loadConfig()
	if err != nil {
		return fmt.Errorf("load database configuration: %w", err)
	}
	publisherCfg, err := deps.loadPublisher()
	if err != nil {
		return fmt.Errorf("load publisher configuration: %w", err)
	}
	rabbitCfg, err := deps.loadRabbitMQ()
	if err != nil {
		return fmt.Errorf("load RabbitMQ configuration: %w", err)
	}
	metricsAddr, err := deps.loadMetricsAddr()
	if err != nil {
		return fmt.Errorf("load metrics configuration: %w", err)
	}
	logger := slog.New(slog.NewJSONHandler(deps.logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	metrics := observability.New()
	processID, err := id.NewV7()
	if err != nil {
		return fmt.Errorf("generate publisher process id: %w", err)
	}

	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()
	db, err := deps.newPool(startupCtx, cfg.Database)
	if err != nil {
		return fmt.Errorf("initialize PostgreSQL: %w", err)
	}
	defer db.Close()

	relay, err := deps.newRelay(startupCtx, db, publisherCfg, rabbitCfg, processID, logger, metrics)
	cancelStartup()
	if err != nil {
		return fmt.Errorf("initialize publisher runtime: %w", err)
	}
	defer relay.Close()
	if metricsAddr != "" {
		server, err := deps.startMetrics(ctx, metricsAddr, metrics, logger)
		if err != nil {
			return err
		}
		defer func() {
			closeCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = server.Close(closeCtx)
		}()
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	done := make(chan error, 1)
	go func() { done <- relay.Run(runCtx) }()
	if deps.onStarted != nil {
		deps.onStarted()
	}
	logger.Info("publisher started")

	select {
	case <-ctx.Done():
		cancelRun()
	case err := <-done:
		cancelRun()
		if err != nil {
			return fmt.Errorf("run publisher: %w", err)
		}
		return nil
	}

	timeout := deps.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	select {
	case err := <-done:
		if err != nil {
			return fmt.Errorf("shutdown publisher: %w", err)
		}
	case <-time.After(timeout):
		return fmt.Errorf("shutdown publisher: timed out after %s", timeout)
	}
	logger.Info("publisher shutdown completed")
	return nil
}
