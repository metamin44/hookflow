// Package outbox implements the approved database-side publisher accounting.
//
// It deliberately contains no RabbitMQ client and performs no publication. A
// caller claims rows in one committed transaction, publishes them outside the
// database, and records the result through the fenced T2 methods.
package outbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Beginner is satisfied by *pgxpool.Pool and by pgx.Tx. The transaction form is
// useful for tests that must roll their fixtures back.
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// DB is the query surface required outside an explicit transaction.
type DB interface {
	Begin(context.Context) (pgx.Tx, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// TokenGenerator returns a fresh, unique UUID-compatible fencing token. It is
// injected because U1's entity-ID policy does not select a token algorithm and
// the project has no UUID dependency.
type TokenGenerator func() (string, error)

// Backoff returns the configured publish backoff for a consumed claim count.
// It is injected because U22 does not approve a production backoff policy.
type Backoff func(claimCount int) (time.Duration, error)

// Store is the database-side outbox publisher.
type Store struct {
	db     DB
	logger *slog.Logger
}

// NewStore creates a publisher store. A nil logger uses slog.Default.
func NewStore(db DB, logger *slog.Logger) *Store {
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, logger: logger}
}

// ClaimOptions are the explicit, unresolved publisher parameters supplied by
// configuration. No production defaults are embedded here.
type ClaimOptions struct {
	BatchSize     int
	MaxClaims     int
	LeaseDuration time.Duration
	ProcessID     string
	NewToken      TokenGenerator
}

// Claim is one row claimed by T1.
type Claim struct {
	MessageID      string
	OrganizationID string
	DeliveryID     string
	Sequence       int
	Kind           string
	Topic          string
	Owner          string
	MaxClaims      int
	ClaimCount     int
	LeaseToken     string
	ClaimedAt      time.Time
	LeaseExpiresAt time.Time
}

// Failure is the T2 publication-failure input.
type Failure struct {
	Claim   Claim
	Backoff time.Duration
	// Error must already be bounded and credential-scrubbed by the caller.
	Error string
}

// FailureResult reports the outcome of a fenced T2 failure update.
type FailureResult struct {
	Applied  bool
	Status   string
	Attempts int
}

// ReapOptions are the T3 expired-lease parameters.
type ReapOptions struct {
	BatchSize     int
	MaxClaims     int
	LeaseDuration time.Duration
	Backoff       Backoff
}

// ReapedLease describes one lease returned to a retry state by T3.
type ReapedLease struct {
	Status   string
	Attempts int
}

// ReapResult is the result of one T3 batch.
type ReapResult struct {
	Reaped []ReapedLease
}

const reapDiagnostic = "publisher lease expired"

const claimSelectSQL = `
SELECT id::text, organization_id::text, delivery_id::text, sequence, kind, topic, attempts, available_at
  FROM outbox_messages
 WHERE status = 'pending'
   AND available_at <= clock_timestamp()
   AND attempts < $2
 ORDER BY available_at, id
 FOR UPDATE SKIP LOCKED
 LIMIT $1`

const claimUpdateSQL = `
UPDATE outbox_messages
   SET status = 'publishing',
       attempts = attempts + 1,
       locked_by = $3,
       locked_at = $4,
       lease_token = $5::uuid,
       updated_at = $4
 WHERE id = $1::uuid
   AND status = 'pending'
   AND attempts = $2
   AND available_at <= $4
RETURNING attempts`

// Claim runs T1: it locks pending eligible rows, samples fresh database time
// after the lock, increments the per-cycle counter and commits the complete
// lease tuple before returning. The caller must publish only after Commit.
func (s *Store) Claim(ctx context.Context, opts ClaimOptions) ([]Claim, error) {
	if err := validateClaimOptions(opts); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin publisher claim transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, claimSelectSQL, opts.BatchSize, opts.MaxClaims)
	if err != nil {
		return nil, fmt.Errorf("select publisher claim candidates: %w", err)
	}

	type candidate struct {
		id             string
		organizationID string
		deliveryID     string
		sequence       int
		kind           string
		topic          string
		attempts       int
		availableAt    time.Time
	}
	candidates := make([]candidate, 0, opts.BatchSize)
	for rows.Next() {
		var c candidate
		if err := rows.Scan(&c.id, &c.organizationID, &c.deliveryID, &c.sequence, &c.kind, &c.topic, &c.attempts, &c.availableAt); err != nil {
			rows.Close()
			return nil, fmt.Errorf("scan publisher claim candidate: %w", err)
		}
		candidates = append(candidates, c)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, fmt.Errorf("read publisher claim candidates: %w", err)
	}
	rows.Close()

	if len(candidates) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit empty publisher claim: %w", err)
		}
		return nil, nil
	}

	var claimedAt time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&claimedAt); err != nil {
		return nil, fmt.Errorf("sample publisher claim time: %w", err)
	}

	claims := make([]Claim, 0, len(candidates))
	tokens := make(map[string]struct{}, len(candidates))
	for _, c := range candidates {
		if c.availableAt.After(claimedAt) || c.attempts >= opts.MaxClaims {
			continue
		}
		token, err := opts.NewToken()
		if err != nil {
			return nil, fmt.Errorf("generate publisher lease token: %w", err)
		}
		if !validUUID(token) {
			return nil, errors.New("generated publisher lease token is not a UUID")
		}
		if _, exists := tokens[token]; exists {
			return nil, errors.New("generated duplicate publisher lease token")
		}
		tokens[token] = struct{}{}

		var newAttempts int
		if err := tx.QueryRow(ctx, claimUpdateSQL,
			c.id, c.attempts, opts.ProcessID, claimedAt, token,
		).Scan(&newAttempts); err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				return nil, errors.New("publisher claim candidate changed after locking")
			}
			return nil, fmt.Errorf("write publisher claim: %w", err)
		}
		if newAttempts != c.attempts+1 {
			return nil, errors.New("publisher claim counter did not advance by one")
		}

		claims = append(claims, Claim{
			MessageID:      c.id,
			OrganizationID: c.organizationID,
			DeliveryID:     c.deliveryID,
			Sequence:       c.sequence,
			Kind:           c.kind,
			Topic:          c.topic,
			Owner:          opts.ProcessID,
			MaxClaims:      opts.MaxClaims,
			ClaimCount:     newAttempts,
			LeaseToken:     token,
			ClaimedAt:      claimedAt,
			LeaseExpiresAt: claimedAt.Add(opts.LeaseDuration),
		})
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit publisher claim: %w", err)
	}

	oldest := time.Duration(0)
	for _, c := range candidates {
		if age := claimedAt.Sub(c.availableAt); age > oldest {
			oldest = age
		}
	}
	s.logger.Info("publisher claim committed", "claimed", len(claims), "oldest_available_age", oldest.String())
	return claims, nil
}

