//go:build integration

package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
)

func TestAPIIntegration(t *testing.T) {
	if _, err := config.Load(); err != nil {
		t.Skipf("integration environment not configured: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	deps := defaultDependencies()
	deps.loadAPIConfig = func() (config.API, error) {
		return config.API{Addr: "127.0.0.1:0"}, nil
	}
	deps.logOutput = io.Discard
	started := make(chan string, 1)
	deps.onStarted = func(listener net.Listener) {
		started <- listener.Addr().String()
	}

	done := make(chan error, 1)
	go func() {
		done <- run(ctx, deps)
	}()

	var addr string
	select {
	case addr = <-started:
	case err := <-done:
		t.Fatalf("run() exited before starting: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("API did not start")
	}

	client := &http.Client{Timeout: 3 * time.Second}
	for _, path := range []string{"/health/live", "/health/ready"} {
		resp, err := client.Get("http://" + addr + path)
		if err != nil {
			t.Fatalf("GET %s: %v", path, err)
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s status = %d, body = %s", path, resp.StatusCode, body)
		}
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("API did not stop")
	}
}
