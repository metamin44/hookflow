// Package delivery implements the approved C-2 database-side worker core.
//
// It acquires a fixed worker lease, fences completion against the current
// generation and token, persists write-on-completion HTTP attempts, and
// persists the pre-HTTP decryption-failure disposition. It performs no HTTP
// request and decrypts no secret: those boundaries are injected by callers.
package delivery

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/platform/diagnostics"
)

// Beginner is satisfied by *pgxpool.Pool and by pgx.Tx.
type Beginner interface {
	Begin(context.Context) (pgx.Tx, error)
}

// IDGenerator supplies application-generated entity IDs. U1 requires UUIDv7
// for entities; this package does not choose or implement that generator.
type IDGenerator func() (string, error)

// TokenGenerator supplies a fresh worker fencing token (a UUIDv4 is proposed,
// not an entity ID). The package does not select a UUID library.
type TokenGenerator func() (string, error)

// Message is the explicit retry outbox row a caller asks the Store to insert.
type Message struct {
	ID       string
	Sequence int
	Kind     string
	Topic    string
	Payload  string
	Headers  string
}

// RetryMessageInput is the current-generation input for a retry dispatch.
type RetryMessageInput struct {
	OrganizationID string
	DeliveryID     string
	EventID        string
	Sequence       int
	NextAttemptAt  time.Time
}

// MessageBuilder constructs the approved small retry dispatch intent. U21
// topology is open, so the caller owns topic and payload.
type MessageBuilder func(RetryMessageInput) (Message, error)

// Options are explicit worker-core dependencies.
type Options struct {
	NewID             IDGenerator
	BuildRetryMessage MessageBuilder
	Logger            *slog.Logger
}

// Store is the C-2 database-side worker core.
type Store struct {
	db     Beginner
	opts   Options
	logger *slog.Logger
}

