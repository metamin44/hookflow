package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/idempotency"
	"github.com/metamin44/hookflow/internal/app/recovery"
	workerruntime "github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

const (
	startupTimeout         = 15 * time.Second
	defaultShutdownTimeout = 35 * time.Second
)

type pool interface {
	Ping(context.Context) error
	Close()
	Raw() *pgxpool.Pool
}

type component interface {
	Run(context.Context) error
}

type consumerComponent interface {
	component
	Start(context.Context) error
	Quiescent() bool
}

type metricsServer interface {
	Close(context.Context) error
}

type dependencies struct {
	loadConfig         func() (config.Config, error)
	loadWorkerConfig   func() (config.Worker, error)
	loadDeliveryConfig func() (config.DeliveryWorker, error)
	loadSecurityConfig func() (config.DeliverySecurity, error)
	loadRecoveryConfig func() (config.Recovery, error)
	loadRabbitMQ       func() (config.RabbitMQ, error)
	loadMetricsAddr    func() (string, error)
	newPool            func(context.Context, config.Database, int) (pool, error)
	newProvider        func(config.DeliverySecurity) (kek.Provider, error)
	newSweeper         func(pool, config.IdempotencySweep, *slog.Logger) component
	newRecovery        func(pool, config.Recovery, *slog.Logger) component
	newConsumer        func(context.Context, pool, config.DeliveryWorker, config.DeliverySecurity, kek.Provider, config.RabbitMQ, *slog.Logger, *observability.Metrics) (consumerComponent, error)
	startMetrics       func(context.Context, string, *observability.Metrics, *slog.Logger) (metricsServer, error)
	logOutput          io.Writer
	shutdownTimeout    time.Duration
	onStarted          func()
}

func defaultDependencies() dependencies {
	return dependencies{
		loadConfig: config.Load, loadWorkerConfig: config.LoadWorker,
		loadDeliveryConfig: config.LoadDeliveryWorker, loadRecoveryConfig: config.LoadRecovery,
		loadSecurityConfig: config.LoadDeliverySecurity,
		loadRabbitMQ:       config.LoadRabbitMQ, loadMetricsAddr: config.LoadMetricsAddr,
		newPool: func(ctx context.Context, cfg config.Database, size int) (pool, error) {
			return database.NewPoolWithSize(ctx, cfg, size)
		},
		newProvider: func(security config.DeliverySecurity) (kek.Provider, error) {
			provider, err := kek.NewFileProvider(security.KeyringPath, security.CurrentKID, kek.FileOptions{})
			if err != nil {
				return nil, err
			}
			if err := provider.ValidateKIDs(security.AllowedKIDs); err != nil {
				return nil, err
			}
			return provider, nil
		},
		newSweeper: func(db pool, cfg config.IdempotencySweep, logger *slog.Logger) component {
			return idempotency.NewSweeper(idempotency.NewPostgresStore(db.Raw()), idempotency.Options{
				BatchSize: cfg.BatchSize, Interval: cfg.Interval, MaxBatchesPerTick: cfg.MaxBatchesPerTick,
			}, logger)
		},
		newRecovery: func(db pool, cfg config.Recovery, logger *slog.Logger) component {
			sweep, err := recovery.NewSweep(db.Raw(), recovery.Options{
				Budget: recovery.Budget{
					Total:  cfg.Total,
					Expire: recovery.PassBudget{MaxCandidates: cfg.Expire.MaxCandidates, MaxDuration: cfg.Expire.MaxDuration},
					Repair: recovery.PassBudget{MaxCandidates: cfg.Repair.MaxCandidates, MaxDuration: cfg.Repair.MaxDuration},
					ReArm:  recovery.PassBudget{MaxCandidates: cfg.ReArm.MaxCandidates, MaxDuration: cfg.ReArm.MaxDuration},
					Stale:  recovery.PassBudget{MaxCandidates: cfg.Stale.MaxCandidates, MaxDuration: cfg.Stale.MaxDuration},
				},
				StalePublishedAfter: cfg.StalePublishedAfter,
				BuildMessage:        recovery.BuildMessage, Logger: logger,
			})
			if err != nil {
				return &failedComponent{err: err}
			}
			scheduler, err := recovery.NewScheduler(sweep, cfg.Cadence, logger)
			if err != nil {
				return &failedComponent{err: err}
			}
			return scheduler
		},
		newConsumer: func(_ context.Context, db pool, cfg config.DeliveryWorker, security config.DeliverySecurity, provider kek.Provider, rabbit config.RabbitMQ, logger *slog.Logger, metrics *observability.Metrics) (consumerComponent, error) {
			store, err := delivery.NewStore(db.Raw(), delivery.Options{NewID: newEntityID, BuildRetryMessage: delivery.BuildRetryMessage, Logger: logger})
			if err != nil {
				return nil, err
			}
			executor, err := workerruntime.NewExecutor(workerruntime.HTTPOptions{
				Deadline: cfg.HTTPDeadline, HandshakeBudget: cfg.HandshakeBudget,
				PostProcessBudget: cfg.PostProcessBudget, ShutdownReserve: cfg.ShutdownReserve,
				ResponseBodyLimit: cfg.ResponseBodyLimit,
				Metrics:           metrics,
			}, signing.New(), logger)
			if err != nil {
				return nil, err
			}
			retry, err := workerruntime.NewRetryPolicy(cfg.MaxAttempts, cfg.RetryHorizon)
			if err != nil {
				return nil, err
			}
			processor, err := workerruntime.NewRuntime(cfg, security, provider, store, executor, retry, delivery.RandomToken, logger, metrics)
			if err != nil {
				return nil, err
			}
			consumer, err := workerruntime.NewConsumer(rabbit, cfg.Prefetch, cfg.LeaseDuration-cfg.ShutdownReserve, processor, logger, metrics)
			if err != nil {
				return nil, err
			}
			return consumer, nil
		},
		startMetrics: func(ctx context.Context, address string, metrics *observability.Metrics, logger *slog.Logger) (metricsServer, error) {
			return observability.StartServer(ctx, address, metrics, logger)
		},
		logOutput: os.Stdout, shutdownTimeout: defaultShutdownTimeout,
	}
}

