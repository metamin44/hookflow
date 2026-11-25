package database

import (
	"context"
	"math"
	"strings"
	"testing"

	"github.com/metamin44/hookflow/internal/platform/config"
)

func testDatabaseConfig() config.Database {
	return config.Database{
		Name:     "hookflow_dev",
		User:     "hookflow",
		Password: "unit-test-password",
		Host:     "127.0.0.1",
		Port:     5433,
		SSLMode:  "disable",
	}
}

func TestNewPoolConfig(t *testing.T) {
	cfg := testDatabaseConfig()

	poolConfig, err := NewPoolConfig(cfg)
	if err != nil {
		t.Fatalf("NewPoolConfig() error: %v", err)
	}

	if poolConfig.ConnConfig.Host != cfg.Host {
		t.Fatalf("host = %q, want %q", poolConfig.ConnConfig.Host, cfg.Host)
	}
	if poolConfig.ConnConfig.Port != cfg.Port {
		t.Fatalf("port = %d, want %d", poolConfig.ConnConfig.Port, cfg.Port)
	}
	if poolConfig.ConnConfig.Database != cfg.Name {
		t.Fatalf("database = %q, want %q", poolConfig.ConnConfig.Database, cfg.Name)
	}
	if poolConfig.ConnConfig.User != cfg.User {
		t.Fatalf("user = %q, want %q", poolConfig.ConnConfig.User, cfg.User)
	}
	if poolConfig.ConnConfig.Password != cfg.Password {
		t.Fatal("database password was not applied to the pool configuration")
	}
	if poolConfig.ConnConfig.TLSConfig != nil {
		t.Fatal("sslmode=disable unexpectedly produced a TLS configuration")
	}
	if poolConfig.MaxConns == 0 {
		t.Fatal("pgxpool default MaxConns was not applied")
	}
	if poolConfig.MinConns != 0 {
		t.Fatalf("MinConns = %d, want pgxpool default 0", poolConfig.MinConns)
	}
}

func TestPoolSizeValidation(t *testing.T) {
	if _, err := NewPoolWithSize(context.Background(), testDatabaseConfig(), -1); err == nil {
		t.Fatal("negative pool size accepted")
	}
	if _, err := NewPoolWithSize(context.Background(), testDatabaseConfig(), math.MaxInt32+1); err == nil {
		t.Fatal("pool size that overflows pgx int32 was accepted")
	}
}

func TestNewPoolConfigRequiresTLS(t *testing.T) {
	cfg := testDatabaseConfig()
	cfg.SSLMode = "require"

	poolConfig, err := NewPoolConfig(cfg)
	if err != nil {
		t.Fatalf("NewPoolConfig() error: %v", err)
	}
	if poolConfig.ConnConfig.TLSConfig == nil {
		t.Fatal("sslmode=require did not produce a TLS configuration")
	}
}

func TestNewConnConfig(t *testing.T) {
	cfg := testDatabaseConfig()

	connConfig, err := newConnConfig(cfg)
	if err != nil {
		t.Fatalf("newConnConfig() error: %v", err)
	}
	if connConfig.Host != cfg.Host || connConfig.Port != cfg.Port || connConfig.Database != cfg.Name || connConfig.User != cfg.User {
		t.Fatal("connection configuration does not match validated settings")
	}
	if connConfig.Password != cfg.Password {
		t.Fatal("database password was not applied to the connection configuration")
	}
}

func TestConnectionURLOmitsPassword(t *testing.T) {
	cfg := testDatabaseConfig()

	connectionString := connectionURL(cfg)
	if strings.Contains(connectionString, cfg.Password) {
		t.Fatal("connection URL contains the database password")
	}
	if !strings.Contains(connectionString, "sslmode=disable") {
		t.Fatal("connection URL does not include the SSL mode")
	}
}

func TestNewPoolConfigErrorDoesNotLeakPassword(t *testing.T) {
	cfg := testDatabaseConfig()
	cfg.SSLMode = "bogus"

	_, err := NewPoolConfig(cfg)
	if err == nil {
		t.Fatal("NewPoolConfig() accepted an invalid SSL mode")
	}
	if strings.Contains(err.Error(), cfg.Password) {
		t.Fatal("pool configuration error contains the database password")
	}
}

func TestNewConnConfigErrorDoesNotLeakPassword(t *testing.T) {
	cfg := testDatabaseConfig()
	cfg.SSLMode = "bogus"

	_, err := newConnConfig(cfg)
	if err == nil {
		t.Fatal("newConnConfig() accepted an invalid SSL mode")
	}
	if strings.Contains(err.Error(), cfg.Password) {
		t.Fatal("connection configuration error contains the database password")
	}
}
