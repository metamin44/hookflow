package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	workerruntime "github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

type fakePool struct{ closed int }

func (p *fakePool) Ping(context.Context) error { return nil }
func (p *fakePool) Close()                     { p.closed++ }
func (p *fakePool) Raw() *pgxpool.Pool         { return nil }

type fakeComponent struct{ run func(context.Context) error }

func (c *fakeComponent) Run(ctx context.Context) error {
	if c.run != nil {
		return c.run(ctx)
	}
	<-ctx.Done()
	return nil
}

type fakeConsumer struct {
	fakeComponent
	start     func(context.Context) error
	quiescent func() bool
}

func (c *fakeConsumer) Start(ctx context.Context) error {
	if c.start != nil {
		return c.start(ctx)
	}
	return nil
}

func (c *fakeConsumer) Quiescent() bool {
	return c.quiescent == nil || c.quiescent()
}

func testDatabaseConfig() (config.Config, error) {
	return config.Config{Database: config.Database{Name: "hookflow_test", User: "hookflow", Password: "unit-test", Host: "127.0.0.1", Port: 5433, SSLMode: "disable"}}, nil
}
func testWorkerConfig() (config.Worker, error) {
	return config.Worker{IdempotencySweep: config.IdempotencySweep{BatchSize: 1, Interval: time.Hour, MaxBatchesPerTick: 1}}, nil
}
func testDeliveryConfig() (config.DeliveryWorker, error) {
	return config.DeliveryWorker{LeaseDuration: 30 * time.Second, HTTPDeadline: 10 * time.Second, HandshakeBudget: 2 * time.Second, PostProcessBudget: 3 * time.Second, ShutdownReserve: 10 * time.Second, SecretResolutionBudget: 2 * time.Second, Concurrency: 1, Prefetch: 1, PoolSize: 2, MaxAttempts: 8, RetryHorizon: 12 * time.Hour, DecryptFailurePacing: 30 * time.Second, ResponseBodyLimit: 1024}, nil
}
func testSecurityConfig() (config.DeliverySecurity, error) {
	return config.DeliverySecurity{
		Mode:             config.ModeDevelopment,
		Provider:         kek.ProviderLocalFile,
		KeyringPath:      "/outside/repository/keyring.json",
		CurrentKID:       "kek-1",
		AllowedKIDs:      []string{"kek-1"},
		AllowedProviders: []string{kek.ProviderLocalFile},
	}, nil
}
func testRecoveryConfig() (config.Recovery, error) {
	return config.Recovery{Cadence: time.Minute, Total: 45 * time.Second, Expire: config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: 12 * time.Second}, Repair: config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: 10 * time.Second}, ReArm: config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: 10 * time.Second}, Stale: config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: 8 * time.Second}, StalePublishedAfter: 5 * time.Minute}, nil
}
func testRabbitConfig() (config.RabbitMQ, error) {
	return config.RabbitMQ{URI: "amqp://user:password@localhost:5672/", ManagementURL: "http://localhost:15672", VHost: "/", Heartbeat: 10 * time.Second, ConnectTimeout: 5 * time.Second, ReconnectMinBackoff: 500 * time.Millisecond, ReconnectMaxBackoff: 10 * time.Second}, nil
}

func baseDependencies(t *testing.T) (dependencies, *fakePool, *fakeConsumer) {
	t.Helper()
	db := &fakePool{}
	consumer := &fakeConsumer{}
	deps := dependencies{
		loadConfig: testDatabaseConfig, loadWorkerConfig: testWorkerConfig,
		loadDeliveryConfig: testDeliveryConfig, loadRecoveryConfig: testRecoveryConfig,
		loadSecurityConfig: testSecurityConfig,
		loadRabbitMQ:       testRabbitConfig, loadMetricsAddr: func() (string, error) { return "", nil },
		newPool: func(context.Context, config.Database, int) (pool, error) { return db, nil },
		newProvider: func(config.DeliverySecurity) (kek.Provider, error) {
			return kek.NewMemoryProvider("kek-1", map[string][]byte{"kek-1": make([]byte, 32)}, nil)
		},
		newSweeper:  func(pool, config.IdempotencySweep, *slog.Logger) component { return &fakeComponent{} },
		newRecovery: func(pool, config.Recovery, *slog.Logger) component { return &fakeComponent{} },
		newConsumer: func(context.Context, pool, config.DeliveryWorker, config.DeliverySecurity, kek.Provider, config.RabbitMQ, *slog.Logger, *observability.Metrics) (consumerComponent, error) {
			return consumer, nil
		},
		logOutput: io.Discard, shutdownTimeout: time.Second,
	}
	return deps, db, consumer
}

func TestRunStopsOnContextCancellation(t *testing.T) {
	deps, db, _ := baseDependencies(t)
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	<-started
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if db.closed != 1 {
		t.Fatalf("pool closed %d times", db.closed)
	}
}

