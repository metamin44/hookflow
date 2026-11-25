//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/kek"
)

// testIntegrationSecurity writes a development keyring outside any repository
// path and returns the matching non-secret security configuration. The ring
// holds a throwaway 32-byte key generated for this test only, so the test does
// not depend on whatever deployment configuration the environment exports.
func testIntegrationSecurity(t *testing.T) config.DeliverySecurity {
	t.Helper()
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i + 1)
	}
	ring, err := json.Marshal(map[string]any{
		"current": "kek-integration",
		"keys":    map[string]string{"kek-integration": base64.RawURLEncoding.EncodeToString(key)},
	})
	if err != nil {
		t.Fatalf("encode integration keyring: %v", err)
	}
	path := filepath.Join(t.TempDir(), "integration-keyring.json")
	if err := os.WriteFile(path, ring, 0o600); err != nil {
		t.Fatalf("write integration keyring: %v", err)
	}
	return config.DeliverySecurity{
		Mode:                  config.ModeDevelopment,
		AllowUnsignedDelivery: true,
		Provider:              kek.ProviderLocalFile,
		KeyringPath:           path,
		CurrentKID:            "kek-integration",
		AllowedKIDs:           []string{"kek-integration"},
		AllowedProviders:      []string{kek.ProviderLocalFile},
	}
}

func TestAPIIntegration(t *testing.T) {
	if _, err := config.Load(); err != nil {
		t.Skipf("integration environment not configured: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	security := testIntegrationSecurity(t)
	deps := defaultDependencies()
	deps.loadAPIConfig = func() (config.API, error) {
		return config.API{Addr: "127.0.0.1:0"}, nil
	}
	deps.loadDeliverySecurity = func() (config.DeliverySecurity, error) {
		return security, nil
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