const publishSQL = `
WITH sampled AS (SELECT clock_timestamp() AS t)
UPDATE outbox_messages AS o
   SET status = 'published',
       published_at = sampled.t,
       updated_at = sampled.t,
       locked_by = NULL,
       locked_at = NULL,
       lease_token = NULL,
       last_error = NULL
  FROM sampled
 WHERE o.id = $1::uuid
   AND o.status = 'publishing'
   AND o.locked_by = $2
   AND o.lease_token = $3::uuid
RETURNING 1`

// MarkPublished records T2 success. A false result is a fence: the stale
// operation wrote nothing and must not re-drive the row.
func (s *Store) MarkPublished(ctx context.Context, claim Claim) (bool, error) {
	if err := validateClaim(claim); err != nil {
		return false, err
	}

	var marker int
	err := s.db.QueryRow(ctx, publishSQL, claim.MessageID, claim.Owner, claim.LeaseToken).Scan(&marker)
	if errors.Is(err, pgx.ErrNoRows) {
		s.logger.Info("publisher completion fenced", "outcome", "published")
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("record publisher success: %w", err)
	}

	s.logger.Info("publisher completion committed", "outcome", "published", "claim_count", claim.ClaimCount)
	return true, nil
}

const failureSQL = `
WITH sampled AS (SELECT clock_timestamp() AS t)
UPDATE outbox_messages AS o
   SET status = CASE WHEN o.attempts >= $5 THEN 'failed' ELSE 'pending' END,
       available_at = GREATEST(o.available_at, sampled.t + ($4::bigint * interval '1 microsecond')),
       updated_at = sampled.t,
       locked_by = NULL,
       locked_at = NULL,
       lease_token = NULL,
       last_error = $6
  FROM sampled
 WHERE o.id = $1::uuid
   AND o.status = 'publishing'
   AND o.locked_by = $2
   AND o.lease_token = $3::uuid
RETURNING status, attempts`