func TestRunReturnsComponentError(t *testing.T) {
	deps, db, _ := baseDependencies(t)
	deps.newRecovery = func(pool, config.Recovery, *slog.Logger) component {
		return &fakeComponent{run: func(context.Context) error { return errors.New("sweep exploded") }}
	}
	err := run(context.Background(), deps)
	if err == nil || !strings.Contains(err.Error(), "run worker component") {
		t.Fatalf("error = %v", err)
	}
	if db.closed != 1 {
		t.Fatalf("pool closed %d times", db.closed)
	}
}

func TestRunFailsClosedOnConsumerStartup(t *testing.T) {
	deps, db, consumer := baseDependencies(t)
	consumer.start = func(context.Context) error { return errors.New("topology mismatch") }
	sweeperStarted := false
	deps.newSweeper = func(pool, config.IdempotencySweep, *slog.Logger) component {
		sweeperStarted = true
		return &fakeComponent{}
	}
	if err := run(context.Background(), deps); err == nil {
		t.Fatal("run succeeded despite consumer startup failure")
	}
	if sweeperStarted || db.closed != 1 {
		t.Fatalf("sweeper=%t pool closed=%d", sweeperStarted, db.closed)
	}
}

func TestRunUsesOneOverallShutdownDeadline(t *testing.T) {
	deps, _, _ := baseDependencies(t)
	releases := []chan struct{}{make(chan struct{}), make(chan struct{}), make(chan struct{})}
	delayed := func(index int, delay time.Duration) component {
		return &fakeComponent{run: func(context.Context) error {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-timer.C:
				close(releases[index])
			case <-releases[index]:
			}
			return nil
		}}
	}
	deps.newSweeper = func(pool, config.IdempotencySweep, *slog.Logger) component { return delayed(0, 60*time.Millisecond) }
	deps.newRecovery = func(pool, config.Recovery, *slog.Logger) component { return delayed(1, 60*time.Millisecond) }
	consumer := &fakeConsumer{}
	consumer.run = func(context.Context) error {
		timer := time.NewTimer(150 * time.Millisecond)
		defer timer.Stop()
		select {
		case <-timer.C:
			close(releases[2])
		case <-releases[2]:
		}
		return nil
	}
	deps.newConsumer = func(context.Context, pool, config.DeliveryWorker, config.DeliverySecurity, kek.Provider, config.RabbitMQ, *slog.Logger, *observability.Metrics) (consumerComponent, error) {
		return consumer, nil
	}
	deps.shutdownTimeout = 100 * time.Millisecond
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	<-started
	cancel()
	if err := <-done; err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Fatalf("error = %v, want overall shutdown deadline", err)
	}
}

func TestComponentFailureCancelsAndJoinsOtherComponents(t *testing.T) {
	deps, db, consumer := baseDependencies(t)
	cancelled := make(chan struct{})
	release := make(chan struct{})
	consumer.run = func(ctx context.Context) error {
		<-ctx.Done()
		close(cancelled)
		<-release
		return nil
	}
	deps.newRecovery = func(pool, config.Recovery, *slog.Logger) component {
		return &fakeComponent{run: func(context.Context) error { return errors.New("recovery failed") }}
	}
	done := make(chan error, 1)
	go func() { done <- run(context.Background(), deps) }()
	select {
	case <-cancelled:
	case <-time.After(time.Second):
		t.Fatal("component failure did not cancel the consumer")
	}
	select {
	case err := <-done:
		t.Fatalf("run returned before the consumer drained: %v", err)
	default:
	}
	close(release)
	if err := <-done; err == nil || !strings.Contains(err.Error(), "run worker component") || db.closed != 1 {
		t.Fatalf("joined shutdown: error=%v pool closes=%d", err, db.closed)
	}
}

type blockingClosePool struct {
	fakePool
	started  chan struct{}
	release  chan struct{}
	finished chan struct{}
}

func (p *blockingClosePool) Close() {
	close(p.started)
	<-p.release
	p.fakePool.Close()
	close(p.finished)
}

func TestPoolCleanupCannotBypassOverallShutdownDeadline(t *testing.T) {
	deps, _, _ := baseDependencies(t)
	db := &blockingClosePool{started: make(chan struct{}), release: make(chan struct{}), finished: make(chan struct{})}
	deps.newPool = func(context.Context, config.Database, int) (pool, error) { return db, nil }
	deps.shutdownTimeout = 50 * time.Millisecond
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	<-started
	cancel()
	<-db.started
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pool cleanup exceeded") {
			t.Fatalf("pool cleanup error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("pool cleanup bypassed the shutdown deadline")
	}
	close(db.release)
	<-db.finished
}

