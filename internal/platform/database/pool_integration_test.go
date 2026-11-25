//go:build integration

package database

import (
	"context"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/config"
)

func integrationConfig(t *testing.T) config.Database {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}
	return cfg.Database
}

func TestPoolIntegration(t *testing.T) {
	dbcfg := integrationConfig(t)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	pool, err := NewPool(ctx, dbcfg)
	if err != nil {
		t.Fatalf("NewPool() error: %v", err)
	}
	defer pool.Close()

	var version int
	var dirty bool
	if err := pool.Raw().QueryRow(ctx, "SELECT version, dirty FROM schema_migrations LIMIT 1").Scan(&version, &dirty); err != nil {
		t.Fatalf("read schema_migrations: %v", err)
	}
	if version != 13 || dirty {
		t.Fatalf("migration state = (version %d, dirty %t), want (13, false)", version, dirty)
	}

	var tableCount int
	if err := pool.Raw().QueryRow(ctx, "SELECT count(*) FROM information_schema.tables WHERE table_schema = 'public' AND table_name NOT IN ('schema_migrations', 'schema_lock')").Scan(&tableCount); err != nil {
		t.Fatalf("count application tables: %v", err)
	}
	if tableCount != 13 {
		t.Fatalf("application table count = %d, want 13", tableCount)
	}
}