// MarkFailed records T2 failure. Below MaxClaims the row returns to pending
// with monotonic backoff; at MaxClaims it becomes failed. last_error is
// retained until a later successful publication.
func (s *Store) MarkFailed(ctx context.Context, failure Failure) (FailureResult, error) {
	if err := validateClaim(failure.Claim); err != nil {
		return FailureResult{}, err
	}
	if failure.Backoff <= 0 {
		return FailureResult{}, errors.New("publisher backoff must be positive")
	}
	diagnostic, err := boundedDiagnostic(failure.Error)
	if err != nil {
		return FailureResult{}, err
	}

	var result FailureResult
	result.Attempts = failure.Claim.ClaimCount
	err = s.db.QueryRow(ctx, failureSQL,
		failure.Claim.MessageID,
		failure.Claim.Owner,
		failure.Claim.LeaseToken,
		failure.Backoff.Microseconds(),
		failure.Claim.MaxClaims,
		diagnostic,
	).Scan(&result.Status, &result.Attempts)
	if errors.Is(err, pgx.ErrNoRows) {
		s.logger.Info("publisher completion fenced", "outcome", "failure")
		return FailureResult{}, nil
	}
	if err != nil {
		return FailureResult{}, fmt.Errorf("record publisher failure: %w", err)
	}
	result.Applied = true

	s.logger.Info("publisher completion committed", "outcome", result.Status, "claim_count", result.Attempts)
	return result, nil
}

const reapSelectSQL = `
SELECT id::text, attempts, available_at, locked_at, lease_token::text
  FROM outbox_messages
 WHERE status = 'publishing'
   AND locked_at + ($1::bigint * interval '1 microsecond') <= clock_timestamp()
 ORDER BY locked_at, id
 FOR UPDATE SKIP LOCKED
 LIMIT $2`

const reapUpdateSQL = `
UPDATE outbox_messages
   SET status = $3::text,
       available_at = GREATEST(available_at, $4::timestamptz),
       updated_at = $4::timestamptz,
       locked_by = NULL,
       locked_at = NULL,
       lease_token = NULL,
       last_error = $5
 WHERE id = $1::uuid
   AND status = 'publishing'
   AND lease_token = $2::uuid
RETURNING 1`

