package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrRetrySourceNotFound reports a missing or foreign retry source. The caller
// renders it as the same not-found response for both cases.
var ErrRetrySourceNotFound = errors.New("delivery retry source not found")

// ErrRetryRequestNotFound reports an absent retry idempotency record.
var ErrRetryRequestNotFound = errors.New("delivery retry request not found")

// ErrRetryRequestConflict reports a duplicate key that could not be claimed.
var ErrRetryRequestConflict = errors.New("delivery retry request conflict")

// RetryQuerier is satisfied by pgx.Tx and pgxpool.Pool.
type RetryQuerier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// RetrySourceIdentity is the unlocked lookup used to acquire the endpoint lock
// before locking the delivery itself. The lineage columns are immutable.
type RetrySourceIdentity struct {
	DeliveryID string
	EventID    string
	EndpointID string
}

// RetrySource is the locked source Delivery and its Event expiry.
type RetrySource struct {
	DeliveryID     string
	OrganizationID string
	EventID        string
	EndpointID     string
	State          string
	Generation     int
	DispatchSeq    int
	AttemptCount   int
	LeaseToken     string
	LeaseExpiresAt pgtype.Timestamptz
	EventExpiresAt time.Time
	EventType      string
}

const readRetrySourceIdentitySQL = `
SELECT d.id::text, d.event_id::text, d.endpoint_id::text
  FROM deliveries d
 WHERE d.organization_id = $1::uuid
   AND d.id = $2::uuid`

// ReadRetrySourceIdentity locates the source without a lock. It must run after
// the organization lock and before the endpoint lock, preserving the approved
// organizations -> endpoints -> source deliveries order.
func ReadRetrySourceIdentity(ctx context.Context, q RetryQuerier, organizationID, deliveryID string) (RetrySourceIdentity, error) {
	var source RetrySourceIdentity
	err := q.QueryRow(ctx, readRetrySourceIdentitySQL, organizationID, deliveryID).Scan(
		&source.DeliveryID, &source.EventID, &source.EndpointID,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetrySourceIdentity{}, ErrRetrySourceNotFound
	}
	if err != nil {
		return RetrySourceIdentity{}, fmt.Errorf("read retry source identity: %w", err)
	}
	return source, nil
}

const lockRetrySourceSQL = `
SELECT d.id::text, d.organization_id::text, d.event_id::text, d.endpoint_id::text,
       d.state, d.generation, d.dispatch_seq, d.attempt_count,
       COALESCE(d.dispatch_lease_token::text, ''), d.dispatch_lease_expires_at,
       e.expires_at, e.type
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.organization_id = $1::uuid
   AND d.id = $2::uuid
 FOR UPDATE OF d`

// LockRetrySource locks the source Delivery for the retry transaction. Event
// rows are immutable; only their expiry is read.
func LockRetrySource(ctx context.Context, q RetryQuerier, organizationID, deliveryID string) (RetrySource, error) {
	var source RetrySource
	err := q.QueryRow(ctx, lockRetrySourceSQL, organizationID, deliveryID).Scan(
		&source.DeliveryID, &source.OrganizationID, &source.EventID, &source.EndpointID,
		&source.State, &source.Generation, &source.DispatchSeq, &source.AttemptCount,
		&source.LeaseToken, &source.LeaseExpiresAt, &source.EventExpiresAt, &source.EventType,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetrySource{}, ErrRetrySourceNotFound
	}
	if err != nil {
		return RetrySource{}, fmt.Errorf("lock retry source: %w", err)
	}
	return source, nil
}

const allocateGenerationSQL = `
SELECT COALESCE(MAX(generation), 0) + 1
  FROM deliveries
 WHERE organization_id = $1::uuid
   AND event_id = $2::uuid
   AND endpoint_id = $3::uuid`

