//go:build integration

package delivery

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
)

// Worker acquisition is intentionally global: it selects the oldest eligible
// delivery in the whole database. These tests therefore require an otherwise
// empty delivery/outbox population and skip rather than mutate data that does
// not belong to the test. Run integration packages with -p 1.
type integrationFixture struct {
	pool       *pgxpool.Pool
	orgID      string
	endpointID string
	seedCount  int
}

func newIntegrationFixture(t *testing.T) *integrationFixture {
	t.Helper()

	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("NewPool() error: %v", err)
	}
	t.Cleanup(pool.Close)

	var deliveries, outbox int
	if err := pool.Raw().QueryRow(ctx,
		`SELECT (SELECT count(*) FROM deliveries), (SELECT count(*) FROM outbox_messages)`,
	).Scan(&deliveries, &outbox); err != nil {
		t.Fatalf("check integration isolation: %v", err)
	}
	if deliveries != 0 || outbox != 0 {
		t.Skipf("delivery integration tests require an isolated database; found %d deliveries and %d outbox rows", deliveries, outbox)
	}

	f := &integrationFixture{
		pool:       pool.Raw(),
		orgID:      testUUID(t),
		endpointID: testUUID(t),
	}

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		f.orgID, "delivery-test-"+f.orgID,
	); err != nil {
		t.Fatalf("insert organization: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := f.pool.Exec(cleanupCtx, `DELETE FROM organizations WHERE id = $1::uuid`, f.orgID); err != nil {
			t.Errorf("cleanup organization: %v", err)
		}
	})

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO endpoints (id, organization_id, name, url, secret_encrypted, status)
		 VALUES ($1::uuid, $2::uuid, $3, 'https://example.test/hook', 'test-ciphertext', 'active')`,
		f.endpointID, f.orgID, "endpoint-"+f.endpointID,
	); err != nil {
		t.Fatalf("insert endpoint: %v", err)
	}
	return f
}

type deliverySeed struct {
	state          string
	dispatchSeq    int
	attemptCount   int
	nextAttemptAt  time.Time
	leaseToken     string
	leaseExpiresAt time.Time
	eventReceived  time.Time
	eventExpiresAt time.Time
	completedAt    time.Time
	url            string
	headers        string
	secret         string
	payload        string
	eventType      string
}

type seededDelivery struct {
	id      string
	eventID string
}

func (f *integrationFixture) seedDelivery(t *testing.T, spec deliverySeed) seededDelivery {
	t.Helper()

	now := time.Now()
	f.seedCount++
	if spec.state == "" {
		spec.state = "pending"
	}
	if spec.dispatchSeq == 0 {
		spec.dispatchSeq = 1
	}
	if spec.url == "" {
		spec.url = "https://example.test/hook"
	}
	if spec.headers == "" {
		spec.headers = `{"x-test":"value"}`
	}
	if spec.secret == "" {
		spec.secret = "test-ciphertext"
	}
	if spec.payload == "" {
		spec.payload = `{"source":"fixture"}`
	}
	if spec.eventType == "" {
		spec.eventType = "test.delivery"
	}
	if spec.eventReceived.IsZero() {
		spec.eventReceived = now.Add(-time.Hour)
	}
	if spec.eventExpiresAt.IsZero() {
		spec.eventExpiresAt = now.Add(time.Hour)
	}
	live := spec.state == "pending" || spec.state == "retrying"
	if live && spec.nextAttemptAt.IsZero() {
		spec.nextAttemptAt = now.Add(-time.Minute)
	}
	if spec.leaseToken != "" && spec.leaseExpiresAt.IsZero() {
		spec.leaseExpiresAt = now.Add(5 * time.Minute)
	}
	if !live && spec.completedAt.IsZero() {
		spec.completedAt = now
	}

	var nextAttemptAt any
	if live {
		nextAttemptAt = spec.nextAttemptAt
	}
	var completedAt any
	if !spec.completedAt.IsZero() {
		completedAt = spec.completedAt
	}
	var leaseToken any
	var leaseExpiresAt any
	if spec.leaseToken != "" {
		leaseToken = spec.leaseToken
		leaseExpiresAt = spec.leaseExpiresAt
	}

	deliveryID := testUUID(t)
	eventID := testUUID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO events
		     (id, organization_id, type, payload, destination_count, received_at, expires_at)
		 VALUES ($1::uuid, $2::uuid, $3, $6::jsonb, 1, $4::timestamptz, $5::timestamptz)`,
		eventID, f.orgID, spec.eventType, spec.eventReceived, spec.eventExpiresAt, spec.payload,
	); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO deliveries
		     (id, organization_id, event_id, endpoint_id, url, http_headers, secret_encrypted,
		      state, dispatch_seq, attempt_count, next_attempt_at, created_at, updated_at,
		      completed_at, dispatch_lease_token, dispatch_lease_expires_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5, $6::jsonb, $7,
		         $8, $9, $10, $11::timestamptz, $12::timestamptz, $12::timestamptz,
		         $13::timestamptz, $14::uuid, $15::timestamptz)`,
		deliveryID, f.orgID, eventID, f.endpointID, spec.url, spec.headers, spec.secret,
		spec.state, spec.dispatchSeq, spec.attemptCount, nextAttemptAt, now,
		completedAt, leaseToken, leaseExpiresAt,
	); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	return seededDelivery{id: deliveryID, eventID: eventID}
}

type deliveryRow struct {
	state              string
	dispatchSeq        int
	attemptCount       int
	nextAttemptAt      pgtype.Timestamptz
	completedAt        pgtype.Timestamptz
	lastAttemptAt      pgtype.Timestamptz
	leaseToken         string
	leaseExpiresAt     pgtype.Timestamptz
	lastResponseStatus pgtype.Int4
	lastError          pgtype.Text
}

func (f *integrationFixture) readDelivery(t *testing.T, id string) deliveryRow {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var row deliveryRow
	err := f.pool.QueryRow(ctx,
		`SELECT state, dispatch_seq, attempt_count, next_attempt_at, completed_at, last_attempt_at,
		        COALESCE(dispatch_lease_token::text, ''), dispatch_lease_expires_at,
		        last_response_status, last_error
		   FROM deliveries WHERE id = $1::uuid`,
		id,
	).Scan(&row.state, &row.dispatchSeq, &row.attemptCount, &row.nextAttemptAt,
		&row.completedAt, &row.lastAttemptAt, &row.leaseToken, &row.leaseExpiresAt,
		&row.lastResponseStatus, &row.lastError)
	if err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return row
}

type attemptRow struct {
	attemptNumber   int
	requestMethod   string
	requestURL      string
	requestHeaders  string
	responseStatus  pgtype.Int4
	responseHeaders pgtype.Text
	responseBody    pgtype.Text
	outcome         string
	errorClass      pgtype.Text
	errorMessage    pgtype.Text
	durationMS      pgtype.Int4
	startedAt       time.Time
	finishedAt      time.Time
}

func (f *integrationFixture) attempts(t *testing.T, deliveryID string) []attemptRow {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := f.pool.Query(ctx,
		`SELECT attempt_number, request_method, request_url, request_headers::text,
		        response_status, response_headers::text, response_body_snippet, outcome,
		        error_class, error_message, duration_ms, started_at, finished_at
		   FROM delivery_attempts WHERE delivery_id = $1::uuid
		  ORDER BY attempt_number`,
		deliveryID,
	)
	if err != nil {
		t.Fatalf("query delivery attempts: %v", err)
	}
	defer rows.Close()

	var result []attemptRow
	for rows.Next() {
		var row attemptRow
		if err := rows.Scan(&row.attemptNumber, &row.requestMethod, &row.requestURL,
			&row.requestHeaders, &row.responseStatus, &row.responseHeaders, &row.responseBody,
			&row.outcome, &row.errorClass, &row.errorMessage, &row.durationMS,
			&row.startedAt, &row.finishedAt); err != nil {
			t.Fatalf("scan delivery attempt: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate delivery attempts: %v", err)
	}
	return result
}

func (f *integrationFixture) attemptCount(t *testing.T, deliveryID string) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM delivery_attempts WHERE delivery_id = $1::uuid`, deliveryID,
	).Scan(&count); err != nil {
		t.Fatalf("count delivery attempts: %v", err)
	}
	return count
}