func TestRunLeavesPoolOpenWhenConsumerIsNotQuiescent(t *testing.T) {
	deps, db, consumer := baseDependencies(t)
	consumer.run = func(ctx context.Context) error {
		<-ctx.Done()
		return workerruntime.ErrShutdownIncomplete
	}
	consumer.quiescent = func() bool { return false }
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "pool left open") {
			t.Fatalf("error = %v, want incomplete-shutdown pool safety error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("run did not return after incomplete shutdown")
	}
	if db.closed != 0 {
		t.Fatalf("pool closed %d times while consumer was non-quiescent", db.closed)
	}
}

func TestRunTimeoutLeavesPoolOpenWhileConsumerRunMayAdmitWork(t *testing.T) {
	deps, db, consumer := baseDependencies(t)
	release := make(chan struct{})
	runReturned := make(chan struct{})
	consumer.run = func(context.Context) error {
		defer close(runReturned)
		<-release
		return nil
	}
	consumer.quiescent = func() bool { return true }
	deps.shutdownTimeout = 25 * time.Millisecond
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "pool left open") || !strings.Contains(err.Error(), "consumer run has not returned") {
			t.Fatalf("error = %v, want timeout plus run-completion pool safety error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout did not return")
	}
	if db.closed != 0 {
		t.Fatalf("pool closed %d times while settlement may still be active", db.closed)
	}
	close(release)
	<-runReturned
}

func TestRunTimeoutLeavesPoolOpenWhileMaintenanceComponentMayBeActive(t *testing.T) {
	deps, db, _ := baseDependencies(t)
	release := make(chan struct{})
	sweeperReturned := make(chan struct{})
	deps.newSweeper = func(pool, config.IdempotencySweep, *slog.Logger) component {
		return &fakeComponent{run: func(context.Context) error {
			defer close(sweeperReturned)
			<-release
			return nil
		}}
	}
	deps.shutdownTimeout = 25 * time.Millisecond
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	<-started
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out") || !strings.Contains(err.Error(), "pool left open") || !strings.Contains(err.Error(), "database-using component has not returned") {
			t.Fatalf("error = %v, want timeout plus maintenance pool safety error", err)
		}
	case <-time.After(time.Second):
		t.Fatal("shutdown timeout did not return")
	}
	if db.closed != 0 {
		t.Fatalf("pool closed %d times while maintenance work was active", db.closed)
	}
	close(release)
	<-sweeperReturned
}

func TestRunValidatesConfigurationBeforePool(t *testing.T) {
	deps, _, _ := baseDependencies(t)
	deps.loadDeliveryConfig = func() (config.DeliveryWorker, error) { return config.DeliveryWorker{}, errors.New("invalid lease") }
	created := false
	deps.newPool = func(context.Context, config.Database, int) (pool, error) { created = true; return &fakePool{}, nil }
	if err := run(context.Background(), deps); err == nil {
		t.Fatal("invalid config accepted")
	}
	if created {
		t.Fatal("pool created after invalid configuration")
	}
}

func TestRunFailsClosedOnSecurityConfiguration(t *testing.T) {
	deps, _, _ := baseDependencies(t)
	deps.loadSecurityConfig = func() (config.DeliverySecurity, error) {
		return config.DeliverySecurity{}, errors.New("unsigned delivery may be enabled only in development")
	}
	poolCreated := false
	deps.newPool = func(context.Context, config.Database, int) (pool, error) {
		poolCreated = true
		return &fakePool{}, nil
	}
	providerCreated := false
	deps.newProvider = func(config.DeliverySecurity) (kek.Provider, error) {
		providerCreated = true
		return nil, errors.New("unexpected provider initialization")
	}
	err := run(context.Background(), deps)
	if err == nil || !strings.Contains(err.Error(), "load delivery security configuration") {
		t.Fatalf("error = %v", err)
	}
	if poolCreated || providerCreated {
		t.Fatalf("pool=%t provider=%t after invalid security configuration", poolCreated, providerCreated)
	}
}

func TestRunFailsClosedOnProviderInitialization(t *testing.T) {
	deps, _, _ := baseDependencies(t)
	deps.newProvider = func(config.DeliverySecurity) (kek.Provider, error) {
		return nil, errors.New("keyring file is missing")
	}
	poolCreated := false
	deps.newPool = func(context.Context, config.Database, int) (pool, error) {
		poolCreated = true
		return &fakePool{}, nil
	}
	consumerCreated := false
	deps.newConsumer = func(context.Context, pool, config.DeliveryWorker, config.DeliverySecurity, kek.Provider, config.RabbitMQ, *slog.Logger, *observability.Metrics) (consumerComponent, error) {
		consumerCreated = true
		return &fakeConsumer{}, nil
	}
	err := run(context.Background(), deps)
	if err == nil || !strings.Contains(err.Error(), "initialize webhook secret provider") {
		t.Fatalf("error = %v", err)
	}
	if poolCreated || consumerCreated {
		t.Fatalf("pool=%t consumer=%t after provider failure", poolCreated, consumerCreated)
	}
}