// AllocateGeneration returns the next monotonic generation. The organization,
// endpoint and source row are already locked, so concurrent retries of the same
// lineage serialize here.
func AllocateGeneration(ctx context.Context, q RetryQuerier, organizationID, eventID, endpointID string) (int, error) {
	var generation int
	if err := q.QueryRow(ctx, allocateGenerationSQL, organizationID, eventID, endpointID).Scan(&generation); err != nil {
		return 0, fmt.Errorf("allocate delivery generation: %w", err)
	}
	return generation, nil
}

// RetrySnapshot is the fresh immutable snapshot of one new retry generation.
type RetrySnapshot struct {
	ID              string
	OrganizationID  string
	EventID         string
	EndpointID      string
	SubscriptionID  string
	URL             string
	Headers         string
	SecretEncrypted string
	Generation      int
	ParentID        string
	NextAttemptAt   time.Time
	CreatedAt       time.Time
}

func (s RetrySnapshot) validate() error {
	for _, identifier := range []struct {
		name  string
		value string
	}{
		{name: "id", value: s.ID},
		{name: "organization id", value: s.OrganizationID},
		{name: "event id", value: s.EventID},
		{name: "endpoint id", value: s.EndpointID},
		{name: "parent delivery id", value: s.ParentID},
	} {
		if !validUUID(identifier.value) {
			return errors.New("retry delivery " + identifier.name + " is not a UUID")
		}
	}
	if s.SubscriptionID != "" && !validUUID(s.SubscriptionID) {
		return errors.New("retry delivery subscription id is not a UUID")
	}
	if strings.TrimSpace(s.URL) == "" {
		return errors.New("retry delivery url is required")
	}
	if !json.Valid([]byte(s.Headers)) || !strings.HasPrefix(strings.TrimSpace(s.Headers), "{") {
		return errors.New("retry delivery headers are not a JSON object")
	}
	if s.Generation < 2 {
		return errors.New("retry delivery generation must be at least 2")
	}
	if s.NextAttemptAt.IsZero() || s.CreatedAt.IsZero() {
		return errors.New("retry delivery times are required")
	}
	return nil
}

const insertRetrySQL = `
INSERT INTO deliveries
    (id, organization_id, event_id, endpoint_id, endpoint_subscription_id,
     url, http_headers, secret_encrypted, state, dispatch_seq, next_attempt_at,
     generation, parent_delivery_id, created_at, updated_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
        $6, $7::jsonb, $8, 'pending', 1, $9::timestamptz,
        $10, $11::uuid, $12::timestamptz, $12::timestamptz)
RETURNING id::text`

// InsertRetry writes a new pending generation with dispatch sequence 1, zero
// attempts and no lease. The source row is never updated.
func InsertRetry(ctx context.Context, q RetryQuerier, snapshot RetrySnapshot) (string, error) {
	if err := snapshot.validate(); err != nil {
		return "", err
	}
	var subscription any
	if snapshot.SubscriptionID != "" {
		subscription = snapshot.SubscriptionID
	}
	var inserted string
	err := q.QueryRow(ctx, insertRetrySQL,
		snapshot.ID, snapshot.OrganizationID, snapshot.EventID, snapshot.EndpointID, subscription,
		snapshot.URL, snapshot.Headers, snapshot.SecretEncrypted, snapshot.NextAttemptAt,
		snapshot.Generation, snapshot.ParentID, snapshot.CreatedAt,
	).Scan(&inserted)
	if err != nil {
		return "", fmt.Errorf("insert retry delivery: %w", err)
	}
	if inserted != snapshot.ID {
		return "", errors.New("insert retry delivery returned an unexpected id")
	}
	return inserted, nil
}

// RetryRequest is one caller-facing idempotency claim for a manual retry.
type RetryRequest struct {
	ID               string
	OrganizationID   string
	IdempotencyKey   string
	RequestHash      []byte
	SourceDeliveryID string
	NewDeliveryID    string
	CreatedAt        time.Time
	ExpiresAt        time.Time
}

const findRetryRequestSQL = `
SELECT id::text, organization_id::text, idempotency_key, request_hash,
       source_delivery_id::text, new_delivery_id::text, created_at, expires_at
  FROM delivery_retry_requests
 WHERE organization_id = $1::uuid AND idempotency_key = $2
 FOR UPDATE`

