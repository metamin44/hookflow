package event

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound reports a missing Event row.
var ErrNotFound = errors.New("event not found")

// Querier is satisfied by pgx.Tx and pgxpool.Pool.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// insertEventSQL freezes received_at and the 90-day expiry from database time.
// The 202 body reads received_at from RETURNING, so a replay can reproduce the
// original bytes without consulting the clock.
const insertEventSQL = `
INSERT INTO events
    (id, organization_id, type, payload, destination_count, received_at, expires_at)
VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, now(), now() + interval '90 days')
RETURNING received_at`

// AppendInsert queues the Event row inside the acceptance batch. The caller
// reads received_at from the batch result.
func AppendInsert(batch *pgx.Batch, record Record) {
	batch.Queue(insertEventSQL,
		record.ID, record.OrganizationID, record.Type, record.Payload, record.DestinationCount,
	)
}

const loadEventSQL = `
SELECT id::text, organization_id::text, type, destination_count, received_at
  FROM events
 WHERE organization_id = $1::uuid
   AND id = $2::uuid`

// Load reads the frozen replay columns of an accepted Event.
func Load(ctx context.Context, q Querier, organizationID, eventID string) (Record, error) {
	var record Record
	err := q.QueryRow(ctx, loadEventSQL, organizationID, eventID).Scan(
		&record.ID, &record.OrganizationID, &record.Type, &record.DestinationCount, &record.ReceivedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("load event: %w", err)
	}
	return record, nil
}
