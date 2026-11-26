package delivery

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Pointer identifies one exact broker dispatch generation.
type Pointer struct {
	OrganizationID string
	DeliveryID     string
	OutboxID       string
	DispatchSeq    int
}

// Validate checks the canonical UUID representation and positive generation.
func (p Pointer) Validate() error {
	if !canonicalUUID(p.OrganizationID) {
		return errors.New("delivery pointer organization id must be a canonical UUID")
	}
	if !canonicalUUID(p.DeliveryID) {
		return errors.New("delivery pointer delivery id must be a canonical UUID")
	}
	if !canonicalUUID(p.OutboxID) {
		return errors.New("delivery pointer outbox id must be a canonical UUID")
	}
	if p.DispatchSeq < 1 {
		return errors.New("delivery pointer dispatch sequence must be positive")
	}
	return nil
}

func canonicalUUID(value string) bool {
	return validUUID(value) && value == strings.ToLower(value)
}

// PointerStatus is the database-authoritative disposition of a broker pointer.
type PointerStatus string

// Pointer acquisition dispositions are database-authoritative.
const (
	PointerAcquired PointerStatus = "acquired"
	// PointerPublishing retains a live pointer delivered before Publisher T2 commits.
	PointerPublishing PointerStatus = "publishing"
	PointerStale      PointerStatus = "stale"
	PointerObsolete   PointerStatus = "obsolete"
	PointerExpired    PointerStatus = "expired"
	PointerDuplicate  PointerStatus = "duplicate"
	PointerNotDue     PointerStatus = "not_due"
	PointerInvalid    PointerStatus = "invalid"
)

// PointerResult reports a pointer disposition without writing an HTTP attempt.
type PointerResult struct {
	Status PointerStatus
	Claim  *Claim
}

var (
	// ErrPointerStale indicates that a claim no longer matches the locked fence.
	ErrPointerStale = errors.New("delivery pointer is stale")
	// ErrEventExpired indicates that the joined event horizon has passed.
	ErrEventExpired = errors.New("delivery event has expired")
	// ErrInsufficientLease indicates that the fixed lease cannot fund required work.
	ErrInsufficientLease = errors.New("delivery lease has insufficient HTTP budget")
)

type pointerRow struct {
	claim          Claim
	state          string
	nextAttemptAt  time.Time
	leaseExpiresAt pgtype.Timestamptz
	outboxID       string
	outboxSeq      int
	outboxStatus   string
}

const acquirePointerSelectSQL = `
SELECT d.organization_id::text, d.id::text, d.event_id::text,
       d.endpoint_id::text, d.dispatch_seq, d.state, d.next_attempt_at, d.attempt_count,
       COALESCE(d.dispatch_lease_token::text, ''), d.dispatch_lease_expires_at,
       d.url, d.http_headers::text, d.secret_encrypted,
       e.expires_at, e.type, e.payload::text,
       COALESCE(o.id::text, ''), COALESCE(o.sequence, 0), COALESCE(o.status, '')
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
  LEFT JOIN outbox_messages o
    ON o.id = $3::uuid
   AND o.organization_id = d.organization_id
   AND o.delivery_id = d.id
 WHERE d.organization_id = $1::uuid
   AND d.id = $2::uuid
 FOR UPDATE OF d`

const acquirePointerUpdateSQL = `
UPDATE deliveries
   SET dispatch_lease_token = $2::uuid,
       dispatch_lease_expires_at = $3::timestamptz,
       updated_at = $4::timestamptz
 WHERE organization_id = $1::uuid
   AND id = $5::uuid
   AND state IN ('pending', 'retrying')
   AND dispatch_seq = $6
   AND next_attempt_at <= $4::timestamptz
   AND attempt_count < $7
   AND (dispatch_lease_token IS NULL OR dispatch_lease_expires_at <= $4::timestamptz)
   AND EXISTS (
       SELECT 1 FROM events e
        WHERE e.organization_id = deliveries.organization_id
          AND e.id = deliveries.event_id
          AND e.expires_at > $4::timestamptz
   )
   AND EXISTS (
       SELECT 1 FROM outbox_messages o
        WHERE o.id = $8::uuid
          AND o.organization_id = deliveries.organization_id
          AND o.delivery_id = deliveries.id
          AND o.sequence = deliveries.dispatch_seq
          AND o.status = 'published'
   )`

