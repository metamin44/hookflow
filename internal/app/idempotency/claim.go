package idempotency

import (
	"bytes"
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Stable claim outcomes.
var (
	// ErrConflict reports an unexpired record whose request_hash differs.
	ErrConflict = errors.New("idempotency key conflict")
	// ErrClaimLost reports a claim row that disappeared twice: once between the
	// failed INSERT and the lookup, and again after the single bounded retry.
	ErrClaimLost = errors.New("idempotency claim lost")
)

// Querier is satisfied by pgx.Tx and pgxpool.Pool.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// ClaimInput is one organization/key claim.
type ClaimInput struct {
	// RecordID is the application-generated UUIDv7 of a fresh claim.
	RecordID       string
	OrganizationID string
	Key            string
	Hash           []byte
}

// Resolution reports one of the three approved outcomes: a fresh claim owned by
// this request (Owner), a replay of an unexpired identical request
// (ReplayEventID set), or ErrConflict.
type Resolution struct {
	RecordID      string
	Owner         bool
	ReplayEventID string
}

// claimSQL is the approved claim-first statement. It writes no event and links
// nothing; the deferred trigger refuses a commit that leaves event_id NULL.
const claimSQL = `
INSERT INTO idempotency_records
    (id, organization_id, idempotency_key, request_hash, hash_algorithm,
     event_id, created_at, expires_at)
VALUES ($1::uuid, $2::uuid, $3, $4, 'sha256', NULL, now(), now() + interval '72 hours')
ON CONFLICT (organization_id, idempotency_key) DO NOTHING
RETURNING id::text`

const resolveSQL = `
SELECT id::text, COALESCE(event_id::text, ''), request_hash, expires_at <= now()
  FROM idempotency_records
 WHERE organization_id = $1::uuid
   AND idempotency_key = $2
 FOR UPDATE`

const takeoverSQL = `
UPDATE idempotency_records
   SET request_hash = $2,
       event_id = NULL,
       created_at = now(),
       expires_at = now() + interval '72 hours'
 WHERE id = $1::uuid
RETURNING 1`

// maxClaimAttempts is the approved single bounded retry for a claim row the
// sweeper deleted between the failed INSERT and the locked lookup.
const maxClaimAttempts = 2

// Claim claims or resolves the idempotency record inside the caller's
// transaction. Expired records are reused in place: the same row id is kept,
// the hash is replaced, event_id is cleared and the 72-hour window restarts
// from database time.
func Claim(ctx context.Context, q Querier, input ClaimInput) (Resolution, error) {
	if len(input.Hash) != sha256Size {
		return Resolution{}, errors.New("idempotency request hash must be 32 bytes")
	}

	for attempt := 0; attempt < maxClaimAttempts; attempt++ {
		var recordID string
		err := q.QueryRow(ctx, claimSQL,
			input.RecordID, input.OrganizationID, input.Key, input.Hash,
		).Scan(&recordID)
		switch {
		case err == nil:
			return Resolution{RecordID: recordID, Owner: true}, nil
		case !errors.Is(err, pgx.ErrNoRows):
			return Resolution{}, fmt.Errorf("claim idempotency record: %w", err)
		}

		var (
			existingID string
			eventID    string
			storedHash []byte
			expired    bool
		)
		err = q.QueryRow(ctx, resolveSQL, input.OrganizationID, input.Key).
			Scan(&existingID, &eventID, &storedHash, &expired)
		if errors.Is(err, pgx.ErrNoRows) {
			// The sweeper removed the expired record between the failed claim
			// and this lookup; retry the claim exactly once.
			continue
		}
		if err != nil {
			return Resolution{}, fmt.Errorf("resolve idempotency record: %w", err)
		}

		if !expired {
			if !bytes.Equal(storedHash, input.Hash) {
				return Resolution{}, ErrConflict
			}
			if eventID == "" {
				return Resolution{}, fmt.Errorf("idempotency record %s committed without an event link", existingID)
			}
			return Resolution{RecordID: existingID, ReplayEventID: eventID}, nil
		}

		var marker int
		if err := q.QueryRow(ctx, takeoverSQL, existingID, input.Hash).Scan(&marker); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return Resolution{}, ErrClaimLost
			}
			return Resolution{}, fmt.Errorf("reuse expired idempotency record: %w", err)
		}
		return Resolution{RecordID: existingID, Owner: true}, nil
	}
	return Resolution{}, ErrClaimLost
}

// sha256Size is the fixed request_hash length.
const sha256Size = 32

const linkSQL = `
UPDATE idempotency_records
   SET event_id = $2::uuid
 WHERE id = $1::uuid`

// AppendLink queues the U44 event link. The same transaction must contain the
// Event insert; the deferred trg_idem_linked fails the commit otherwise.
func AppendLink(batch *pgx.Batch, recordID, eventID string) {
	batch.Queue(linkSQL, recordID, eventID)
}