// ReapExpired runs T3: it locks expired publisher leases, revalidates expiry
// against fresh database time, and atomically returns them to pending or
// failed without incrementing attempts again.
func (s *Store) ReapExpired(ctx context.Context, opts ReapOptions) (ReapResult, error) {
	if opts.BatchSize <= 0 {
		return ReapResult{}, errors.New("publisher reap batch size must be positive")
	}
	if opts.MaxClaims <= 0 {
		return ReapResult{}, errors.New("publisher maximum claims must be positive")
	}
	if opts.LeaseDuration <= 0 {
		return ReapResult{}, errors.New("publisher lease duration must be positive")
	}
	if opts.Backoff == nil {
		return ReapResult{}, errors.New("publisher backoff function is required")
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return ReapResult{}, fmt.Errorf("begin publisher reap transaction: %w", err)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	rows, err := tx.Query(ctx, reapSelectSQL, opts.LeaseDuration.Microseconds(), opts.BatchSize)
	if err != nil {
		return ReapResult{}, fmt.Errorf("select expired publisher leases: %w", err)
	}

	type expired struct {
		id          string
		attempts    int
		availableAt time.Time
		lockedAt    time.Time
		token       string
	}
	candidates := make([]expired, 0, opts.BatchSize)
	for rows.Next() {
		var e expired
		if err := rows.Scan(&e.id, &e.attempts, &e.availableAt, &e.lockedAt, &e.token); err != nil {
			rows.Close()
			return ReapResult{}, fmt.Errorf("scan expired publisher lease: %w", err)
		}
		candidates = append(candidates, e)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return ReapResult{}, fmt.Errorf("read expired publisher leases: %w", err)
	}
	rows.Close()

	if len(candidates) == 0 {
		if err := tx.Commit(ctx); err != nil {
			return ReapResult{}, fmt.Errorf("commit empty publisher reap: %w", err)
		}
		return ReapResult{}, nil
	}

	var sampledAt time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&sampledAt); err != nil {
		return ReapResult{}, fmt.Errorf("sample publisher reap time: %w", err)
	}

	result := ReapResult{Reaped: make([]ReapedLease, 0, len(candidates))}
	for _, c := range candidates {
		if c.lockedAt.Add(opts.LeaseDuration).After(sampledAt) {
			continue
		}
		backoff, err := opts.Backoff(c.attempts)
		if err != nil {
			return ReapResult{}, fmt.Errorf("derive publisher reap backoff: %w", err)
		}
		if backoff <= 0 {
			return ReapResult{}, errors.New("publisher reap backoff must be positive")
		}

		status := "pending"
		if c.attempts >= opts.MaxClaims {
			status = "failed"
		}
		availableAt := sampledAt.Add(backoff)
		if c.availableAt.After(availableAt) {
			availableAt = c.availableAt
		}

		var marker int
		err = tx.QueryRow(ctx, reapUpdateSQL,
			c.id, c.token, status, availableAt, reapDiagnostic,
		).Scan(&marker)
		if errors.Is(err, pgx.ErrNoRows) {
			continue
		}
		if err != nil {
			return ReapResult{}, fmt.Errorf("reap publisher lease: %w", err)
		}
		result.Reaped = append(result.Reaped, ReapedLease{Status: status, Attempts: c.attempts})
	}

	if err := tx.Commit(ctx); err != nil {
		return ReapResult{}, fmt.Errorf("commit publisher reap: %w", err)
	}

	s.logger.Info("publisher expired leases reaped", "reaped", len(result.Reaped))
	return result, nil
}

// RandomToken returns a cryptographically random UUIDv4-shaped fencing token.
// It is a token, not a U1 entity identifier.
func RandomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func validateClaimOptions(opts ClaimOptions) error {
	if opts.BatchSize <= 0 {
		return errors.New("publisher batch size must be positive")
	}
	if opts.MaxClaims <= 0 {
		return errors.New("publisher maximum claims must be positive")
	}
	if opts.LeaseDuration <= 0 {
		return errors.New("publisher lease duration must be positive")
	}
	if strings.TrimSpace(opts.ProcessID) == "" {
		return errors.New("publisher process id is required")
	}
	if opts.NewToken == nil {
		return errors.New("publisher token generator is required")
	}
	return nil
}

func validateClaim(claim Claim) error {
	if !validUUID(claim.MessageID) {
		return errors.New("publisher message id is not a UUID")
	}
	if !validUUID(claim.LeaseToken) {
		return errors.New("publisher lease token is not a UUID")
	}
	if strings.TrimSpace(claim.Owner) == "" {
		return errors.New("publisher claim owner is required")
	}
	if claim.MaxClaims <= 0 {
		return errors.New("publisher maximum claims must be positive")
	}
	if claim.ClaimCount <= 0 || claim.ClaimCount > claim.MaxClaims {
		return errors.New("publisher claim count is outside the configured budget")
	}
	return nil
}

func validUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, r := range value {
		switch i {
		case 8, 13, 18, 23:
			if r != '-' {
				return false
			}
		default:
			if (r < '0' || r > '9') && (r < 'a' || r > 'f') && (r < 'A' || r > 'F') {
				return false
			}
		}
	}
	return true
}

func boundedDiagnostic(value string) (string, error) {
	cleaned := strings.Map(func(r rune) rune {
		switch r {
		case 0, '\r', '\n', '\t':
			return ' '
		default:
			return r
		}
	}, strings.TrimSpace(value))
	cleaned = strings.Join(strings.Fields(cleaned), " ")
	if cleaned == "" {
		return "", errors.New("publisher failure diagnostic must not be empty")
	}

	const limit = 500
	runes := []rune(cleaned)
	if len(runes) > limit {
		cleaned = string(runes[:limit])
	}
	return cleaned, nil
}