// FindRetryRequest locks one key-scoped claim. The organization lock already
// serializes retries for the tenant; the row lock keeps the read stable.
func FindRetryRequest(ctx context.Context, q RetryQuerier, organizationID, key string) (RetryRequest, error) {
	var request RetryRequest
	err := q.QueryRow(ctx, findRetryRequestSQL, organizationID, key).Scan(
		&request.ID, &request.OrganizationID, &request.IdempotencyKey, &request.RequestHash,
		&request.SourceDeliveryID, &request.NewDeliveryID, &request.CreatedAt, &request.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetryRequest{}, ErrRetryRequestNotFound
	}
	if err != nil {
		return RetryRequest{}, fmt.Errorf("find delivery retry request: %w", err)
	}
	return request, nil
}

const insertRetryRequestSQL = `
INSERT INTO delivery_retry_requests
    (id, organization_id, idempotency_key, request_hash,
     source_delivery_id, new_delivery_id, created_at, expires_at)
VALUES ($1::uuid, $2::uuid, $3, $4, $5::uuid, $6::uuid, $7::timestamptz, $8::timestamptz)
RETURNING id::text`

// InsertRetryRequest claims a key after the new Delivery exists.
func InsertRetryRequest(ctx context.Context, q RetryQuerier, request RetryRequest) error {
	var inserted string
	err := q.QueryRow(ctx, insertRetryRequestSQL,
		request.ID, request.OrganizationID, request.IdempotencyKey, request.RequestHash,
		request.SourceDeliveryID, request.NewDeliveryID, request.CreatedAt, request.ExpiresAt,
	).Scan(&inserted)
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" &&
		pgErr.ConstraintName == "uq_retry_org_key" {
		return ErrRetryRequestConflict
	}
	if err != nil {
		return fmt.Errorf("insert delivery retry request: %w", err)
	}
	if inserted != request.ID {
		return errors.New("insert delivery retry request returned an unexpected id")
	}
	return nil
}

const deleteRetryRequestSQL = `
DELETE FROM delivery_retry_requests
 WHERE organization_id = $1::uuid AND id = $2::uuid
RETURNING id::text`

// DeleteRetryRequest removes an expired claim. The caller takes a fresh key
// instead of mutating the old row, so no partially updated claim is visible.
func DeleteRetryRequest(ctx context.Context, q RetryQuerier, organizationID, requestID string) error {
	var deleted string
	err := q.QueryRow(ctx, deleteRetryRequestSQL, organizationID, requestID).Scan(&deleted)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrRetryRequestNotFound
	}
	if err != nil {
		return fmt.Errorf("delete delivery retry request: %w", err)
	}
	return nil
}

// RetryDeliveryView is the safe response projection of a created or replayed
// retry generation. It never carries URL, headers, ciphertext or payload.
type RetryDeliveryView struct {
	ID               string
	EventID          string
	EndpointID       string
	State            string
	Generation       int
	ParentDeliveryID *string
	AttemptCount     int
	CreatedAt        time.Time
}

const readRetryDeliverySQL = `
SELECT id::text, event_id::text, endpoint_id::text, state, generation,
       parent_delivery_id::text, attempt_count, created_at
  FROM deliveries
 WHERE organization_id = $1::uuid AND id = $2::uuid`

// ReadRetryDelivery loads the response projection for a created or replayed
// retry generation.
func ReadRetryDelivery(ctx context.Context, q RetryQuerier, organizationID, deliveryID string) (RetryDeliveryView, error) {
	var view RetryDeliveryView
	err := q.QueryRow(ctx, readRetryDeliverySQL, organizationID, deliveryID).Scan(
		&view.ID, &view.EventID, &view.EndpointID, &view.State, &view.Generation,
		&view.ParentDeliveryID, &view.AttemptCount, &view.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return RetryDeliveryView{}, ErrRetrySourceNotFound
	}
	if err != nil {
		return RetryDeliveryView{}, fmt.Errorf("read retry delivery: %w", err)
	}
	return view, nil
}