type outboxRow struct {
	id          string
	sequence    int
	kind        string
	status      string
	attempts    int
	availableAt time.Time
	leaseToken  string
	publishedAt pgtype.Timestamptz
	lastError   pgtype.Text
	payload     string
	headers     pgtype.Text
}

const outboxColumns = `id::text, sequence, kind, status, attempts, available_at,
	COALESCE(lease_token::text, ''), published_at, last_error, payload::text, headers::text`

func (f *integrationFixture) outboxRows(t *testing.T, deliveryID string) []outboxRow {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rows, err := f.pool.Query(ctx,
		`SELECT `+outboxColumns+` FROM outbox_messages
		  WHERE delivery_id = $1::uuid ORDER BY sequence`, deliveryID,
	)
	if err != nil {
		t.Fatalf("query outbox rows: %v", err)
	}
	defer rows.Close()

	var result []outboxRow
	for rows.Next() {
		var row outboxRow
		if err := rows.Scan(&row.id, &row.sequence, &row.kind, &row.status, &row.attempts,
			&row.availableAt, &row.leaseToken, &row.publishedAt, &row.lastError,
			&row.payload, &row.headers); err != nil {
			t.Fatalf("scan outbox row: %v", err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate outbox rows: %v", err)
	}
	return result
}

func (f *integrationFixture) outboxFor(t *testing.T, deliveryID string, sequence int) (outboxRow, bool) {
	t.Helper()

	for _, row := range f.outboxRows(t, deliveryID) {
		if row.sequence == sequence {
			return row, true
		}
	}
	return outboxRow{}, false
}

func (f *integrationFixture) insertOutbox(t *testing.T, deliveryID string, sequence int, kind string, availableAt time.Time) string {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	id := testUUID(t)
	_, err := f.pool.Exec(ctx,
		`INSERT INTO outbox_messages
		     (id, organization_id, delivery_id, sequence, kind, topic, payload, status, attempts, available_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 'test.delivery', '{"source":"external"}'::jsonb,
		         'pending', 0, $6::timestamptz)`,
		id, f.orgID, deliveryID, sequence, kind, availableAt,
	)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return id
}

func (f *integrationFixture) setEventExpiresAt(t *testing.T, eventID string, expiresAt time.Time) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := f.pool.Exec(ctx,
		`UPDATE events SET expires_at = $2::timestamptz WHERE id = $1::uuid`, eventID, expiresAt,
	); err != nil {
		t.Fatalf("update event expiry: %v", err)
	}
}

func (f *integrationFixture) expireEvent(t *testing.T, eventID string) {
	t.Helper()
	f.setEventExpiresAt(t, eventID, time.Now().Add(-5*time.Second))
}

func (f *integrationFixture) eventExpiresAt(t *testing.T, eventID string) time.Time {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var expiresAt time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT expires_at FROM events WHERE id = $1::uuid`, eventID,
	).Scan(&expiresAt); err != nil {
		t.Fatalf("read event expiry: %v", err)
	}
	return expiresAt
}

func integrationBuilder() MessageBuilder {
	return func(input RetryMessageInput) (Message, error) {
		id, err := randomUUID()
		if err != nil {
			return Message{}, err
		}
		return Message{
			ID:       id,
			Sequence: input.Sequence,
			Kind:     "delivery.retry",
			Topic:    "test.delivery",
			Payload:  fmt.Sprintf(`{"delivery_id":%q,"sequence":%d}`, input.DeliveryID, input.Sequence),
			Headers:  fmt.Sprintf(`{"x-generation":%d}`, input.Sequence),
		}, nil
	}
}

func newTestStore(t *testing.T, f *integrationFixture, mutate func(*Options)) *Store {
	t.Helper()

	opts := Options{
		NewID:             randomUUID,
		BuildRetryMessage: integrationBuilder(),
		Logger:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&opts)
	}
	store, err := NewStore(f.pool, opts)
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	return store
}

func acquire(t *testing.T, store *Store, lease time.Duration, maxAttempts int) *Claim {
	t.Helper()

	claim, err := store.Acquire(context.Background(), AcquireOptions{
		LeaseDuration: lease,
		MaxAttempts:   maxAttempts,
		NewToken:      RandomToken,
	})
	if err != nil {
		t.Fatalf("Acquire() error: %v", err)
	}
	if claim == nil {
		t.Fatal("Acquire() returned no claim")
	}
	return claim
}

func successOutcome() Outcome {
	now := time.Now()
	status := 200
	return Outcome{
		Kind:                OutcomeSuccess,
		RequestMethod:       "POST",
		ResponseStatus:      &status,
		ResponseHeaders:     `{"content-type":"application/json"}`,
		ResponseBodySnippet: "ok",
		Duration:            120 * time.Millisecond,
		StartedAt:           now.Add(-200 * time.Millisecond),
		FinishedAt:          now,
	}
}

func retryableOutcome() Outcome {
	now := time.Now()
	status := 503
	return Outcome{
		Kind:           OutcomeRetryableFailure,
		RequestMethod:  "POST",
		ResponseStatus: &status,
		ErrorClass:     "http_status",
		ErrorMessage:   "destination temporarily unavailable",
		Duration:       80 * time.Millisecond,
		StartedAt:      now.Add(-150 * time.Millisecond),
		FinishedAt:     now,
	}
}

func terminalOutcome() Outcome {
	now := time.Now()
	status := 400
	return Outcome{
		Kind:           OutcomeTerminalFailure,
		RequestMethod:  "POST",
		ResponseStatus: &status,
		ErrorClass:     "http_status",
		ErrorMessage:   "destination rejected the payload",
		Duration:       60 * time.Millisecond,
		StartedAt:      now.Add(-100 * time.Millisecond),
		FinishedAt:     now,
	}
}

func requireTimestamptzNear(t *testing.T, name string, got pgtype.Timestamptz, want time.Time, tolerance time.Duration) {
	t.Helper()
	if !got.Valid {
		t.Fatalf("%s is NULL, want %s", name, want.Format(time.RFC3339Nano))
	}
	if diff := got.Time.Sub(want); diff < -tolerance || diff > tolerance {
		t.Fatalf("%s = %s, want within %s of %s", name, got.Time.Format(time.RFC3339Nano), tolerance, want.Format(time.RFC3339Nano))
	}
}

func requireTimeNear(t *testing.T, name string, got, want time.Time, tolerance time.Duration) {
	t.Helper()
	if diff := got.Sub(want); diff < -tolerance || diff > tolerance {
		t.Fatalf("%s = %s, want within %s of %s", name, got.Format(time.RFC3339Nano), tolerance, want.Format(time.RFC3339Nano))
	}
}

func testUUID(t *testing.T) string {
	t.Helper()
	id, err := randomUUID()
	if err != nil {
		t.Fatalf("generate test uuid: %v", err)
	}
	return id
}

func randomUUID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", fmt.Errorf("generate random uuid: %w", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

func TestAcquireInstallsLeaseIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})

	claim := acquire(t, store, time.Minute, 3)
	if claim.DeliveryID != d.id || claim.EventID != d.eventID {
		t.Fatalf("acquired the wrong delivery: %+v", claim)
	}
	if claim.DispatchSeq != 1 || claim.AttemptCount != 0 {
		t.Fatalf("unexpected generation/attempts: %+v", claim)
	}
	if claim.RequestURL != "https://example.test/hook" || claim.RequestHeaders != `{"x-test": "value"}` {
		t.Fatalf("unexpected request snapshot: %+v", claim)
	}
	if claim.SecretCiphertext != "test-ciphertext" || claim.EventType != "test.delivery" {
		t.Fatalf("unexpected secret/event snapshot: %+v", claim)
	}
	if !strings.Contains(claim.EventPayload, "fixture") {
		t.Fatalf("event payload = %q", claim.EventPayload)
	}
	if !validUUID(claim.LeaseToken) {
		t.Fatalf("lease token is not a UUID: %q", claim.LeaseToken)
	}

	row := f.readDelivery(t, d.id)
	if row.leaseToken != claim.LeaseToken {
		t.Fatalf("installed lease token = %q, want %q", row.leaseToken, claim.LeaseToken)
	}
	requireTimestamptzNear(t, "lease expiry", row.leaseExpiresAt, claim.LeaseExpiresAt, 2*time.Millisecond)
	if row.state != "pending" || row.attemptCount != 0 || row.dispatchSeq != 1 {
		t.Fatalf("acquisition mutated the delivery: %+v", row)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("acquisition wrote %d attempt rows", n)
	}

	if again, err := store.Acquire(context.Background(), AcquireOptions{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		NewToken:      RandomToken,
	}); err != nil {
		t.Fatalf("second Acquire() error: %v", err)
	} else if again != nil {
		t.Fatalf("live lease was acquired twice: %+v", again)
	}
}

func TestAcquireNoEligibleCandidateIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	now := time.Now()

	future := f.seedDelivery(t, deliverySeed{nextAttemptAt: now.Add(time.Hour)})
	expiredEvent := f.seedDelivery(t, deliverySeed{
		nextAttemptAt:  now.Add(-time.Minute),
		eventReceived:  now.Add(-2 * time.Hour),
		eventExpiresAt: now.Add(-time.Hour),
	})
	exhausted := f.seedDelivery(t, deliverySeed{
		nextAttemptAt: now.Add(-time.Minute),
		attemptCount:  3,
	})
	terminal := f.seedDelivery(t, deliverySeed{state: "succeeded"})
	leased := f.seedDelivery(t, deliverySeed{
		nextAttemptAt:  now.Add(-time.Minute),
		leaseToken:     testUUID(t),
		leaseExpiresAt: now.Add(time.Hour),
	})

	if claim, err := store.Acquire(context.Background(), AcquireOptions{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		NewToken:      RandomToken,
	}); err != nil {
		t.Fatalf("Acquire() error: %v", err)
	} else if claim != nil {
		t.Fatalf("acquired an ineligible delivery: %+v", claim)
	}

	for _, d := range []seededDelivery{future, expiredEvent, exhausted, terminal} {
		if row := f.readDelivery(t, d.id); row.leaseToken != "" {
			t.Fatalf("ineligible delivery %s received a lease: %+v", d.id, row)
		}
	}
	if row := f.readDelivery(t, leased.id); row.leaseToken == "" {
		t.Fatalf("pre-existing lease was cleared: %+v", row)
	}

	// The final permitted attempt (M-1) remains eligible; M itself does not.
	lastChance := f.seedDelivery(t, deliverySeed{
		nextAttemptAt: now.Add(-2 * time.Minute),
		attemptCount:  2,
	})
	claim := acquire(t, store, time.Minute, 3)
	if claim.DeliveryID != lastChance.id || claim.AttemptCount != 2 {
		t.Fatalf("final permitted attempt was not acquired: %+v", claim)
	}
}

func TestAcquireSkipsLockedDeliveryIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	now := time.Now()

	locked := f.seedDelivery(t, deliverySeed{nextAttemptAt: now.Add(-2 * time.Minute)})
	free := f.seedDelivery(t, deliverySeed{nextAttemptAt: now.Add(-time.Minute)})

	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM deliveries WHERE id = $1::uuid FOR UPDATE`, locked.id); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock delivery: %v", err)
	}

	claim := acquire(t, store, time.Minute, 3)
	if claim.DeliveryID != free.id {
		_ = tx.Rollback(ctx)
		t.Fatalf("SKIP LOCKED was not honored: acquired %s, want %s", claim.DeliveryID, free.id)
	}
	if row := f.readDelivery(t, locked.id); row.leaseToken != "" {
		_ = tx.Rollback(ctx)
		t.Fatalf("locked delivery received a lease: %+v", row)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback lock transaction: %v", err)
	}
}

func TestAcquireTokenFailureIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})

	_, err := store.Acquire(context.Background(), AcquireOptions{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		NewToken: func() (string, error) {
			return "", errors.New("token source unavailable")
		},
	})
	if err == nil {
		t.Fatal("Acquire() accepted a failing token generator")
	}
	if row := f.readDelivery(t, d.id); row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("failed token generation installed a lease: %+v", row)
	}

	_, err = store.Acquire(context.Background(), AcquireOptions{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		NewToken: func() (string, error) {
			return "not-a-uuid", nil
		},
	})
	if err == nil {
		t.Fatal("Acquire() accepted a non-UUID token")
	}
	if row := f.readDelivery(t, d.id); row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("invalid token installed a lease: %+v", row)
	}
}

func TestAcquireExpiredLeaseIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	now := time.Now()
	oldToken := testUUID(t)

	d := f.seedDelivery(t, deliverySeed{
		nextAttemptAt:  now.Add(-time.Minute),
		leaseToken:     oldToken,
		leaseExpiresAt: now.Add(-time.Minute),
	})

	claim := acquire(t, store, time.Minute, 3)
	if claim.DeliveryID != d.id {
		t.Fatalf("acquired %s, want %s", claim.DeliveryID, d.id)
	}
	if claim.LeaseToken == oldToken {
		t.Fatal("acquired delivery reused the expired fencing token")
	}
	if !validUUID(claim.LeaseToken) {
		t.Fatalf("new lease token is not a UUID: %q", claim.LeaseToken)
	}
	row := f.readDelivery(t, d.id)
	if row.leaseToken != claim.LeaseToken {
		t.Fatalf("stored token = %q, want %q", row.leaseToken, claim.LeaseToken)
	}
	if !row.leaseExpiresAt.Valid || !row.leaseExpiresAt.Time.After(now) {
		t.Fatalf("lease was not renewed: %+v", row)
	}
	if row.state != "pending" || row.attemptCount != 0 {
		t.Fatalf("acquisition mutated the delivery: %+v", row)
	}
}

func TestCompleteSuccessIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	result, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Applied || result.Fenced || result.Expired {
		t.Fatalf("unexpected completion result: %+v", result)
	}
	if result.Status != "succeeded" || result.AttemptNumber != 1 || result.RetrySequence != 1 {
		t.Fatalf("unexpected completion accounting: %+v", result)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "succeeded" || row.attemptCount != 1 || row.dispatchSeq != 1 {
		t.Fatalf("unexpected delivery state: %+v", row)
	}
	if row.nextAttemptAt.Valid || !row.completedAt.Valid || !row.lastAttemptAt.Valid {
		t.Fatalf("scheduling columns are wrong: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("lease was not cleared: %+v", row)
	}
	if !row.lastResponseStatus.Valid || row.lastResponseStatus.Int32 != 200 {
		t.Fatalf("last response status = %+v", row.lastResponseStatus)
	}
	if row.lastError.Valid {
		t.Fatalf("success stored last_error = %q", row.lastError.String)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("success created %d outbox rows", len(rows))
	}

	attempts := f.attempts(t, d.id)
	if len(attempts) != 1 {
		t.Fatalf("attempt rows = %d, want 1", len(attempts))
	}
	attempt := attempts[0]
	if attempt.attemptNumber != 1 || attempt.outcome != "success" {
		t.Fatalf("unexpected attempt: %+v", attempt)
	}
	if attempt.requestMethod != "POST" || attempt.requestURL != "https://example.test/hook" {
		t.Fatalf("attempt snapshot = %+v", attempt)
	}
	if attempt.requestHeaders != `{"x-test": "value"}` {
		t.Fatalf("attempt headers = %q", attempt.requestHeaders)
	}
	if !attempt.responseStatus.Valid || attempt.responseStatus.Int32 != 200 {
		t.Fatalf("attempt response status = %+v", attempt.responseStatus)
	}
	if attempt.responseHeaders.String != `{"content-type": "application/json"}` {
		t.Fatalf("attempt response headers = %q", attempt.responseHeaders.String)
	}
	if attempt.responseBody.String != "ok" || attempt.durationMS.Int32 != 120 {
		t.Fatalf("attempt body/duration = %+v", attempt)
	}
	if attempt.errorClass.Valid || attempt.errorMessage.Valid {
		t.Fatalf("success attempt stored an error: %+v", attempt)
	}
}

func TestCompleteRetryIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	backoff := 45 * time.Second
	result, err := store.Complete(context.Background(), *claim, retryableOutcome(),
		RetryDecision{Retry: true, Backoff: backoff})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Applied || result.Expired || result.Status != "retrying" {
		t.Fatalf("unexpected completion result: %+v", result)
	}
	if result.AttemptNumber != 1 || result.RetrySequence != 2 {
		t.Fatalf("unexpected completion accounting: %+v", result)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "retrying" || row.attemptCount != 1 || row.dispatchSeq != 2 {
		t.Fatalf("unexpected delivery state: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid || row.completedAt.Valid {
		t.Fatalf("retry did not clear the lease or set completion: %+v", row)
	}
	expectedNext := time.Now().Add(backoff)
	requireTimestamptzNear(t, "retry gate", row.nextAttemptAt, expectedNext, 5*time.Second)
	if !row.lastError.Valid || !strings.Contains(row.lastError.String, "temporarily unavailable") {
		t.Fatalf("last_error = %+v", row.lastError)
	}

	rows := f.outboxRows(t, d.id)
	if len(rows) != 1 {
		t.Fatalf("outbox rows = %d, want 1", len(rows))
	}
	outbox := rows[0]
	if outbox.sequence != 2 || outbox.kind != "delivery.retry" || outbox.status != "pending" || outbox.attempts != 0 {
		t.Fatalf("retry outbox = %+v", outbox)
	}
	if outbox.leaseToken != "" {
		t.Fatalf("retry outbox carries a lease: %+v", outbox)
	}
	requireTimeNear(t, "outbox gate", outbox.availableAt, row.nextAttemptAt.Time, 2*time.Millisecond)

	var payload struct {
		Sequence   int    `json:"sequence"`
		DeliveryID string `json:"delivery_id"`
	}
	if err := json.Unmarshal([]byte(outbox.payload), &payload); err != nil {
		t.Fatalf("retry payload is not JSON: %v", err)
	}
	if payload.Sequence != 2 || payload.DeliveryID != d.id {
		t.Fatalf("retry payload = %+v", payload)
	}

	attempts := f.attempts(t, d.id)
	if len(attempts) != 1 || attempts[0].outcome != "retryable_failure" {
		t.Fatalf("attempt rows = %+v", attempts)
	}
	if attempts[0].errorClass.String != "http_status" {
		t.Fatalf("attempt error class = %+v", attempts[0].errorClass)
	}
}

func TestCompleteTerminalFailureIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	result, err := store.Complete(context.Background(), *claim, terminalOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Applied || result.Status != "failed" {
		t.Fatalf("unexpected completion result: %+v", result)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "failed" || row.attemptCount != 1 || row.dispatchSeq != 1 {
		t.Fatalf("unexpected delivery state: %+v", row)
	}
	if row.nextAttemptAt.Valid || !row.completedAt.Valid {
		t.Fatalf("terminal failure scheduling = %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("lease was not cleared: %+v", row)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("terminal failure created %d outbox rows", len(rows))
	}
	attempts := f.attempts(t, d.id)
	if len(attempts) != 1 || attempts[0].outcome != "terminal_failure" {
		t.Fatalf("attempt rows = %+v", attempts)
	}
}

func TestCompleteExpiryPrecedenceIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	successCase := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	successClaim := acquire(t, store, time.Minute, 3)
	f.expireEvent(t, successCase.eventID)

	result, err := store.Complete(context.Background(), *successClaim, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Applied || !result.Expired || result.Status != "expired" {
		t.Fatalf("unexpected expiry result: %+v", result)
	}

	row := f.readDelivery(t, successCase.id)
	if row.state != "expired" || row.attemptCount != 1 || row.dispatchSeq != 1 {
		t.Fatalf("expiry did not win: %+v", row)
	}
	if row.nextAttemptAt.Valid || !row.completedAt.Valid || row.leaseToken != "" {
		t.Fatalf("expiry did not terminate cleanly: %+v", row)
	}
	// The observed HTTP result is preserved even though the lifecycle state is
	// expired: a genuine success must not be converted into a failure.
	attempts := f.attempts(t, successCase.id)
	if len(attempts) != 1 || attempts[0].outcome != "success" {
		t.Fatalf("expired completion lost the HTTP result: %+v", attempts)
	}
	if rows := f.outboxRows(t, successCase.id); len(rows) != 0 {
		t.Fatalf("expired completion created %d outbox rows", len(rows))
	}

	retryCase := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	retryClaim := acquire(t, store, time.Minute, 3)
	f.expireEvent(t, retryCase.eventID)

	result, err = store.Complete(context.Background(), *retryClaim, retryableOutcome(),
		RetryDecision{Retry: true, Backoff: time.Minute})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Applied || !result.Expired || result.Status != "expired" {
		t.Fatalf("unexpected expiry result: %+v", result)
	}
	row = f.readDelivery(t, retryCase.id)
	if row.state != "expired" || row.dispatchSeq != 1 {
		t.Fatalf("expiry allowed a retry generation: %+v", row)
	}
	if rows := f.outboxRows(t, retryCase.id); len(rows) != 0 {
		t.Fatalf("expired retry created %d outbox rows", len(rows))
	}
}

func TestCompletionFencingIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	cases := []struct {
		name   string
		tamper func(*Claim)
	}{
		{"wrong token", func(c *Claim) { c.LeaseToken = testUUID(t) }},
		{"wrong generation", func(c *Claim) { c.DispatchSeq++ }},
		{"wrong organization", func(c *Claim) { c.OrganizationID = testUUID(t) }},
		{"wrong event", func(c *Claim) { c.EventID = testUUID(t) }},
		{"missing delivery", func(c *Claim) { c.DeliveryID = testUUID(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
			claim := acquire(t, store, time.Minute, 3)

			tampered := *claim
			tc.tamper(&tampered)

			result, err := store.Complete(context.Background(), tampered, successOutcome(), RetryDecision{})
			if err != nil {
				t.Fatalf("Complete() error: %v", err)
			}
			if !result.Fenced || result.Applied {
				t.Fatalf("tampered completion was not fenced: %+v", result)
			}

			row := f.readDelivery(t, d.id)
			if row.state != "pending" || row.attemptCount != 0 || row.dispatchSeq != 1 {
				t.Fatalf("fenced completion mutated the delivery: %+v", row)
			}
			if row.leaseToken != claim.LeaseToken {
				t.Fatalf("fenced completion changed the lease: %+v", row)
			}
			if n := f.attemptCount(t, d.id); n != 0 {
				t.Fatalf("fenced completion wrote %d attempt rows", n)
			}
			if rows := f.outboxRows(t, d.id); len(rows) != 0 {
				t.Fatalf("fenced completion wrote %d outbox rows", len(rows))
			}
		})
	}
}

func TestCompletionAfterTerminalStateIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	if result, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{}); err != nil {
		t.Fatalf("Complete() error: %v", err)
	} else if !result.Applied {
		t.Fatalf("first completion was not applied: %+v", result)
	}

	result, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("second Complete() error: %v", err)
	}
	if !result.Fenced || result.Applied {
		t.Fatalf("terminal delivery accepted a second completion: %+v", result)
	}
	if n := f.attemptCount(t, d.id); n != 1 {
		t.Fatalf("second completion wrote attempt rows: %d", n)
	}
	if row := f.readDelivery(t, d.id); row.state != "succeeded" || row.attemptCount != 1 {
		t.Fatalf("terminal delivery changed: %+v", row)
	}
}

func TestConcurrentCompletionIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	type completion struct {
		result CompletionResult
		err    error
	}
	results := make(chan completion, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			result, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{})
			results <- completion{result: result, err: err}
		}()
	}
	wg.Wait()
	close(results)

	applied, fenced := 0, 0
	for completion := range results {
		if completion.err != nil {
			t.Fatalf("concurrent Complete() error: %v", completion.err)
		}
		switch {
		case completion.result.Applied:
			applied++
		case completion.result.Fenced:
			fenced++
		default:
			t.Fatalf("unexpected concurrent result: %+v", completion.result)
		}
	}
	if applied != 1 || fenced != 1 {
		t.Fatalf("applied=%d fenced=%d, want exactly one each", applied, fenced)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "succeeded" || row.attemptCount != 1 {
		t.Fatalf("concurrent completion corrupted the delivery: %+v", row)
	}
	if n := f.attemptCount(t, d.id); n != 1 {
		t.Fatalf("concurrent completion wrote %d attempt rows", n)
	}
}

func TestStaleWorkerAfterReplacementIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})

	staleClaim := acquire(t, store, 100*time.Millisecond, 3)
	time.Sleep(300 * time.Millisecond)

	replacement := acquire(t, store, time.Minute, 3)
	if replacement.DeliveryID != d.id || replacement.DispatchSeq != staleClaim.DispatchSeq {
		t.Fatalf("replacement claim = %+v", replacement)
	}
	if replacement.LeaseToken == staleClaim.LeaseToken {
		t.Fatal("replacement reused the stale fencing token")
	}

	result, err := store.Complete(context.Background(), *staleClaim, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("stale Complete() error: %v", err)
	}
	if !result.Fenced || result.Applied {
		t.Fatalf("stale worker was not fenced: %+v", result)
	}
	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.attemptCount != 0 || row.leaseToken != replacement.LeaseToken {
		t.Fatalf("stale completion mutated the delivery: %+v", row)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("stale completion wrote %d attempt rows", n)
	}

	result, err = store.Complete(context.Background(), *replacement, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("replacement Complete() error: %v", err)
	}
	if !result.Applied || result.AttemptNumber != 1 {
		t.Fatalf("replacement completion was not applied: %+v", result)
	}
	if n := f.attemptCount(t, d.id); n != 1 {
		t.Fatalf("replacement completion attempt rows = %d", n)
	}
}

func TestLateLeaseCompletionIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, 100*time.Millisecond, 3)

	time.Sleep(300 * time.Millisecond)

	result, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Applied || result.Fenced {
		t.Fatalf("matching token/generation completion was rejected after lease expiry: %+v", result)
	}
	if row := f.readDelivery(t, d.id); row.state != "succeeded" || row.attemptCount != 1 {
		t.Fatalf("late-lease completion did not persist: %+v", row)
	}
}

func TestCompletionAtomicRollbackIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	// Occupy the retry generation so the outbox insert fails after the attempt
	// row and delivery update have already been written in the transaction.
	f.insertOutbox(t, d.id, 2, "delivery.retry", time.Now().Add(time.Minute))

	_, err := store.Complete(context.Background(), *claim, retryableOutcome(),
		RetryDecision{Retry: true, Backoff: 30 * time.Second})
	if err == nil {
		t.Fatal("Complete() succeeded despite a conflicting retry outbox row")
	}

	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.attemptCount != 0 || row.dispatchSeq != 1 {
		t.Fatalf("failed completion was not rolled back: %+v", row)
	}
	if row.leaseToken != claim.LeaseToken {
		t.Fatalf("failed completion cleared the lease: %+v", row)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("failed completion left %d attempt rows", n)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 1 || rows[0].sequence != 2 {
		t.Fatalf("failed completion changed outbox rows: %+v", rows)
	}
}

func TestCrashWindowReacquisitionIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})

	// The worker "crashes" after HTTP but before committing: no attempt row and
	// no delivery mutation exist, and only the lease remains.
	lostClaim := acquire(t, store, 100*time.Millisecond, 3)
	time.Sleep(300 * time.Millisecond)

	recovered := acquire(t, store, time.Minute, 3)
	if recovered.DeliveryID != d.id || recovered.DispatchSeq != lostClaim.DispatchSeq {
		t.Fatalf("crash recovery claim = %+v", recovered)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("crash window left %d attempt rows", n)
	}
	if row := f.readDelivery(t, d.id); row.state != "pending" || row.attemptCount != 0 {
		t.Fatalf("crash window mutated the delivery: %+v", row)
	}

	result, err := store.Complete(context.Background(), *lostClaim, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("lost worker Complete() error: %v", err)
	}
	if !result.Fenced {
		t.Fatalf("lost worker was not fenced: %+v", result)
	}

	result, err = store.Complete(context.Background(), *recovered, successOutcome(), RetryDecision{})
	if err != nil {
		t.Fatalf("recovered worker Complete() error: %v", err)
	}
	if !result.Applied || result.AttemptNumber != 1 {
		t.Fatalf("recovered worker completion = %+v", result)
	}
	if n := f.attemptCount(t, d.id); n != 1 {
		t.Fatalf("recovered attempt rows = %d, want 1", n)
	}
}

func TestDecryptFailurePacedRetryIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	pacing := 30 * time.Second
	result, err := store.RecordDecryptFailure(context.Background(), *claim, DecryptFailureOptions{
		Pacing: pacing,
		Error:  "decrypt: unable to open secret envelope Authorization: Bearer super-secret-token",
	})
	if err != nil {
		t.Fatalf("RecordDecryptFailure() error: %v", err)
	}
	if !result.Applied || result.Fenced || result.Expired || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}
	if result.RetrySequence != 2 {
		t.Fatalf("retry generation = %d, want 2", result.RetrySequence)
	}
	requireTimeNear(t, "disposition gate", result.NextAttemptAt, time.Now().Add(pacing), 5*time.Second)

	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.attemptCount != 0 || row.dispatchSeq != 2 {
		t.Fatalf("decrypt disposition changed delivery accounting: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("decrypt disposition did not clear the lease: %+v", row)
	}
	if row.lastAttemptAt.Valid {
		t.Fatalf("decrypt disposition set last_attempt_at: %+v", row)
	}
	if !row.lastError.Valid || !strings.Contains(row.lastError.String, "decrypt") {
		t.Fatalf("decrypt disposition last_error = %+v", row.lastError)
	}
	if strings.Contains(row.lastError.String, "super-secret-token") {
		t.Fatalf("persisted last_error leaked a credential: %q", row.lastError.String)
	}
	requireTimestamptzNear(t, "delivery gate", row.nextAttemptAt, result.NextAttemptAt, 2*time.Millisecond)

	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("decrypt failure wrote %d attempt rows", n)
	}

	rows := f.outboxRows(t, d.id)
	if len(rows) != 1 {
		t.Fatalf("decrypt disposition outbox rows = %d, want 1", len(rows))
	}
	outbox := rows[0]
	if outbox.sequence != 2 || outbox.kind != "delivery.retry" || outbox.status != "pending" || outbox.attempts != 0 {
		t.Fatalf("decrypt retry outbox = %+v", outbox)
	}
	requireTimeNear(t, "outbox gate", outbox.availableAt, result.NextAttemptAt, 2*time.Millisecond)
}

func TestDecryptFailureNearExpiryIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	// The paced retry would land after the event horizon: no dispatch may be
	// created, and the delivery must stay live until the horizon.
	f.setEventExpiresAt(t, d.eventID, time.Now().Add(2*time.Second))
	eventExpiry := f.eventExpiresAt(t, d.eventID)

	result, err := store.RecordDecryptFailure(context.Background(), *claim, DecryptFailureOptions{
		Pacing: 10 * time.Second,
		Error:  "decrypt: key unavailable",
	})
	if err != nil {
		t.Fatalf("RecordDecryptFailure() error: %v", err)
	}
	if !result.Applied || result.Expired || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}
	if result.RetrySequence != 1 {
		t.Fatalf("retry generation = %d, want unchanged 1", result.RetrySequence)
	}
	requireTimeNear(t, "disposition gate", result.NextAttemptAt, eventExpiry, 2*time.Millisecond)

	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.dispatchSeq != 1 || row.attemptCount != 0 {
		t.Fatalf("near-expiry disposition changed delivery: %+v", row)
	}
	requireTimestamptzNear(t, "delivery gate", row.nextAttemptAt, eventExpiry, 2*time.Millisecond)
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("near-expiry disposition did not clear the lease: %+v", row)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("near-expiry disposition created %d outbox rows", len(rows))
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("near-expiry disposition wrote %d attempt rows", n)
	}
}

func TestDecryptFailureExpiredEventIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	f.expireEvent(t, d.eventID)

	result, err := store.RecordDecryptFailure(context.Background(), *claim, DecryptFailureOptions{
		Pacing: 30 * time.Second,
		Error:  "decrypt: unable to open secret envelope",
	})
	if err != nil {
		t.Fatalf("RecordDecryptFailure() error: %v", err)
	}
	if !result.Applied || !result.Expired || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "expired" || !row.completedAt.Valid || row.nextAttemptAt.Valid {
		t.Fatalf("decrypt expiry did not terminate the delivery: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("decrypt expiry did not clear the lease: %+v", row)
	}
	if !row.lastError.Valid || !strings.Contains(row.lastError.String, "decrypt") {
		t.Fatalf("decrypt expiry last_error = %+v", row.lastError)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("decrypt expiry wrote %d attempt rows", n)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("decrypt expiry created %d outbox rows", len(rows))
	}
}

func TestDecryptFailureExpiredLeaseIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	run := func(t *testing.T, eventExpiresAt time.Time) {
		t.Helper()
		now := time.Now()
		token := testUUID(t)
		leaseExpiry := now.Add(-time.Minute)
		d := f.seedDelivery(t, deliverySeed{
			nextAttemptAt:  now.Add(-time.Minute),
			leaseToken:     token,
			leaseExpiresAt: leaseExpiry,
			eventReceived:  now.Add(-2 * time.Hour),
			eventExpiresAt: eventExpiresAt,
		})
		claim := Claim{
			OrganizationID: f.orgID,
			DeliveryID:     d.id,
			EventID:        d.eventID,
			EndpointID:     f.endpointID,
			DispatchSeq:    1,
			LeaseToken:     token,
			LeaseExpiresAt: leaseExpiry,
		}

		result, err := store.RecordDecryptFailure(context.Background(), claim, DecryptFailureOptions{
			Pacing: 30 * time.Second,
			Error:  "decrypt: unable to open secret envelope",
		})
		if err != nil {
			t.Fatalf("RecordDecryptFailure() error: %v", err)
		}
		if !result.Applied || !result.Expired || result.Fenced || !result.AckEligible {
			t.Fatalf("unexpected disposition result: %+v", result)
		}

		row := f.readDelivery(t, d.id)
		if row.state != "expired" || !row.completedAt.Valid || row.nextAttemptAt.Valid {
			t.Fatalf("expired lease did not terminalize the delivery: %+v", row)
		}
		if row.leaseToken != "" || row.leaseExpiresAt.Valid {
			t.Fatalf("expired lease disposition did not clear the lease: %+v", row)
		}
		if row.dispatchSeq != 1 || row.attemptCount != 0 {
			t.Fatalf("expired lease disposition changed accounting: %+v", row)
		}
		if !row.lastError.Valid || !strings.Contains(row.lastError.String, "decrypt") {
			t.Fatalf("expired lease disposition last_error = %+v", row.lastError)
		}
		if n := f.attemptCount(t, d.id); n != 0 {
			t.Fatalf("expired lease disposition wrote %d attempt rows", n)
		}
		if rows := f.outboxRows(t, d.id); len(rows) != 0 {
			t.Fatalf("expired lease disposition wrote %d outbox rows", len(rows))
		}

		// A repeated disposition for the now-terminal delivery is safely fenced
		// and writes nothing further.
		again, err := store.RecordDecryptFailure(context.Background(), claim, DecryptFailureOptions{
			Pacing: 30 * time.Second,
			Error:  "decrypt: unable to open secret envelope",
		})
		if err != nil {
			t.Fatalf("repeated RecordDecryptFailure() error: %v", err)
		}
		if !again.Fenced || again.Applied || !again.AckEligible {
			t.Fatalf("repeated disposition result = %+v", again)
		}
		if row := f.readDelivery(t, d.id); row.state != "expired" || row.dispatchSeq != 1 || row.attemptCount != 0 {
			t.Fatalf("repeated disposition mutated the delivery: %+v", row)
		}
		if n := f.attemptCount(t, d.id); n != 0 {
			t.Fatalf("repeated disposition wrote %d attempt rows", n)
		}
		if rows := f.outboxRows(t, d.id); len(rows) != 0 {
			t.Fatalf("repeated disposition wrote %d outbox rows", len(rows))
		}
	}

	t.Run("live event", func(t *testing.T) {
		run(t, time.Now().Add(time.Hour))
	})
	t.Run("event already expired", func(t *testing.T) {
		run(t, time.Now().Add(-time.Hour))
	})
}

func TestDecryptFailureFencedIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	cases := []struct {
		name   string
		tamper func(*Claim)
	}{
		{"wrong token", func(c *Claim) { c.LeaseToken = testUUID(t) }},
		{"wrong generation", func(c *Claim) { c.DispatchSeq++ }},
		{"wrong organization", func(c *Claim) { c.OrganizationID = testUUID(t) }},
		{"wrong event", func(c *Claim) { c.EventID = testUUID(t) }},
		{"missing delivery", func(c *Claim) { c.DeliveryID = testUUID(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
			claim := acquire(t, store, time.Minute, 3)

			tampered := *claim
			tc.tamper(&tampered)

			result, err := store.RecordDecryptFailure(context.Background(), tampered, DecryptFailureOptions{
				Pacing: 30 * time.Second,
				Error:  "decrypt: unable to open secret envelope",
			})
			if err != nil {
				t.Fatalf("RecordDecryptFailure() error: %v", err)
			}
			if !result.Fenced || result.Applied || !result.AckEligible {
				t.Fatalf("fenced disposition result = %+v", result)
			}

			row := f.readDelivery(t, d.id)
			if row.state != "pending" || row.dispatchSeq != 1 || row.attemptCount != 0 {
				t.Fatalf("fenced disposition mutated the delivery: %+v", row)
			}
			if row.leaseToken != claim.LeaseToken {
				t.Fatalf("fenced disposition changed the lease: %+v", row)
			}
			if row.lastError.Valid {
				t.Fatalf("fenced disposition wrote last_error = %q", row.lastError.String)
			}
			if rows := f.outboxRows(t, d.id); len(rows) != 0 {
				t.Fatalf("fenced disposition wrote %d outbox rows", len(rows))
			}
			if n := f.attemptCount(t, d.id); n != 0 {
				t.Fatalf("fenced disposition wrote %d attempt rows", n)
			}
		})
	}
}

func TestDecryptFailureBuilderFailureIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, func(opts *Options) {
		opts.BuildRetryMessage = func(RetryMessageInput) (Message, error) {
			return Message{}, errors.New("retry message construction failed")
		}
	})
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)
	original := f.readDelivery(t, d.id)

	result, err := store.RecordDecryptFailure(context.Background(), *claim, DecryptFailureOptions{
		Pacing: 30 * time.Second,
		Error:  "decrypt: unable to open secret envelope",
	})
	if err == nil {
		t.Fatalf("RecordDecryptFailure() succeeded despite a failing message builder: %+v", result)
	}
	if result.AckEligible || result.Applied {
		t.Fatalf("failed disposition was ACK-eligible: %+v", result)
	}

	row := f.readDelivery(t, d.id)
	if row.state != original.state || row.dispatchSeq != original.dispatchSeq || row.attemptCount != original.attemptCount {
		t.Fatalf("failed disposition was not rolled back: %+v", row)
	}
	if row.leaseToken != claim.LeaseToken {
		t.Fatalf("failed disposition cleared the lease: %+v", row)
	}
	if row.lastError.Valid {
		t.Fatalf("failed disposition wrote last_error = %q", row.lastError.String)
	}
	requireTimestamptzNear(t, "delivery gate", row.nextAttemptAt, original.nextAttemptAt.Time, 2*time.Millisecond)
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("failed disposition wrote %d outbox rows", len(rows))
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("failed disposition wrote %d attempt rows", n)
	}
}