// newShutdownContext transfers cancellation ownership to run's deferred resource
// cleanup. It must remain live while both component draining and pool closing run.
func newShutdownContext(timeout time.Duration) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.Background(), timeout)
}

func isClosed(done <-chan struct{}) bool {
	select {
	case <-done:
		return true
	default:
		return false
	}
}

func closeWorkerResources(ctx context.Context, db pool, metrics metricsServer, consumer consumerComponent, consumerDone, componentsDone <-chan struct{}) error {
	var closeErr error
	if metrics != nil {
		closeErr = errors.Join(closeErr, metrics.Close(ctx))
	}
	if consumer != nil && consumerDone != nil && !isClosed(consumerDone) {
		return errors.Join(closeErr, errors.New("shutdown worker: PostgreSQL pool left open because consumer run has not returned"))
	}
	if consumer != nil && !consumer.Quiescent() {
		return errors.Join(closeErr, errors.New("shutdown worker: PostgreSQL pool left open because tracked settlement remains active"))
	}
	if componentsDone != nil && !isClosed(componentsDone) {
		return errors.Join(closeErr, errors.New("shutdown worker: PostgreSQL pool left open because a database-using component has not returned"))
	}
	closed := make(chan struct{})
	go func() { db.Close(); close(closed) }()
	select {
	case <-closed:
	case <-ctx.Done():
		closeErr = errors.Join(closeErr, errors.New("shutdown worker: PostgreSQL pool cleanup exceeded the overall deadline"))
	}
	return closeErr
}

type failedComponent struct{ err error }

func (c *failedComponent) Run(context.Context) error { return c.err }

// loadDeliverySecurity loads the non-secret security configuration and builds
// the configured wrapping provider. Both success values are used later by the
// worker composition in run.
func loadDeliverySecurity(deps dependencies) (config.DeliverySecurity, kek.Provider, error) {
	securityCfg, err := deps.loadSecurityConfig()
	if err != nil {
		return config.DeliverySecurity{}, nil, fmt.Errorf("load delivery security configuration: %w", err)
	}
	provider, err := deps.newProvider(securityCfg)
	if err != nil {
		return config.DeliverySecurity{}, nil, fmt.Errorf("initialize webhook secret provider: %w", err)
	}
	return securityCfg, provider, nil
}

// runtimeSettings groups the remaining process settings loaded before startup.
type runtimeSettings struct {
	recovery    config.Recovery
	rabbit      config.RabbitMQ
	metricsAddr string
}

func loadRuntimeSettings(deps dependencies) (runtimeSettings, error) {
	var settings runtimeSettings
	var err error
	settings.recovery, err = deps.loadRecoveryConfig()
	if err != nil {
		return runtimeSettings{}, fmt.Errorf("load recovery configuration: %w", err)
	}
	settings.rabbit, err = deps.loadRabbitMQ()
	if err != nil {
		return runtimeSettings{}, fmt.Errorf("load RabbitMQ configuration: %w", err)
	}
	settings.metricsAddr, err = deps.loadMetricsAddr()
	if err != nil {
		return runtimeSettings{}, fmt.Errorf("load metrics configuration: %w", err)
	}
	return settings, nil
}

// processConfig is the complete validated configuration and provider set the
// worker process needs before opening PostgreSQL or RabbitMQ.
type processConfig struct {
	cfg         config.Config
	workerCfg   config.Worker
	deliveryCfg config.DeliveryWorker
	securityCfg config.DeliverySecurity
	provider    kek.Provider
	recoveryCfg config.Recovery
	rabbitCfg   config.RabbitMQ
	metricsAddr string
}

