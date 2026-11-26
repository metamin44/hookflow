// Package eventapp implements the approved Phase 7 ingestion use case.
//
// AcceptEvent owns ONE Read Committed transaction: organization share lock,
// idempotency claim or replay, exact destination match, Event insert, the U44
// event link, immutable Delivery snapshots and the initial Outbox rows. No
// broker publish, HTTP call or secret-provider operation happens inside it.
package eventapp

import (
	"context"
	"errors"
	"log/slog"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

// Database is the pool surface ingestion needs. *pgxpool.Pool satisfies it.
type Database interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
}

// Options are explicit service dependencies.
type Options struct {
	// NewID supplies application-generated UUIDv7 entity identifiers.
	NewID func() (string, error)
	// Metrics receives the approved inexact-number counter; nil is safe.
	Metrics *observability.Metrics
	Logger  *slog.Logger
}

// Service is the ingestion application service.
type Service struct {
	db      Database
	options Options
	logger  *slog.Logger
}

// NewService validates the ingestion dependencies.
func NewService(db Database, options Options) (*Service, error) {
	if db == nil {
		return nil, errors.New("eventapp: database pool is required")
	}
	if options.NewID == nil {
		options.NewID = id.NewV7
	}
	logger := options.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{db: db, options: options, logger: logger}, nil
}
