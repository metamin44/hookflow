package idempotency

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Beginner is satisfied by *pgxpool.Pool and by pgx.Tx. Accepting the
// interface keeps the store testable inside a rolled-back transaction.
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// PostgresStore deletes expired idempotency records from PostgreSQL.
type PostgresStore struct {
	db Beginner
}

// NewPostgresStore creates a store over the shared pool (or a transaction in
// tests).
func NewPostgresStore(db Beginner) *PostgresStore {
	return &PostgresStore{db: db}
}

// deleteExpiredBatchSQL is the approved U43 shape: a locking CTE bounds the
// row set, and DELETE ... USING removes exactly those rows. expires_at is
// compared by the database clock, never application time.
const deleteExpiredBatchSQL = `
WITH picked AS (
    SELECT id
      FROM idempotency_records
     WHERE expires_at <= now()
     ORDER BY expires_at
     FOR UPDATE SKIP LOCKED
     LIMIT $1
)
DELETE FROM idempotency_records r
USING picked p
WHERE r.id = p.id
RETURNING r.id`

// DeleteExpiredBatch removes at most limit expired records in one transaction.
func (s *PostgresStore) DeleteExpiredBatch(ctx context.Context, limit int) (int, error) {
	if limit <= 0 {
		return 0, fmt.Errorf("idempotency sweep batch size must be positive")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin idempotency sweep transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, deleteExpiredBatchSQL, limit)
	if err != nil {
		return 0, fmt.Errorf("delete expired idempotency records: %w", err)
	}

	deleted := 0
	for rows.Next() {
		var id pgtype.UUID
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return 0, fmt.Errorf("scan deleted idempotency record: %w", err)
		}
		deleted++
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return 0, fmt.Errorf("read deleted idempotency records: %w", err)
	}
	rows.Close()

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit idempotency sweep batch: %w", err)
	}
	return deleted, nil
}