func loadProcessConfig(deps dependencies) (processConfig, error) {
	var result processConfig
	var err error
	result.cfg, err = deps.loadConfig()
	if err != nil {
		return processConfig{}, fmt.Errorf("load database configuration: %w", err)
	}
	result.workerCfg, err = deps.loadWorkerConfig()
	if err != nil {
		return processConfig{}, fmt.Errorf("load worker configuration: %w", err)
	}
	result.deliveryCfg, err = deps.loadDeliveryConfig()
	if err != nil {
		return processConfig{}, fmt.Errorf("load delivery worker configuration: %w", err)
	}
	result.securityCfg, result.provider, err = loadDeliverySecurity(deps)
	if err != nil {
		return processConfig{}, err
	}
	settings, err := loadRuntimeSettings(deps)
	if err != nil {
		return processConfig{}, err
	}
	result.recoveryCfg, result.rabbitCfg, result.metricsAddr = settings.recovery, settings.rabbit, settings.metricsAddr
	return result, nil
}

// startOptionalMetrics starts the metrics listener only when an address is
// configured.
func startOptionalMetrics(ctx context.Context, deps dependencies, address string, metrics *observability.Metrics, logger *slog.Logger) (metricsServer, error) {
	if address == "" {
		return nil, nil
	}
	return deps.startMetrics(ctx, address, metrics, logger)
}

func run(ctx context.Context, deps dependencies) (runErr error) {
	startup, err := loadProcessConfig(deps)
	if err != nil {
		return err
	}
	logger := slog.New(slog.NewJSONHandler(deps.logOutput, &slog.HandlerOptions{Level: slog.LevelInfo}))
	metrics := observability.New()

	startupCtx, cancelStartup := context.WithTimeout(ctx, startupTimeout)
	defer cancelStartup()
	db, err := deps.newPool(startupCtx, startup.cfg.Database, startup.deliveryCfg.PoolSize)
	if err != nil {
		return fmt.Errorf("initialize PostgreSQL: %w", err)
	}
	timeout := deps.shutdownTimeout
	if timeout <= 0 {
		timeout = defaultShutdownTimeout
	}
	var shutdownCtx context.Context
	var cancelShutdown context.CancelFunc
	var metricsHandle metricsServer
	var consumer consumerComponent
	var consumerDone chan struct{}
	var componentsDone chan struct{}
	defer func() {
		if shutdownCtx == nil {
			shutdownCtx, cancelShutdown = newShutdownContext(timeout)
		}
		defer cancelShutdown()
		runErr = errors.Join(runErr, closeWorkerResources(shutdownCtx, db, metricsHandle, consumer, consumerDone, componentsDone))
	}()

	consumer, err = deps.newConsumer(startupCtx, db, startup.deliveryCfg, startup.securityCfg, startup.provider, startup.rabbitCfg, logger, metrics)
	if err != nil {
		cancelStartup()
		return fmt.Errorf("initialize worker consumer: %w", err)
	}
	if err := consumer.Start(startupCtx); err != nil {
		cancelStartup()
		return fmt.Errorf("start worker consumer: %w", err)
	}
	cancelStartup()
	sweeper := deps.newSweeper(db, startup.workerCfg.IdempotencySweep, logger)
	recoveryRunner := deps.newRecovery(db, startup.recoveryCfg, logger)
	metricsHandle, err = startOptionalMetrics(ctx, deps, startup.metricsAddr, metrics, logger)
	if err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancel(ctx)
	defer cancelRun()
	errorsCh := make(chan error, 3)
	consumerDone = make(chan struct{})
	componentsDone = make(chan struct{})
	var componentsRemaining atomic.Int32
	componentsRemaining.Store(3)
	finishComponent := func(err error) {
		if componentsRemaining.Add(-1) == 0 {
			close(componentsDone)
		}
		errorsCh <- err
	}
	go func() {
		// Publish completion before the result so deferred pool cleanup cannot
		// observe a returned consumer that has not yet been marked stopped.
		err := consumer.Run(runCtx)
		close(consumerDone)
		finishComponent(err)
	}()
	go func() { finishComponent(recoveryRunner.Run(runCtx)) }()
	go func() { finishComponent(sweeper.Run(runCtx)) }()
	if deps.onStarted != nil {
		deps.onStarted()
	}
	logger.Info("worker started", "concurrency", startup.deliveryCfg.Concurrency, "prefetch", startup.deliveryCfg.Prefetch)

	remaining := 3
	select {
	case <-ctx.Done():
		logger.Info("worker shutdown requested")
	case err := <-errorsCh:
		remaining--
		if err != nil {
			runErr = fmt.Errorf("run worker component: %w", err)
		} else if ctx.Err() == nil {
			runErr = errors.New("run worker component: stopped unexpectedly")
		}
	}
	shutdownCtx, cancelShutdown = newShutdownContext(timeout)
	cancelRun()
	for remaining > 0 {
		select {
		case err := <-errorsCh:
			remaining--
			if err != nil {
				runErr = errors.Join(runErr, fmt.Errorf("shutdown worker component: %w", err))
			}
		case <-shutdownCtx.Done():
			return errors.Join(runErr, fmt.Errorf("shutdown worker: timed out after %s", timeout))
		}
	}
	logger.Info("worker shutdown completed")
	return runErr
}
