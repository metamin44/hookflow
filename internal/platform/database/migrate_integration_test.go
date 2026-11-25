//go:build integration

package database

import (
	"context"
	"testing"
	"time"
)

func TestMigrationRunnerIntegration(t *testing.T) {
	dbcfg := integrationConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	runner, err := NewRunner(ctx, dbcfg, "")
	if err != nil {
		t.Fatalf("NewRunner() error: %v", err)
	}
	defer func() {
		if closeErr := runner.Close(); closeErr != nil {
			t.Errorf("Close() error: %v", closeErr)
		}
	}()

	version, dirty, err := runner.Version()
	if err != nil {
		t.Fatalf("Version() error: %v", err)
	}
	if version != 13 || dirty {
		t.Fatalf("migration state = (version %d, dirty %t), want (13, false)", version, dirty)
	}

	changed, err := runner.Up()
	if err != nil {
		t.Fatalf("Up() error: %v", err)
	}
	if changed {
		t.Fatal("Up() applied migrations unexpectedly; the database should already be at version 13")
	}
}