// NewStore validates explicit dependencies.
func NewStore(db Beginner, opts Options) (*Store, error) {
	if db == nil {
		return nil, errors.New("delivery database is required")
	}
	if opts.NewID == nil {
		return nil, errors.New("delivery entity ID generator is required")
	}
	if opts.BuildRetryMessage == nil {
		return nil, errors.New("delivery retry message builder is required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{db: db, opts: opts, logger: logger}, nil
}

// AcquireOptions are the explicit unresolved acquisition parameters.
type AcquireOptions struct {
	LeaseDuration time.Duration
	MaxAttempts   int
	NewToken      TokenGenerator
}

// Claim is an acquired fixed worker lease. HTTP may start only after Acquire
// returns, because the acquisition transaction has committed.
type Claim struct {
	OrganizationID   string
	DeliveryID       string
	EventID          string
	EndpointID       string
	DispatchSeq      int
	LeaseToken       string
	LeaseExpiresAt   time.Time
	AttemptCount     int
	NextAttemptAt    time.Time
	RequestURL       string
	RequestHeaders   string
	SecretCiphertext string
	EventType        string
	EventPayload     string
	EventExpiresAt   time.Time
	ClaimedAt        time.Time
}

func (o AcquireOptions) validate() error {
	if o.LeaseDuration <= 0 {
		return errors.New("delivery lease duration must be positive")
	}
	if o.MaxAttempts <= 0 {
		return errors.New("delivery maximum attempts must be positive")
	}
	if o.NewToken == nil {
		return errors.New("delivery lease token generator is required")
	}
	return nil
}

const acquireSelectSQL = `
SELECT d.organization_id::text, d.id::text, d.event_id::text,
       d.endpoint_id::text, d.dispatch_seq, d.state, d.next_attempt_at, d.attempt_count,
       COALESCE(d.dispatch_lease_token::text, ''), d.dispatch_lease_expires_at,
       d.url, d.http_headers::text, d.secret_encrypted,
       e.expires_at, e.type, e.payload::text
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.state IN ('pending', 'retrying')
   AND d.next_attempt_at <= clock_timestamp()
   AND e.expires_at > clock_timestamp()
   AND d.attempt_count < $1
   AND (d.dispatch_lease_token IS NULL OR d.dispatch_lease_expires_at <= clock_timestamp())
 ORDER BY d.next_attempt_at, d.id
 LIMIT 1
 FOR UPDATE OF d SKIP LOCKED`

const acquireUpdateSQL = `
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
   AND (dispatch_lease_token IS NULL OR dispatch_lease_expires_at <= $4::timestamptz)`

// Acquire atomically selects one eligible delivery and installs a fixed lease.
// It performs no attempt write and does not change the delivery state.
func (s *Store) Acquire(ctx context.Context, opts AcquireOptions) (*Claim, error) {
	if err := opts.validate(); err != nil {
		return nil, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin delivery acquisition transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var (
		claim          Claim
		state          string
		nextAttemptAt  time.Time
		leaseExpiresAt pgtype.Timestamptz
		eventExpiresAt time.Time
	)
	err = tx.QueryRow(ctx, acquireSelectSQL, opts.MaxAttempts).Scan(
		&claim.OrganizationID,
		&claim.DeliveryID,
		&claim.EventID,
		&claim.EndpointID,
		&claim.DispatchSeq,
		&state,
		&nextAttemptAt,
		&claim.AttemptCount,
		&claim.LeaseToken,
		&leaseExpiresAt,
		&claim.RequestURL,
		&claim.RequestHeaders,
		&claim.SecretCiphertext,
		&eventExpiresAt,
		&claim.EventType,
		&claim.EventPayload,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit empty delivery acquisition transaction: %w", err)
		}
		s.logger.Debug("delivery acquisition found no eligible candidate")
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("select delivery acquisition candidate: %w", err)
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return nil, err
	}
	if !acquireEligible(state, nextAttemptAt, eventExpiresAt, claim.AttemptCount, opts.MaxAttempts,
		claim.LeaseToken, leaseExpiresAt, t) {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit ineligible delivery acquisition transaction: %w", err)
		}
		return nil, nil
	}

	token, err := opts.NewToken()
	if err != nil {
		return nil, fmt.Errorf("generate delivery lease token: %w", err)
	}
	if !validUUID(token) {
		return nil, errors.New("delivery lease token generator returned a non-UUID value")
	}

	claim.LeaseToken = token
	claim.LeaseExpiresAt = t.Add(opts.LeaseDuration)
	claim.ClaimedAt = t
	claim.NextAttemptAt = nextAttemptAt
	claim.EventExpiresAt = eventExpiresAt

	tag, err := tx.Exec(ctx, acquireUpdateSQL,
		claim.OrganizationID, claim.LeaseToken, claim.LeaseExpiresAt, t,
		claim.DeliveryID, claim.DispatchSeq, opts.MaxAttempts,
	)
	if err != nil {
		return nil, fmt.Errorf("install delivery lease: %w", err)
	}
	if tag.RowsAffected() != 1 {
		if err := tx.Commit(ctx); err != nil {
			return nil, fmt.Errorf("commit changed delivery acquisition transaction: %w", err)
		}
		return nil, nil
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, fmt.Errorf("commit delivery acquisition transaction: %w", err)
	}

	s.logger.Info("delivery acquired",
		"lease_duration", opts.LeaseDuration.String(),
		"attempt_count", claim.AttemptCount,
		"event_expires_in", time.Until(claim.EventExpiresAt).String(),
	)
	return &claim, nil
}

func acquireEligible(state string, next, eventExpires time.Time, attempts, maxAttempts int,
	leaseToken string, leaseExpires pgtype.Timestamptz, t time.Time) bool {
	if state != "pending" && state != "retrying" {
		return false
	}
	if next.After(t) || attempts >= maxAttempts {
		return false
	}
	if !eventExpires.After(t) {
		return false
	}
	return leaseToken == "" || (leaseExpires.Valid && !leaseExpires.Time.After(t))
}

// OutcomeKind is the stored HTTP outcome classification.
type OutcomeKind string

// Outcome kinds classify one completed HTTP execution.
const (
	OutcomeSuccess          OutcomeKind = "success"
	OutcomeRetryableFailure OutcomeKind = "retryable_failure"
	OutcomeTerminalFailure  OutcomeKind = "terminal_failure"
)

// Outcome is an observed, completed HTTP execution supplied by the caller.
type Outcome struct {
	Kind                OutcomeKind
	RequestMethod       string
	RequestHeaders      string
	ResponseStatus      *int
	ResponseHeaders     string
	ResponseBodySnippet string
	ErrorClass          string
	ErrorMessage        string
	Duration            time.Duration
	StartedAt           time.Time
	FinishedAt          time.Time
}

// RetryDecision is the explicit retry-policy input. This package neither
// computes nor defaults a backoff or attempt budget.
type RetryDecision struct {
	Retry   bool
	Backoff time.Duration
}

// CompletionResult reports the durable disposition of a completion.
type CompletionResult struct {
	Applied       bool
	Fenced        bool
	Expired       bool
	Status        string
	AttemptNumber int
	RetrySequence int
}

// Complete persists an observed HTTP execution for a currently fenced claim.
// A stale organization, generation, token or terminal delivery writes nothing.
func (s *Store) Complete(ctx context.Context, claim Claim, outcome Outcome, decision RetryDecision) (CompletionResult, error) {
	if err := validateClaim(claim); err != nil {
		return CompletionResult{}, err
	}
	if err := validateOutcome(outcome); err != nil {
		return CompletionResult{}, err
	}
	if err := validateDecision(outcome.Kind, decision); err != nil {
		return CompletionResult{}, err
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("begin delivery completion transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, found, err := lockDelivery(ctx, tx, claim.OrganizationID, claim.DeliveryID)
	if err != nil {
		return CompletionResult{}, err
	}
	if !found || completionFenced(locked, claim) {
		if err := tx.Commit(ctx); err != nil {
			return CompletionResult{}, fmt.Errorf("commit fenced delivery completion transaction: %w", err)
		}
		s.logger.Warn("delivery completion fenced", "reason", fenceReason(locked, found, claim))
		return CompletionResult{Fenced: true}, nil
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return CompletionResult{}, err
	}

	attemptNumber := locked.attemptCount + 1
	expired := !locked.eventExpiresAt.After(t)
	state := "failed"
	nextAttemptAt := time.Time{}
	retrySequence := locked.dispatchSeq
	shouldRetry := false
	if expired {
		state = "expired"
	} else {
		switch outcome.Kind {
		case OutcomeSuccess:
			state = "succeeded"
		case OutcomeRetryableFailure:
			if decision.Retry {
				state = "retrying"
				nextAttemptAt = t.Add(decision.Backoff)
				retrySequence = locked.dispatchSeq + 1
				shouldRetry = true
			}
		case OutcomeTerminalFailure:
			state = "failed"
		}
	}

	attemptID, err := s.opts.NewID()
	if err != nil {
		return CompletionResult{}, fmt.Errorf("generate delivery attempt id: %w", err)
	}
	if !validUUID(attemptID) {
		return CompletionResult{}, errors.New("delivery attempt id generator returned a non-UUID value")
	}

	var message Message
	if shouldRetry {
		message, err = s.opts.BuildRetryMessage(RetryMessageInput{
			OrganizationID: claim.OrganizationID,
			DeliveryID:     claim.DeliveryID,
			EventID:        claim.EventID,
			Sequence:       retrySequence,
			NextAttemptAt:  nextAttemptAt,
		})
		if err != nil {
			return CompletionResult{}, fmt.Errorf("build delivery retry outbox message: %w", err)
		}
		if err := validateMessage(message, retrySequence); err != nil {
			return CompletionResult{}, err
		}
	}

	errorText := failureDiagnostic(outcome)
	lastResponse := nullableStatus(outcome.ResponseStatus)

	if err := insertAttempt(ctx, tx, attemptID, claim, locked, outcome, attemptNumber); err != nil {
		return CompletionResult{}, err
	}

	tag, err := tx.Exec(ctx, completionUpdateSQL,
		claim.OrganizationID, claim.DeliveryID, claim.DispatchSeq, claim.LeaseToken,
		state, nullableTime(nextAttemptAt), t, lastResponse, errorText,
		nullableTime(completedAt(state, t)), retrySequence,
	)
	if err != nil {
		return CompletionResult{}, fmt.Errorf("persist delivery completion: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return CompletionResult{}, errors.New("delivery completion fence was lost after validation")
	}

	if shouldRetry {
		if _, err := tx.Exec(ctx, insertOutboxSQL,
			message.ID, claim.OrganizationID, claim.DeliveryID, message.Sequence,
			message.Kind, message.Topic, message.Payload, nullableJSON(message.Headers),
			nextAttemptAt, t,
		); err != nil {
			return CompletionResult{}, fmt.Errorf("insert delivery retry outbox row: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return CompletionResult{}, fmt.Errorf("commit delivery completion transaction: %w", err)
	}

	s.logger.Info("delivery completion applied",
		"status", state,
		"expired", expired,
		"retry", shouldRetry,
		"attempt_number", attemptNumber,
	)
	return CompletionResult{
		Applied:       true,
		Expired:       expired,
		Status:        state,
		AttemptNumber: attemptNumber,
		RetrySequence: retrySequence,
	}, nil
}

// DecryptFailureOptions are the explicit pre-HTTP processing-disposition inputs.
type DecryptFailureOptions struct {
	Pacing time.Duration
	// Error is the raw processing error. It is normalized, credential-scrubbed
	// and bounded before it is persisted; callers must not assume that arbitrary
	// binary or unstructured payloads are fully scrubbed.
	Error string
}

// DecryptFailureResult reports the durable pre-HTTP disposition.
type DecryptFailureResult struct {
	Applied       bool
	Fenced        bool
	Expired       bool
	NextAttemptAt time.Time
	RetrySequence int
	AckEligible   bool
}

// RecordDecryptFailure persists the approved B5 secret-unavailability
// disposition. It is a compatibility wrapper over RecordPreHTTPFailure with the
// transient secret_unavailable reason; it creates no attempt row, does not
// increment attempt_count, and performs no HTTP.
func (s *Store) RecordDecryptFailure(ctx context.Context, claim Claim, opts DecryptFailureOptions) (DecryptFailureResult, error) {
	result, err := s.RecordPreHTTPFailure(ctx, claim, PreHTTPFailureOptions{
		Reason: ReasonSecretUnavailable,
		Pacing: opts.Pacing,
		Error:  opts.Error,
	})
	if err != nil {
		return DecryptFailureResult{}, err
	}
	return DecryptFailureResult{
		Applied:       result.Applied,
		Fenced:        result.Fenced,
		Expired:       result.Expired,
		NextAttemptAt: result.NextAttemptAt,
		RetrySequence: result.RetrySequence,
		AckEligible:   result.AckEligible,
	}, nil
}

// expirePreHTTPFailure persists the pre-HTTP terminal expiry disposition. It
// writes no attempt row, does not increment attempt_count and creates no
// dispatch.
func (s *Store) expirePreHTTPFailure(ctx context.Context, tx pgx.Tx, claim Claim, t time.Time, diagnostic, reason string) (PreHTTPFailureResult, error) {
	tag, err := tx.Exec(ctx, expireAfterDecryptFailureSQL,
		claim.OrganizationID, claim.DeliveryID, claim.DispatchSeq, claim.LeaseToken, t, diagnostic,
	)
	if err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("expire delivery after pre-HTTP failure: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return PreHTTPFailureResult{}, errors.New("pre-HTTP failure expiry fence was lost after validation")
	}
	if err := tx.Commit(ctx); err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("commit pre-HTTP failure expiry transaction: %w", err)
	}
	s.logger.Info("pre-HTTP disposition expired", "reason", reason)
	return PreHTTPFailureResult{
		Applied:     true,
		Expired:     true,
		AckEligible: true,
	}, nil
}

type lockedDelivery struct {
	eventID        string
	dispatchSeq    int
	state          string
	leaseToken     string
	leaseExpiresAt pgtype.Timestamptz
	attemptCount   int
	nextAttemptAt  pgtype.Timestamptz
	eventExpiresAt time.Time
	requestURL     string
	requestHeaders string
}

const lockDeliverySQL = `
SELECT d.event_id::text, d.dispatch_seq, d.state,
       COALESCE(d.dispatch_lease_token::text, ''), d.dispatch_lease_expires_at, d.attempt_count,
       d.next_attempt_at, d.url, d.http_headers::text, e.expires_at
  FROM deliveries d
  JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
 WHERE d.organization_id = $1::uuid
   AND d.id = $2::uuid
 FOR UPDATE OF d`

func lockDelivery(ctx context.Context, tx pgx.Tx, organizationID, deliveryID string) (lockedDelivery, bool, error) {
	var d lockedDelivery
	err := tx.QueryRow(ctx, lockDeliverySQL, organizationID, deliveryID).Scan(
		&d.eventID,
		&d.dispatchSeq,
		&d.state,
		&d.leaseToken,
		&d.leaseExpiresAt,
		&d.attemptCount,
		&d.nextAttemptAt,
		&d.requestURL,
		&d.requestHeaders,
		&d.eventExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return lockedDelivery{}, false, nil
	}
	if err != nil {
		return lockedDelivery{}, false, fmt.Errorf("lock delivery completion row: %w", err)
	}
	return d, true, nil
}

// leaseExpired reports whether the authoritative locked lease expiry is at or
// before the fresh database timestamp. An absent expiry on a matching lease
// token cannot prove a live lease and is treated as expired.
func (d lockedDelivery) leaseExpired(t time.Time) bool {
	return !d.leaseExpiresAt.Valid || !d.leaseExpiresAt.Time.After(t)
}

func completionFenced(d lockedDelivery, claim Claim) bool {
	if d.eventID != claim.EventID {
		return true
	}
	if d.dispatchSeq != claim.DispatchSeq {
		return true
	}
	if d.leaseToken != claim.LeaseToken {
		return true
	}
	return d.state != "pending" && d.state != "retrying"
}

func fenceReason(d lockedDelivery, found bool, claim Claim) string {
	if !found {
		return "delivery missing"
	}
	if d.eventID != claim.EventID {
		return "event mismatch"
	}
	if d.dispatchSeq != claim.DispatchSeq {
		return "generation mismatch"
	}
	if d.leaseToken != claim.LeaseToken {
		return "token mismatch"
	}
	if d.state != "pending" && d.state != "retrying" {
		return "terminal state"
	}
	return "fenced"
}

const insertAttemptSQL = `
INSERT INTO delivery_attempts
    (id, organization_id, delivery_id, attempt_number, request_method, request_url,
     request_headers, response_status, response_headers, response_body_snippet,
     outcome, error_class, error_message, duration_ms, started_at, finished_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8, $9::jsonb,
        $10, $11, $12, $13, $14, $15::timestamptz, $16::timestamptz)`

func insertAttempt(ctx context.Context, tx pgx.Tx, attemptID string, claim Claim, locked lockedDelivery, outcome Outcome, attemptNumber int) error {
	requestHeaders, err := mergeRequestHeaders(locked.requestHeaders, outcome.RequestHeaders)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, insertAttemptSQL,
		attemptID, claim.OrganizationID, claim.DeliveryID, attemptNumber,
		outcome.RequestMethod, locked.requestURL, requestHeaders,
		nullableStatus(outcome.ResponseStatus), nullableJSON(outcome.ResponseHeaders),
		nullableString(outcome.ResponseBodySnippet), string(outcome.Kind),
		nullableString(outcome.ErrorClass), nullableString(boundedDiagnostic(outcome.ErrorMessage)),
		int(outcome.Duration/time.Millisecond), outcome.StartedAt, outcome.FinishedAt,
	)
	if err != nil {
		return fmt.Errorf("insert delivery attempt: %w", err)
	}
	return nil
}

// sanitizeRequestHeaders parses the snapshot as an object and replaces every
// credential-bearing value. Non-sensitive RawMessage values retain their JSON
// representation; malformed or non-object input fails closed before persistence.
func sanitizeRequestHeaders(value string) (string, error) {
	headers, err := parseSanitizedHeaders(value)
	if err != nil {
		return "", err
	}
	sanitized, err := json.Marshal(headers)
	if err != nil {
		return "", fmt.Errorf("sanitize delivery request headers: %w", err)
	}
	return string(sanitized), nil
}

func parseSanitizedHeaders(value string) (map[string]json.RawMessage, error) {
	var headers map[string]json.RawMessage
	if err := json.Unmarshal([]byte(value), &headers); err != nil {
		return nil, fmt.Errorf("delivery request headers are not valid JSON: %w", err)
	}
	if headers == nil {
		return nil, errors.New("delivery request headers must be a JSON object")
	}
	for name := range headers {
		if sensitiveRequestHeader(name) {
			headers[name] = json.RawMessage(`"[REDACTED]"`)
		}
	}
	return headers, nil
}

// mergeRequestHeaders persists the snapshot plus the exact webhook-* headers
// that were transmitted on a completed attempt. Only the three protocol headers
// are accepted from the executor; they are validated structurally and never
// contain a secret. Snapshot credentials remain sanitized.
func mergeRequestHeaders(snapshot, transmitted string) (string, error) {
	headers, err := parseSanitizedHeaders(snapshot)
	if err != nil {
		return "", err
	}
	if transmitted != "" {
		var audited map[string]string
		if err := json.Unmarshal([]byte(transmitted), &audited); err != nil || audited == nil {
			return "", errors.New("transmitted request headers are not a JSON object")
		}
		for name, value := range audited {
			lower := strings.ToLower(strings.TrimSpace(name))
			switch lower {
			case "webhook-id", "webhook-timestamp", "webhook-signature":
			default:
				return "", errors.New("transmitted request header is not allowlisted")
			}
			if !validTransmittedHeader(lower, value) {
				return "", errors.New("transmitted request header is invalid")
			}
			removeHeaderFold(headers, lower)
			encoded, err := json.Marshal(value)
			if err != nil {
				return "", errors.New("transmitted request header cannot be encoded")
			}
			headers[lower] = encoded
		}
	}
	merged, err := json.Marshal(headers)
	if err != nil {
		return "", fmt.Errorf("merge delivery request headers: %w", err)
	}
	return string(merged), nil
}

func removeHeaderFold(headers map[string]json.RawMessage, lower string) {
	for name := range headers {
		if strings.EqualFold(name, lower) {
			delete(headers, name)
		}
	}
}

func validTransmittedHeader(name, value string) bool {
	if strings.ContainsAny(value, "\r\n\x00") {
		return false
	}
	switch name {
	case "webhook-id":
		return validUUID(value) && value == strings.ToLower(value)
	case "webhook-timestamp":
		if value == "" || len(value) > 19 || (len(value) > 1 && value[0] == '0') {
			return false
		}
		for i := 0; i < len(value); i++ {
			if value[i] < '0' || value[i] > '9' {
				return false
			}
		}
		return true
	case "webhook-signature":
		if !strings.HasPrefix(value, "v1,") || len(value) > 64 {
			return false
		}
		decoded, err := base64.StdEncoding.DecodeString(value[len("v1,"):])
		return err == nil && len(decoded) == 32
	default:
		return false
	}
}

func sensitiveRequestHeader(name string) bool {
	return endpoint.CredentialBearingHeader(name)
}

const completionUpdateSQL = `
UPDATE deliveries
   SET state = $5,
       attempt_count = attempt_count + 1,
       next_attempt_at = $6::timestamptz,
       last_attempt_at = $7::timestamptz,
       last_response_status = $8,
       last_error = $9,
       completed_at = $10::timestamptz,
       updated_at = $7::timestamptz,
       dispatch_seq = $11,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL
 WHERE organization_id = $1::uuid
   AND id = $2::uuid
   AND dispatch_seq = $3
   AND dispatch_lease_token = $4::uuid
   AND state IN ('pending', 'retrying')`

const expireAfterDecryptFailureSQL = `
UPDATE deliveries
   SET state = 'expired',
       completed_at = $5::timestamptz,
       next_attempt_at = NULL,
       last_error = $6,
       updated_at = $5::timestamptz,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL
 WHERE organization_id = $1::uuid
   AND id = $2::uuid
   AND dispatch_seq = $3
   AND dispatch_lease_token = $4::uuid
   AND state IN ('pending', 'retrying')`

const decryptFailureUpdateSQL = `
UPDATE deliveries
   SET next_attempt_at = GREATEST(next_attempt_at, $5::timestamptz),
       last_error = $6,
       updated_at = $7::timestamptz,
       dispatch_seq = $8,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL
 WHERE organization_id = $1::uuid
   AND id = $2::uuid
   AND dispatch_seq = $3
   AND dispatch_lease_token = $4::uuid
   AND state IN ('pending', 'retrying')`

const insertOutboxSQL = `
INSERT INTO outbox_messages
    (id, organization_id, delivery_id, sequence, kind, topic, payload, headers,
     status, attempts, available_at, created_at, updated_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, $6, $7::jsonb, $8::jsonb,
        'pending', 0, $9::timestamptz, $10::timestamptz, $10::timestamptz)`

func validateClaim(claim Claim) error {
	if !validUUID(claim.OrganizationID) {
		return errors.New("delivery claim organization id is not a UUID")
	}
	if !validUUID(claim.DeliveryID) {
		return errors.New("delivery claim id is not a UUID")
	}
	if !validUUID(claim.EventID) {
		return errors.New("delivery claim event id is not a UUID")
	}
	if !validUUID(claim.EndpointID) {
		return errors.New("delivery claim endpoint id is not a UUID")
	}
	if claim.DispatchSeq < 1 {
		return errors.New("delivery claim generation must be positive")
	}
	if !validUUID(claim.LeaseToken) {
		return errors.New("delivery claim lease token is not a UUID")
	}
	if claim.LeaseExpiresAt.IsZero() {
		return errors.New("delivery claim lease expiry is required")
	}
	if claim.AttemptCount < 0 {
		return errors.New("delivery claim attempt count must not be negative")
	}
	return nil
}

func validateOutcome(outcome Outcome) error {
	switch outcome.Kind {
	case OutcomeSuccess, OutcomeRetryableFailure, OutcomeTerminalFailure:
	default:
		return errors.New("delivery outcome kind is not supported")
	}
	if strings.TrimSpace(outcome.RequestMethod) == "" {
		return errors.New("delivery attempt request method is required")
	}
	if outcome.ResponseStatus != nil && (*outcome.ResponseStatus < 100 || *outcome.ResponseStatus > 599) {
		return errors.New("delivery attempt response status must be between 100 and 599")
	}
	if outcome.ResponseHeaders != "" && !json.Valid([]byte(outcome.ResponseHeaders)) {
		return errors.New("delivery attempt response headers are not valid JSON")
	}
	if utf8.RuneCountInString(outcome.ResponseBodySnippet) > maxResponseSnippetRunes {
		return errors.New("delivery attempt response body snippet exceeds 2000 characters")
	}
	if outcome.ErrorClass != "" {
		switch outcome.ErrorClass {
		case "timeout", "connect", "tls", "dns", "http_status", "internal":
		default:
			return errors.New("delivery attempt error class is not supported")
		}
	}
	if len(outcome.ErrorMessage) > 500 {
		return errors.New("delivery attempt error message exceeds 500 characters")
	}
	if outcome.Duration < 0 {
		return errors.New("delivery attempt duration must not be negative")
	}
	if outcome.Duration > time.Duration(math.MaxInt32)*time.Millisecond {
		return errors.New("delivery attempt duration is too large")
	}
	if outcome.StartedAt.IsZero() || outcome.FinishedAt.IsZero() {
		return errors.New("delivery attempt start and finish times are required")
	}
	if outcome.FinishedAt.Before(outcome.StartedAt) {
		return errors.New("delivery attempt finished before it started")
	}
	return nil
}

func validateDecision(kind OutcomeKind, decision RetryDecision) error {
	if kind != OutcomeRetryableFailure && decision.Retry {
		return errors.New("retry is only valid for a retryable HTTP failure")
	}
	if decision.Retry && decision.Backoff <= 0 {
		return errors.New("delivery retry backoff must be positive")
	}
	return nil
}

func validateMessage(message Message, sequence int) error {
	if !validUUID(message.ID) {
		return errors.New("delivery retry message id is not a UUID")
	}
	if message.Sequence != sequence {
		return errors.New("delivery retry message sequence does not match the generation")
	}
	if message.Kind != "delivery.retry" {
		return errors.New("delivery retry message kind must be delivery.retry")
	}
	if strings.TrimSpace(message.Topic) == "" {
		return errors.New("delivery retry message topic is required")
	}
	if !json.Valid([]byte(message.Payload)) {
		return errors.New("delivery retry message payload is not valid JSON")
	}
	if message.Headers != "" && !json.Valid([]byte(message.Headers)) {
		return errors.New("delivery retry message headers are not valid JSON")
	}
	return nil
}

func failureDiagnostic(outcome Outcome) any {
	if outcome.Kind == OutcomeSuccess {
		return nil
	}
	message := boundedDiagnostic(outcome.ErrorMessage)
	if message == "" {
		message = outcome.ErrorClass
	}
	return nullableString(message)
}

func nullableStatus(status *int) any {
	if status == nil {
		return nil
	}
	return *status
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t
}

func completedAt(state string, t time.Time) time.Time {
	if state == "succeeded" || state == "failed" || state == "expired" {
		return t
	}
	return time.Time{}
}

func sampleTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var t time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("sample delivery database time: %w", err)
	}
	return t, nil
}

// diagnosticLimit is the approved char_length limit for deliveries.last_error
// and delivery_attempts.error_message.
const (
	diagnosticLimit         = 500
	maxResponseSnippetRunes = 2000
)

// Credential-bearing patterns are redacted conservatively before storage by
// platform/diagnostics; this package keeps the approved storage limit here.

// boundedDiagnostic normalizes a raw diagnostic, redacts recognizable
// credential-bearing values, collapses whitespace and truncates to the approved
// character limit without splitting a UTF-8 code point.
func boundedDiagnostic(value string) string {
	return diagnostics.Bounded(value, diagnosticLimit)
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

// RandomToken generates a cryptographically random UUIDv4-shaped fencing
// token. It is not an entity ID and is not a selected dependency.
func RandomToken() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate random token: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}
