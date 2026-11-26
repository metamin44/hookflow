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

	"github.com/metamin44/hookflow/internal/modules/event"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

type fakePool struct {
	pingFn     func(context.Context) error
	pingErr    error
	closeCount atomic.Int32
}

type trackingListener struct {
	net.Listener
	closed atomic.Bool
}

func (l *trackingListener) Close() error {
	l.closed.Store(true)
	return l.Listener.Close()
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
		newManagement: func(pool, config.DeliverySecurity, config.EndpointLimits, *observability.Metrics, *slog.Logger) (managementRoutes, error) {
			return func(*http.ServeMux) {}, nil
		},
		newPool: func(context.Context, config.Database) (pool, error) {
			return p, nil
		},
		loadMetricsAddr: func() (string, error) { return "", nil },
		startMetrics: func(ctx context.Context, address string, metrics *observability.Metrics, logger *slog.Logger) (metricsServer, error) {
			return observability.StartServer(ctx, address, metrics, logger)
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
	deps.newManagement = func(pool, config.DeliverySecurity, config.EndpointLimits, *observability.Metrics, *slog.Logger) (managementRoutes, error) {
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

func TestRunMetricsConfigurationFailurePrecedesPool(t *testing.T) {
	sentinel := errors.New("invalid metrics address")
	deps := happyDependencies(&fakePool{})
	deps.loadMetricsAddr = func() (string, error) { return "", sentinel }
	deps.newPool = func(context.Context, config.Database) (pool, error) {
		t.Fatal("database pool was created after invalid metrics configuration")
		return nil, nil
	}
	if err := run(context.Background(), deps); !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want metrics configuration sentinel", err)
	}
}

func TestRunMetricsStartupFailureClosesListenerAndPool(t *testing.T) {
	sentinel := errors.New("metrics bind failed")
	p := &fakePool{}
	deps := happyDependencies(p)
	deps.loadMetricsAddr = func() (string, error) { return "127.0.0.1:0", nil }
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	tracked := &trackingListener{Listener: listener}
	deps.listen = func(string, string) (net.Listener, error) { return tracked, nil }
	deps.startMetrics = func(context.Context, string, *observability.Metrics, *slog.Logger) (metricsServer, error) {
		return nil, sentinel
	}
	deps.onStarted = func(net.Listener) { t.Error("API started after metrics startup failure") }
	if err := run(context.Background(), deps); !errors.Is(err, sentinel) {
		t.Fatalf("run() error = %v, want metrics startup sentinel", err)
	}
	if !tracked.closed.Load() {
		t.Fatal("API listener remained open after metrics startup failure")
	}
	if got := p.closeCount.Load(); got != 1 {
		t.Fatalf("pool Close() calls = %d, want 1", got)
	}
}

func TestRunDoesNotStartMetricsWhenDisabled(t *testing.T) {
	deps := happyDependencies(&fakePool{})
	var metricsStartCalled atomic.Bool
	deps.startMetrics = func(context.Context, string, *observability.Metrics, *slog.Logger) (metricsServer, error) {
		metricsStartCalled.Store(true)
		return nil, nil
	}
	apiStarted := make(chan string, 1)
	deps.onStarted = func(listener net.Listener) { apiStarted <- listener.Addr().String() }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	waitForStart(t, apiStarted, done)
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("run() error: %v", err)
	}
	if metricsStartCalled.Load() {
		t.Fatal("metrics listener started while disabled")
	}
}

func TestRunExposesAPIListenerMetricsWithoutPublicRoute(t *testing.T) {
	p := &fakePool{}
	deps := happyDependencies(p)
	deps.loadMetricsAddr = func() (string, error) { return "127.0.0.1:0", nil }
	metricsSeen := make(chan *observability.Metrics, 1)
	deps.newManagement = func(_ pool, _ config.DeliverySecurity, _ config.EndpointLimits, metrics *observability.Metrics, _ *slog.Logger) (managementRoutes, error) {
		metricsSeen <- metrics
		if metrics == nil {
			return nil, errors.New("nil metrics registry")
		}
		metrics.Add(event.InexactNumberMetric, 2)
		return func(*http.ServeMux) {}, nil
	}
	apiStarted := make(chan string, 1)
	metricsStarted := make(chan string, 1)
	deps.onStarted = func(listener net.Listener) { apiStarted <- listener.Addr().String() }
	deps.onMetricsStarted = func(address string) { metricsStarted <- address }

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	apiAddr := waitForStart(t, apiStarted, done)
	var metricsAddr string
	select {
	case metricsAddr = <-metricsStarted:
	case err := <-done:
		t.Fatalf("run exited before metrics startup: %v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("metrics listener did not start")
	}
	select {
	case metrics := <-metricsSeen:
		if metrics == nil {
			t.Fatal("management composition received a nil metrics registry")
		}
	case <-time.After(time.Second):
		t.Fatal("management composition did not receive metrics")
	}

	client := &http.Client{Timeout: time.Second}
	response, err := client.Get("http://" + metricsAddr + "/metrics")
	if err != nil {
		t.Fatalf("scrape API metrics: %v", err)
	}
	body, _ := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusOK || !strings.Contains(string(body), event.InexactNumberMetric+" 2") {
		t.Fatalf("metrics response = %d %q", response.StatusCode, body)
	}
	if strings.Contains(string(body), "{") {
		t.Fatalf("metrics response contains labels: %q", body)
	}
	response, err = client.Get("http://" + apiAddr + "/metrics")
	if err != nil {
		t.Fatalf("probe public API metrics route: %v", err)
	}
	_, _ = io.Copy(io.Discard, response.Body)
	_ = response.Body.Close()
	if response.StatusCode != http.StatusNotFound {
		t.Fatalf("public API /metrics status = %d, want 404", response.StatusCode)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("run did not stop after cancellation")
	}
	if response, err := client.Get("http://" + metricsAddr + "/metrics"); err == nil {
		_, _ = io.Copy(io.Discard, response.Body)
		_ = response.Body.Close()
		t.Fatal("metrics listener remained reachable after shutdown")
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
