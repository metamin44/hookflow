// Package database provides PostgreSQL connection and migration infrastructure.
package database

import (
	"context"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/platform/config"
)

// Pool owns a PostgreSQL connection pool.
type Pool struct {
	pool *pgxpool.Pool
}

// NewPoolConfig builds a pgxpool configuration from validated settings.
//
// Pool sizing and timeout values are intentionally left at pgxpool's defaults;
// the architecture does not approve custom production pool policies.
func NewPoolConfig(cfg config.Database) (*pgxpool.Config, error) {
	poolConfig, err := pgxpool.ParseConfig(connectionURL(cfg))
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL pool configuration: %w", err)
	}
	poolConfig.ConnConfig.Password = cfg.Password
	return poolConfig, nil
}

func newConnConfig(cfg config.Database) (*pgx.ConnConfig, error) {
	connConfig, err := pgx.ParseConfig(connectionURL(cfg))
	if err != nil {
		return nil, fmt.Errorf("invalid PostgreSQL connection configuration: %w", err)
	}
	connConfig.Password = cfg.Password
	return connConfig, nil
}

func connectionURL(cfg config.Database) string {
	u := &url.URL{
		Scheme: "postgres",
		User:   url.User(cfg.User),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))),
		Path:   "/" + cfg.Name,
	}
	query := u.Query()
	query.Set("sslmode", cfg.SSLMode)
	u.RawQuery = query.Encode()
	return u.String()
}

// NewPool creates a PostgreSQL pool with pgxpool defaults and verifies
// connectivity with Ping.
func NewPool(ctx context.Context, cfg config.Database) (*Pool, error) {
	return NewPoolWithSize(ctx, cfg, 0)
}

// NewPoolWithSize creates a PostgreSQL pool with an explicit maximum connection
// count. Zero preserves pgxpool defaults.
func NewPoolWithSize(ctx context.Context, cfg config.Database, maxConnections int) (*Pool, error) {
	if maxConnections < 0 {
		return nil, fmt.Errorf("PostgreSQL pool size must not be negative")
	}
	if int64(maxConnections) > math.MaxInt32 {
		return nil, fmt.Errorf("PostgreSQL pool size must fit a signed 32-bit integer")
	}
	poolConfig, err := NewPoolConfig(cfg)
	if err != nil {
		return nil, err
	}
	if maxConnections > 0 {
		poolConfig.MaxConns = int32(maxConnections)
	}

	raw, err := pgxpool.NewWithConfig(ctx, poolConfig)
	if err != nil {
		return nil, fmt.Errorf("create PostgreSQL connection pool: %w", err)
	}

	p := &Pool{pool: raw}
	if err := p.Ping(ctx); err != nil {
		p.Close()
		return nil, fmt.Errorf("verify PostgreSQL connection: %w", err)
	}
	return p, nil
}

// Ping verifies that the pool can reach PostgreSQL.
func (p *Pool) Ping(ctx context.Context) error {
	return p.pool.Ping(ctx)
}

// Close releases the pool and all of its connections.
func (p *Pool) Close() {
	p.pool.Close()
}

// Raw returns the underlying pgxpool.Pool for future repositories and health checks.
func (p *Pool) Raw() *pgxpool.Pool {
	return p.pool
}
