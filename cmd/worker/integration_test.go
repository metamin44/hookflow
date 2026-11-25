//go:build integration

package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/kek"
)

// testIntegrationSecurity writes a development keyring outside any repository
// path and returns the matching non-secret security configuration. The ring
// holds a throwaway 32-byte key generated for this test only.
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

func TestWorkerIntegrationStartupAndShutdown(t *testing.T) {
	if _, err := config.LoadRabbitMQ(); err != nil {
		t.Skipf("integration RabbitMQ configuration unavailable: %v", err)
	}
	security := testIntegrationSecurity(t)
	deps := defaultDependencies()
	deps.loadWorkerConfig = func() (config.Worker, error) {
		return config.Worker{IdempotencySweep: config.IdempotencySweep{BatchSize: 1, Interval: time.Hour, MaxBatchesPerTick: 1}}, nil
	}
	deps.loadSecurityConfig = func() (config.DeliverySecurity, error) { return security, nil }
	deps.logOutput = io.Discard
	started := make(chan struct{})
	deps.onStarted = func() { close(started) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- run(ctx, deps) }()
	select {
	case <-started:
	case err := <-done:
		t.Fatalf("worker exited before starting: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("worker did not start")
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("worker shutdown error: %v", err)
		}
	case <-time.After(40 * time.Second):
		t.Fatal("worker did not stop")
	}
}
