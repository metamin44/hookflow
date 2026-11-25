package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
)

type fakePool struct {
	pingFn     func(context.Context) error
	pingErr    error
	closeCount atomic.Int32
}

func (p *fakePool) Ping(ctx context.Context) error {
	if p.pingFn != nil {
		return p.pingFn(ctx)
	}
	return p.pingErr
}

func (p *fakePool) Close() {
	p.closeCount.Add(1)
}

func testDatabaseConfig() config.Config {
	return config.Config{
		Database: config.Database{
			Name:     "hookflow_dev",
			User:     "hookflow",
			Password: "unit-test-password",
			Host:     "127.0.0.1",
			Port:     5433,
			SSLMode:  "disable",
		},
	}
}

func happyDependencies(p *fakePool) dependencies {
	return dependencies{
		loadConfig: func() (config.Config, error) {
			return testDatabaseConfig(), nil
		},
		loadAPIConfig: func() (config.API, error) {
			return config.API{Addr: "127.0.0.1:0"}, nil
		},
		loadDeliverySecurity: func() (config.DeliverySecurity, error) {
			return config.DeliverySecurity{Mode: config.ModeDevelopment}, nil
		},
		loadEndpointLimits: func() (config.EndpointLimits, error) {
			return config.EndpointLimits{MaxActivePerOrganization: 100}, nil
		},
		newManagement: func(pool, config.DeliverySecurity, config.EndpointLimits, *slog.Logger) (managementRoutes, error) {
			return func(*http.ServeMux) {}, nil
		},
		newPool: func(context.Context, config.Database) (pool, error) {
			return p, nil
		},
		listen:          net.Listen,
		logOutput:       io.Discard,
		shutdownTimeout: time.Second,
	}
}

func TestRunDatabaseConfigurationFailure(t *testing.T) {
	sentinel := errors.New("database configuration failed")
	poolCalled := false
	deps := dependencies{
		loadConfig: func() (config.Config, error) {
			return config.Config{}, sentinel
		},
		loadAPIConfig: func() (config.API, error) {
			t.Fatal("API configuration was loaded after database failure")
			return config.API{}, nil
		},
		newPool: func(context.Context, config.Database) (pool, error) {
			poolCalled = true
			return &fakePool{}, nil
		},
	}

	err := run(context.Background(), deps)
	if !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want wrapped sentinel", err)
	}
	if poolCalled {
		t.Fatal("database pool was created after configuration failure")
	}
}

func TestRunAPIConfigurationFailure(t *testing.T) {
	sentinel := errors.New("API configuration failed")
	poolCalled := false
	deps := happyDependencies(&fakePool{})
	deps.loadAPIConfig = func() (config.API, error) {
		return config.API{}, sentinel
	}
	deps.newPool = func(context.Context, config.Database) (pool, error) {
		poolCalled = true
		return &fakePool{}, nil
	}

	err := run(context.Background(), deps)
	if !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want wrapped sentinel", err)
	}
	if poolCalled {
		t.Fatal("database pool was created after API configuration failure")
	}
}

func TestRunDeliverySecurityConfigurationFailure(t *testing.T) {
	sentinel := errors.New("security configuration failed")
	deps := happyDependencies(&fakePool{})
	deps.loadDeliverySecurity = func() (config.DeliverySecurity, error) {
		return config.DeliverySecurity{}, sentinel
	}
	deps.newPool = func(context.Context, config.Database) (pool, error) {
		t.Fatal("database pool was created after a security configuration failure")
		return nil, nil
	}

	err := run(context.Background(), deps)
	if !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want wrapped sentinel", err)
	}
}

func TestRunManagementInitializationFailureClosesPool(t *testing.T) {
	sentinel := errors.New("management initialization failed")
	p := &fakePool{}
	deps := happyDependencies(p)
	deps.newManagement = func(pool, config.DeliverySecurity, config.EndpointLimits, *slog.Logger) (managementRoutes, error) {
		return nil, sentinel
	}
	deps.listen = func(string, string) (net.Listener, error) {
		t.Fatal("listener was opened after a management initialization failure")
		return nil, nil
	}

	err := run(context.Background(), deps)
	if !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want wrapped sentinel", err)
	}
	if got := p.closeCount.Load(); got != 1 {
		t.Fatalf("pool Close() calls = %d, want 1", got)
	}
}