// AcquirePointer locks and leases only the exact published broker pointer. It
// never performs HTTP or writes an attempt, and every write retains the full
// organization, delivery, event and generation fence.
func (s *Store) AcquirePointer(ctx context.Context, pointer Pointer, opts AcquireOptions) (PointerResult, error) {
	if err := pointer.Validate(); err != nil {
		return PointerResult{Status: PointerInvalid}, err
	}
	if err := opts.validate(); err != nil {
		return PointerResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PointerResult{}, fmt.Errorf("begin delivery pointer acquisition transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	row, found, err := lockPointer(ctx, tx, pointer)
	if err != nil {
		return PointerResult{}, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return PointerResult{}, fmt.Errorf("commit invalid delivery pointer transaction: %w", err)
		}
		return PointerResult{Status: PointerInvalid}, nil
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return PointerResult{}, err
	}
	status := pointerAcquisitionStatus(row, pointer, opts.MaxAttempts, t)
	if status != PointerAcquired {
		if err := tx.Commit(ctx); err != nil {
			return PointerResult{}, fmt.Errorf("commit unchanged delivery pointer transaction: %w", err)
		}
		return PointerResult{Status: status}, nil
	}

	token, err := opts.NewToken()
	if err != nil {
		return PointerResult{}, fmt.Errorf("generate delivery pointer lease token: %w", err)
	}
	if !validUUID(token) {
		return PointerResult{}, errors.New("delivery pointer lease token generator returned a non-UUID value")
	}

	claim := row.claim
	claim.LeaseToken = token
	claim.LeaseExpiresAt = t.Add(opts.LeaseDuration)
	claim.ClaimedAt = t
	claim.NextAttemptAt = row.nextAttemptAt

	tag, err := tx.Exec(ctx, acquirePointerUpdateSQL,
		pointer.OrganizationID, token, claim.LeaseExpiresAt, t,
		pointer.DeliveryID, pointer.DispatchSeq, opts.MaxAttempts, pointer.OutboxID,
	)
	if err != nil {
		return PointerResult{}, fmt.Errorf("install delivery pointer lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return PointerResult{}, fmt.Errorf("commit stale delivery pointer transaction: %w", err)
		}
		return PointerResult{Status: PointerStale}, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return PointerResult{}, fmt.Errorf("commit delivery pointer acquisition transaction: %w", err)
	}
	return PointerResult{Status: PointerAcquired, Claim: &claim}, nil
}

func lockPointer(ctx context.Context, tx pgx.Tx, pointer Pointer) (pointerRow, bool, error) {
	var row pointerRow
	var nextAttemptAt pgtype.Timestamptz
	err := tx.QueryRow(ctx, acquirePointerSelectSQL,
		pointer.OrganizationID, pointer.DeliveryID, pointer.OutboxID,
	).Scan(
		&row.claim.OrganizationID,
		&row.claim.DeliveryID,
		&row.claim.EventID,
		&row.claim.EndpointID,
		&row.claim.DispatchSeq,
		&row.state,
		&nextAttemptAt,
		&row.claim.AttemptCount,
		&row.claim.LeaseToken,
		&row.leaseExpiresAt,
		&row.claim.RequestURL,
		&row.claim.RequestHeaders,
		&row.claim.SecretCiphertext,
		&row.claim.EventExpiresAt,
		&row.claim.EventType,
		&row.claim.EventPayload,
		&row.outboxID,
		&row.outboxSeq,
		&row.outboxStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return pointerRow{}, false, nil
	}
	if err != nil {
		return pointerRow{}, false, fmt.Errorf("lock delivery pointer row: %w", err)
	}
	row.nextAttemptAt = nextAttemptAt.Time
	return row, true, nil
}

func pointerAcquisitionStatus(row pointerRow, pointer Pointer, maxAttempts int, now time.Time) PointerStatus {
	if row.outboxID == "" {
		return PointerInvalid
	}
	if row.state != "pending" && row.state != "retrying" {
		return PointerObsolete
	}
	if !row.claim.EventExpiresAt.After(now) {
		return PointerExpired
	}
	if row.claim.DispatchSeq != pointer.DispatchSeq || row.outboxSeq != pointer.DispatchSeq ||
		row.outboxSeq != row.claim.DispatchSeq {
		return PointerStale
	}
	if row.outboxStatus == "publishing" {
		return PointerPublishing
	}
	if row.outboxStatus != "published" {
		return PointerStale
	}
	if row.claim.AttemptCount >= maxAttempts {
		return PointerObsolete
	}
	if row.claim.LeaseToken != "" && row.leaseExpiresAt.Valid && row.leaseExpiresAt.Time.After(now) {
		return PointerDuplicate
	}
	if row.nextAttemptAt.After(now) {
		return PointerNotDue
	}
	return PointerAcquired
}

const expirePointerSQL = `
UPDATE deliveries
   SET state = 'expired',
       completed_at = $4::timestamptz,
       next_attempt_at = NULL,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL,
       updated_at = $4::timestamptz
 WHERE organization_id = $1::uuid
   AND id = $2::uuid
   AND dispatch_seq = $3
   AND state IN ('pending', 'retrying')
   AND EXISTS (
       SELECT 1 FROM events e
        WHERE e.organization_id = deliveries.organization_id
          AND e.id = deliveries.event_id
          AND e.expires_at <= $4::timestamptz
   )`

// ExpirePointer terminalizes an exact live generation only when its event is
// expired at fresh database time. It writes no attempt and creates no outbox row.
func (s *Store) ExpirePointer(ctx context.Context, pointer Pointer) (bool, error) {
	if err := pointer.Validate(); err != nil {
		return false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin delivery pointer expiry transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, found, err := lockDelivery(ctx, tx, pointer.OrganizationID, pointer.DeliveryID)
	if err != nil {
		return false, err
	}
	if !found || locked.dispatchSeq != pointer.DispatchSeq ||
		(locked.state != "pending" && locked.state != "retrying") {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit unchanged delivery pointer expiry transaction: %w", err)
		}
		return false, nil
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return false, err
	}
	if locked.eventExpiresAt.After(t) {
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit unexpired delivery pointer transaction: %w", err)
		}
		return false, nil
	}

	tag, err := tx.Exec(ctx, expirePointerSQL,
		pointer.OrganizationID, pointer.DeliveryID, pointer.DispatchSeq, t,
	)
	if err != nil {
		return false, fmt.Errorf("expire delivery pointer: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit delivery pointer expiry transaction: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

const releasePointerSQL = `
UPDATE deliveries
   SET dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL,
       updated_at = clock_timestamp()
 WHERE organization_id = $1::uuid
   AND id = $2::uuid
   AND dispatch_seq = $3
   AND dispatch_lease_token = $4::uuid
   AND state IN ('pending', 'retrying')`

// ReleasePointer clears only the currently fenced lease tuple. Scheduling,
// generation, attempt accounting and outbox state remain untouched.
func (s *Store) ReleasePointer(ctx context.Context, claim Claim) (bool, error) {
	if err := validatePointerClaimFence(claim); err != nil {
		return false, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return false, fmt.Errorf("begin delivery pointer release transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, releasePointerSQL,
		claim.OrganizationID, claim.DeliveryID, claim.DispatchSeq, claim.LeaseToken,
	)
	if err != nil {
		return false, fmt.Errorf("release delivery pointer lease: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit delivery pointer release transaction: %w", err)
	}
	return tag.RowsAffected() == 1, nil
}

func validatePointerClaimFence(claim Claim) error {
	if !validUUID(claim.OrganizationID) {
		return errors.New("delivery pointer claim organization id is not a UUID")
	}
	if !validUUID(claim.DeliveryID) {
		return errors.New("delivery pointer claim delivery id is not a UUID")
	}
	if claim.DispatchSeq < 1 {
		return errors.New("delivery pointer claim generation must be positive")
	}
	if !validUUID(claim.LeaseToken) {
		return errors.New("delivery pointer claim lease token is not a UUID")
	}
	return nil
}

// HTTPPreparation is an immutable snapshot from freshly locked delivery state.
type HTTPPreparation struct {
	OrganizationID   string
	DeliveryID       string
	EventID          string
	EndpointID       string
	DispatchSeq      int
	LeaseExpiresAt   time.Time
	EventExpiresAt   time.Time
	DatabaseTime     time.Time
	Remaining        time.Duration
	RequestURL       string
	RequestHeaders   string
	SecretCiphertext string
	// SecretPlaintext is set by the Worker only after successful resolution. It
	// is never persisted, logged, or included in a diagnostic.
	SecretPlaintext []byte
	EventType       string
	EventPayload    string
}

type httpDelivery struct {
	eventID        string
	endpointID     string
	dispatchSeq    int
	state          string
	leaseToken     string
	leaseExpiresAt pgtype.Timestamptz
	requestURL     string
	requestHeaders string
	secret         string
	eventExpiresAt time.Time
	eventType      string
	eventPayload   string
}

const prepareHTTPSelectSQL = `
SELECT d.event_id::text, d.endpoint_id::text, d.dispatch_seq, d.state,
       COALESCE(d.dispatch_lease_token::text, ''), d.dispatch_lease_expires_at,
       d.url, d.http_headers::text, d.secret_encrypted,
       e.expires_at, e.type, e.payload::text
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.organization_id = $1::uuid
   AND d.id = $2::uuid
 FOR UPDATE OF d, e`

// PrepareHTTP revalidates the claim against fresh locked state and enforces the
// pre-HTTP fixed-lease budget. It performs no mutations and starts no HTTP.
func (s *Store) PrepareHTTP(ctx context.Context, claim Claim, required time.Duration) (HTTPPreparation, error) {
	if required <= 0 {
		return HTTPPreparation{}, errors.New("delivery HTTP budget requirement must be positive")
	}
	if err := validateClaim(claim); err != nil {
		return HTTPPreparation{}, err
	}
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return HTTPPreparation{}, fmt.Errorf("begin delivery HTTP preparation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, found, err := lockHTTPDelivery(ctx, tx, claim)
	if err != nil {
		return HTTPPreparation{}, err
	}
	if !found {
		if err := tx.Commit(ctx); err != nil {
			return HTTPPreparation{}, fmt.Errorf("commit stale delivery HTTP preparation transaction: %w", err)
		}
		return HTTPPreparation{}, ErrPointerStale
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return HTTPPreparation{}, err
	}
	if err := validateHTTPPreparation(locked, claim, t, required); err != nil {
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return HTTPPreparation{}, fmt.Errorf("commit refused delivery HTTP preparation transaction: %w", commitErr)
		}
		return HTTPPreparation{}, err
	}

	remaining := locked.leaseExpiresAt.Time.Sub(t)

	preparation := HTTPPreparation{
		OrganizationID:   claim.OrganizationID,
		DeliveryID:       claim.DeliveryID,
		EventID:          locked.eventID,
		EndpointID:       locked.endpointID,
		DispatchSeq:      locked.dispatchSeq,
		LeaseExpiresAt:   locked.leaseExpiresAt.Time,
		EventExpiresAt:   locked.eventExpiresAt,
		DatabaseTime:     t,
		Remaining:        remaining,
		RequestURL:       locked.requestURL,
		RequestHeaders:   locked.requestHeaders,
		SecretCiphertext: locked.secret,
		EventType:        locked.eventType,
		EventPayload:     locked.eventPayload,
	}
	if err := tx.Commit(ctx); err != nil {
		return HTTPPreparation{}, fmt.Errorf("commit delivery HTTP preparation transaction: %w", err)
	}
	return preparation, nil
}

func validateHTTPPreparation(locked httpDelivery, claim Claim, now time.Time, required time.Duration) error {
	if locked.eventID != claim.EventID || locked.endpointID != claim.EndpointID ||
		locked.dispatchSeq != claim.DispatchSeq ||
		locked.leaseToken != claim.LeaseToken ||
		(locked.state != "pending" && locked.state != "retrying") {
		return ErrPointerStale
	}
	if !locked.eventExpiresAt.After(now) {
		return ErrEventExpired
	}
	if locked.leaseExpiresAt.Time.Sub(now) < required {
		return ErrInsufficientLease
	}
	return nil
}

func lockHTTPDelivery(ctx context.Context, tx pgx.Tx, claim Claim) (httpDelivery, bool, error) {
	var locked httpDelivery
	err := tx.QueryRow(ctx, prepareHTTPSelectSQL, claim.OrganizationID, claim.DeliveryID).Scan(
		&locked.eventID,
		&locked.endpointID,
		&locked.dispatchSeq,
		&locked.state,
		&locked.leaseToken,
		&locked.leaseExpiresAt,
		&locked.requestURL,
		&locked.requestHeaders,
		&locked.secret,
		&locked.eventExpiresAt,
		&locked.eventType,
		&locked.eventPayload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpDelivery{}, false, nil
	}
	if err != nil {
		return httpDelivery{}, false, fmt.Errorf("lock delivery HTTP preparation row: %w", err)
	}
	return locked, true, nil
}

// RedactDiagnostic applies the existing bounded credential-safe diagnostic
// normalization for callers that persist worker diagnostics.
func RedactDiagnostic(value string) string {
	return boundedDiagnostic(value)
}
