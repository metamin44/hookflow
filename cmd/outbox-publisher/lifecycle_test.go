package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

type fakePool struct{ closed int }

func (p *fakePool) Ping(context.Context) error { return nil }
func (p *fakePool) Close()                     { p.closed++ }
func (p *fakePool) Raw() *pgxpool.Pool         { return nil }

type fakeRelay struct {
	start  func(context.Context) error
	run    func(context.Context) error
	closed int
}

func (r *fakeRelay) Start(ctx context.Context) error {
	if r.start != nil {
		return r.start(ctx)
	}
	return nil
}
func (r *fakeRelay) Run(ctx context.Context) error {
	if r.run != nil {
		return r.run(ctx)
	}
	<-ctx.Done()
	return nil
}
func (r *fakeRelay) Close() { r.closed++ }

func testDependencies(t *testing.T) (dependencies, *fakePool, *fakeRelay) {
	t.Helper()
	db := &fakePool{}
	relay := &fakeRelay{}
	deps := dependencies{
		loadConfig: func() (config.Config, error) { return config.Config{}, nil },
		loadPublisher: func() (config.Publisher, error) {
			return config.Publisher{BatchSize: 1, MaxClaims: 1, LeaseDuration: 30 * time.Second, PollInterval: time.Second, ReapInterval: time.Second, BackoffBase: time.Second, BackoffCap: time.Second, BackoffMin: time.Millisecond, ConfirmTimeout: time.Second}, nil
		},
		loadRabbitMQ: func() (config.RabbitMQ, error) {
			return config.RabbitMQ{URI: "amqp://user:password@localhost:5672/", ManagementURL: "http://localhost:15672", VHost: "/", Heartbeat: time.Second, ConnectTimeout: time.Second, ReconnectMinBackoff: time.Millisecond, ReconnectMaxBackoff: time.Second}, nil
		},
		loadMetricsAddr: func() (string, error) { return "", nil },
		newPool:         func(context.Context, config.Database) (pool, error) { return db, nil },
		newRelay: func(context.Context, pool, config.Publisher, config.RabbitMQ, string, *slog.Logger, *observability.Metrics) (relayRuntime, error) {
			return relay, nil
		},
		logOutput: io.Discard, shutdownTimeout: time.Second,
	}
	return deps, db, relay
}

func TestRunStartsAndStops(t *testing.T) {
	deps, db, relay := testDependencies(t)
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
	if db.closed != 1 || relay.closed != 1 {
		t.Fatalf("closed db=%d relay=%d", db.closed, relay.closed)
	}
}

func TestRunFailsClosedOnStartupError(t *testing.T) {
	deps, db, relay := testDependencies(t)
	deps.newRelay = func(context.Context, pool, config.Publisher, config.RabbitMQ, string, *slog.Logger, *observability.Metrics) (relayRuntime, error) {
		return nil, errors.New("topology mismatch")
	}
	if err := run(context.Background(), deps); err == nil {
		t.Fatal("run succeeded after startup failure")
	}
	if db.closed != 1 || relay.closed != 0 {
		t.Fatalf("closed db=%d relay=%d", db.closed, relay.closed)
	}
}

func TestRunReturnsRelayError(t *testing.T) {
	deps, _, _ := testDependencies(t)
	deps.newRelay = func(context.Context, pool, config.Publisher, config.RabbitMQ, string, *slog.Logger, *observability.Metrics) (relayRuntime, error) {
		return &fakeRelay{run: func(context.Context) error { return errors.New("loop failed") }}, nil
	}
	if err := run(context.Background(), deps); err == nil {
		t.Fatal("run succeeded despite relay error")
	}
}