func TestRunDatabaseInitializationFailure(t *testing.T) {
	sentinel := errors.New("database initialization failed")
	deps := happyDependencies(&fakePool{})
	deps.newPool = func(context.Context, config.Database) (pool, error) {
		return nil, sentinel
	}
	deps.listen = func(string, string) (net.Listener, error) {
		t.Fatal("listener was opened after database initialization failure")
		return nil, nil
	}

	err := run(context.Background(), deps)
	if !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want wrapped sentinel", err)
	}
}

func TestRunListenFailureClosesPool(t *testing.T) {
	sentinel := errors.New("listen failed")
	p := &fakePool{}
	deps := happyDependencies(p)
	deps.listen = func(string, string) (net.Listener, error) {
		return nil, sentinel
	}

	err := run(context.Background(), deps)
	if !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want wrapped sentinel", err)
	}
	if got := p.closeCount.Load(); got != 1 {
		t.Fatalf("pool Close() calls = %d, want 1", got)
	}
}

func TestRunStartsServesAndShutsDown(t *testing.T) {
	p := &fakePool{}
	var logs bytes.Buffer
	deps := happyDependencies(p)
	deps.logOutput = &logs
	started := make(chan string, 1)
	deps.onStarted = func(listener net.Listener) {
		started <- listener.Addr().String()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, deps)
	}()

	addr := waitForStart(t, started, done)
	client := &http.Client{Timeout: 2 * time.Second}

	for _, path := range []string{"/health/live", "/health/ready"} {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, want 200", path, resp.StatusCode)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run() did not stop after context cancellation")
	}

	if got := p.closeCount.Load(); got != 1 {
		t.Fatalf("pool Close() calls = %d, want 1", got)
	}
	for _, message := range []string{"starting API", "HTTP server listening", "shutdown completed"} {
		if !strings.Contains(logs.String(), message) {
			t.Fatalf("logs do not contain %q: %s", message, logs.String())
		}
	}
	if strings.Contains(logs.String(), "unit-test-password") {
		t.Fatalf("logs contain the database password: %s", logs.String())
	}
}

func TestRunShutdownTimeoutForcesClose(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	finished := make(chan struct{})
	var enteredOnce, finishedOnce sync.Once

	p := &fakePool{
		pingFn: func(context.Context) error {
			enteredOnce.Do(func() { close(entered) })
			<-release
			finishedOnce.Do(func() { close(finished) })
			return nil
		},
	}
	deps := happyDependencies(p)
	deps.shutdownTimeout = 50 * time.Millisecond
	started := make(chan string, 1)
	deps.onStarted = func(listener net.Listener) {
		started <- listener.Addr().String()
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, deps)
	}()

	addr := waitForStart(t, started, done)

	clientDone := make(chan struct{})
	go func() {
		defer close(clientDone)
		client := &http.Client{Timeout: 2 * time.Second}
		resp, err := client.Get("http://" + addr + "/health/ready")
		if err == nil {
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
		}
	}()

	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("readiness handler did not begin")
	}

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("run() succeeded, want a shutdown timeout error")
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("run() error = %v, want a wrapped deadline error", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not stop after shutdown timeout")
	}

	close(release)
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked readiness handler did not finish")
	}
	select {
	case <-clientDone:
	case <-time.After(2 * time.Second):
		t.Fatal("HTTP client did not finish")
	}

	if got := p.closeCount.Load(); got != 1 {
		t.Fatalf("pool Close() calls = %d, want 1", got)
	}
}

func waitForStart(t *testing.T, started <-chan string, done <-chan error) string {
	t.Helper()
	select {
	case addr := <-started:
		return addr
	case err := <-done:
		t.Fatalf("run() exited before starting: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("API server did not start")
	}
	return ""
}
