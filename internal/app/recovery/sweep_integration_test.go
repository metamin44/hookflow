//go:build integration

package recovery

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
)

// Recovery Sweep is intentionally global. These tests therefore require an
// otherwise empty delivery/outbox population and skip rather than mutate data
// that does not belong to the test. Run integration packages with -p 1.
type recoveryFixture struct {
	pool       *pgxpool.Pool
	orgID      string
	endpointID string
	seedCount  int
}

func newRecoveryFixture(t *testing.T) *recoveryFixture {
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
		t.Skipf("recovery integration tests require an isolated database; found %d deliveries and %d outbox rows", deliveries, outbox)
	}

	f := &recoveryFixture{
		pool:       pool.Raw(),
		orgID:      testUUID(t),
		endpointID: testUUID(t),
	}

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		f.orgID, "recovery-test-"+f.orgID,
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

type seedSpec struct {
	state          string
	dispatchSeq    int
	attemptCount   int
	nextAttemptAt  time.Time
	leaseToken     string
	leaseExpiresAt time.Time
	createdAt      time.Time
	eventExpiresAt time.Time
	eventReceived  time.Time
}

type seededDelivery struct {
	id      string
	eventID string
}

func (f *recoveryFixture) seedDelivery(t *testing.T, spec seedSpec) seededDelivery {
	t.Helper()

	now := time.Now()
	f.seedCount++
	if spec.state == "" {
		spec.state = "pending"
	}
	if spec.dispatchSeq == 0 {
		spec.dispatchSeq = 1
	}
	if spec.createdAt.IsZero() {
		spec.createdAt = now.Add(-10 * time.Minute).Add(time.Duration(f.seedCount) * time.Millisecond)
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

	var nextAttemptAt any
	if live {
		nextAttemptAt = spec.nextAttemptAt
	}
	var completedAt any
	if !live {
		completedAt = now
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
		 VALUES ($1::uuid, $2::uuid, 'test.recovery', '{}'::jsonb, 1, $3::timestamptz, $4::timestamptz)`,
		eventID, f.orgID, spec.eventReceived, spec.eventExpiresAt,
	); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO deliveries
		     (id, organization_id, event_id, endpoint_id, url, http_headers, secret_encrypted,
		      state, dispatch_seq, attempt_count, next_attempt_at, created_at, updated_at,
		      completed_at, dispatch_lease_token, dispatch_lease_expires_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'https://example.test/hook', '{}'::jsonb,
		         'test-ciphertext', $5, $6, $7, $8::timestamptz, $9::timestamptz, $9::timestamptz,
		         $10::timestamptz, $11::uuid, $12::timestamptz)`,
		deliveryID, f.orgID, eventID, f.endpointID, spec.state, spec.dispatchSeq,
		spec.attemptCount, nextAttemptAt, spec.createdAt, completedAt, leaseToken, leaseExpiresAt,
	); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	return seededDelivery{id: deliveryID, eventID: eventID}
}

type outboxSpec struct {
	sequence    int
	status      string
	attempts    int
	availableAt time.Time
	publishedAt time.Time
	createdAt   time.Time
	lastError   string
	payload     string
}

func (f *recoveryFixture) insertOutbox(t *testing.T, deliveryID string, spec outboxSpec) string {
	t.Helper()

	now := time.Now()
	if spec.sequence == 0 {
		spec.sequence = 1
	}
	if spec.status == "" {
		spec.status = "pending"
	}
	if spec.availableAt.IsZero() {
		spec.availableAt = now.Add(-time.Minute)
	}
	if spec.createdAt.IsZero() {
		spec.createdAt = spec.availableAt
	}
	if spec.payload == "" {
		spec.payload = `{"source":"fixture"}`
	}
	kind := "delivery.dispatch"
	if spec.sequence > 1 {
		kind = "delivery.retry"
	}

	var publishedAt any
	if spec.status == "published" {
		if spec.publishedAt.IsZero() {
			spec.publishedAt = now.Add(-2 * time.Minute)
		}
		publishedAt = spec.publishedAt
	}
	var lastError any
	if spec.lastError != "" {
		lastError = spec.lastError
	}
	var lockedBy any
	var lockedAt any
	var leaseToken any
	if spec.status == "publishing" {
		lockedBy = "publisher-test"
		lockedAt = now.Add(-time.Minute)
		leaseToken = testUUID(t)
	}

	id := testUUID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := f.pool.Exec(ctx,
		`INSERT INTO outbox_messages
		     (id, organization_id, delivery_id, sequence, kind, topic, payload, status, attempts,
		      available_at, locked_at, locked_by, lease_token, published_at, last_error, created_at, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5, 'test.recovery', $6::jsonb, $7, $8,
		         $9::timestamptz, $10::timestamptz, $11, $12::uuid, $13::timestamptz, $14,
		         $15::timestamptz, $15::timestamptz)`,
		id, f.orgID, deliveryID, spec.sequence, kind, spec.payload, spec.status, spec.attempts,
		spec.availableAt, lockedAt, lockedBy, leaseToken, publishedAt, lastError, spec.createdAt,
	)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return id
}

type deliveryRecord struct {
	state          string
	dispatchSeq    int
	attemptCount   int
	nextAttemptAt  pgtype.Timestamptz
	completedAt    pgtype.Timestamptz
	leaseToken     string
	leaseExpiresAt pgtype.Timestamptz
}

func (f *recoveryFixture) readDelivery(t *testing.T, id string) deliveryRecord {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var row deliveryRecord
	err := f.pool.QueryRow(ctx,
		`SELECT state, dispatch_seq, attempt_count, next_attempt_at, completed_at,
		        COALESCE(dispatch_lease_token::text, ''), dispatch_lease_expires_at
		   FROM deliveries WHERE id = $1::uuid`,
		id,
	).Scan(&row.state, &row.dispatchSeq, &row.attemptCount, &row.nextAttemptAt,
		&row.completedAt, &row.leaseToken, &row.leaseExpiresAt)
	if err != nil {
		t.Fatalf("read delivery: %v", err)
	}
	return row
}

type outboxRecord struct {
	status      string
	attempts    int
	availableAt time.Time
	leaseToken  string
	publishedAt pgtype.Timestamptz
	lastError   string
	payload     string
}

func (f *recoveryFixture) readOutbox(t *testing.T, id string) outboxRecord {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var row outboxRecord
	err := f.pool.QueryRow(ctx,
		`SELECT status, attempts, available_at, COALESCE(lease_token::text, ''),
		        published_at, COALESCE(last_error, ''), payload::text
		   FROM outbox_messages WHERE id = $1::uuid`,
		id,
	).Scan(&row.status, &row.attempts, &row.availableAt, &row.leaseToken,
		&row.publishedAt, &row.lastError, &row.payload)
	if err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	return row
}

func (f *recoveryFixture) outboxFor(t *testing.T, deliveryID string, sequence int) (outboxRecord, bool) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var row outboxRecord
	err := f.pool.QueryRow(ctx,
		`SELECT status, attempts, available_at, COALESCE(lease_token::text, ''),
		        published_at, COALESCE(last_error, ''), payload::text
		   FROM outbox_messages WHERE delivery_id = $1::uuid AND sequence = $2`,
		deliveryID, sequence,
	).Scan(&row.status, &row.attempts, &row.availableAt, &row.leaseToken,
		&row.publishedAt, &row.lastError, &row.payload)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return outboxRecord{}, false
		}
		t.Fatalf("read outbox by sequence: %v", err)
	}
	return row, true
}

func (f *recoveryFixture) outboxCount(t *testing.T, deliveryID string) int {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var count int
	if err := f.pool.QueryRow(ctx,
		`SELECT count(*) FROM outbox_messages WHERE delivery_id = $1::uuid`, deliveryID,
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows: %v", err)
	}
	return count
}

func testBuilder() MessageBuilder {
	return func(d Delivery) (Message, error) {
		id, err := randomUUID()
		if err != nil {
			return Message{}, err
		}
		kind := "delivery.dispatch"
		if d.DispatchSeq > 1 {
			kind = "delivery.retry"
		}
		return Message{
			ID:       id,
			Sequence: d.DispatchSeq,
			Kind:     kind,
			Topic:    "test.recovery",
			Payload:  fmt.Sprintf(`{"delivery_id":%q,"sequence":%d}`, d.DeliveryID, d.DispatchSeq),
			Headers:  fmt.Sprintf(`{"x-generation":%d}`, d.DispatchSeq),
		}, nil
	}
}

func newTestSweep(t *testing.T, f *recoveryFixture, mutate func(*Options)) *Sweep {
	t.Helper()

	opts := Options{
		Budget: Budget{
			Total:  8 * time.Second,
			Expire: PassBudget{MaxCandidates: 20, MaxDuration: 2 * time.Second},
			Repair: PassBudget{MaxCandidates: 20, MaxDuration: 2 * time.Second},
			ReArm:  PassBudget{MaxCandidates: 20, MaxDuration: 2 * time.Second},
			Stale:  PassBudget{MaxCandidates: 20, MaxDuration: 2 * time.Second},
		},
		StalePublishedAfter: time.Minute,
		BuildMessage:        testBuilder(),
		Logger:              slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	if mutate != nil {
		mutate(&opts)
	}
	sweep, err := NewSweep(f.pool, opts)
	if err != nil {
		t.Fatalf("NewSweep() error: %v", err)
	}
	return sweep
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

func TestExpiryPrecedenceIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)

	expired := f.seedDelivery(t, seedSpec{
		eventReceived:  time.Now().Add(-2 * time.Hour),
		eventExpiresAt: time.Now().Add(-time.Hour),
	})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Expire.Applied != 1 || report.Repair.Applied != 0 {
		t.Fatalf("unexpected pass accounting: expire=%+v repair=%+v", report.Expire, report.Repair)
	}

	row := f.readDelivery(t, expired.id)
	if row.state != "expired" || !row.completedAt.Valid || row.nextAttemptAt.Valid {
		t.Fatalf("expiry did not win: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("expiry retained a worker lease: %+v", row)
	}
	if n := f.outboxCount(t, expired.id); n != 0 {
		t.Fatalf("expiry precedence allowed %d repair rows", n)
	}
}

// TestExpiryPassWinsOverValidWorkerLeaseIntegration records the current
// cross-component behavior: the Recovery expiry pass does not treat a live
// worker lease as a reason to defer, so it terminalizes the delivery and a later
// completion from the still-token-valid worker is fenced.
func TestExpiryPassWinsOverValidWorkerLeaseIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()

	token := testUUID(t)
	leaseExpiry := now.Add(5 * time.Minute)
	d := f.seedDelivery(t, seedSpec{
		leaseToken:     token,
		leaseExpiresAt: leaseExpiry,
		eventReceived:  now.Add(-2 * time.Hour),
		eventExpiresAt: now.Add(-time.Hour),
	})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Expire.Applied != 1 {
		t.Fatalf("expire accounting = %+v, want one applied", report.Expire)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "expired" || !row.completedAt.Valid || row.nextAttemptAt.Valid {
		t.Fatalf("expiry did not terminalize a validly leased delivery: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("expiry retained the worker lease: %+v", row)
	}
	if n := f.outboxCount(t, d.id); n != 0 {
		t.Fatalf("expiry created %d outbox rows", n)
	}

	worker, err := delivery.NewStore(f.pool, delivery.Options{
		NewID: randomUUID,
		BuildRetryMessage: func(delivery.RetryMessageInput) (delivery.Message, error) {
			return delivery.Message{}, errors.New("retry must not be built after expiry")
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("delivery.NewStore() error: %v", err)
	}

	status := 200
	result, err := worker.Complete(context.Background(), delivery.Claim{
		OrganizationID: f.orgID,
		DeliveryID:     d.id,
		EventID:        d.eventID,
		EndpointID:     f.endpointID,
		DispatchSeq:    1,
		LeaseToken:     token,
		LeaseExpiresAt: leaseExpiry,
	}, delivery.Outcome{
		Kind:           delivery.OutcomeSuccess,
		RequestMethod:  "POST",
		ResponseStatus: &status,
		Duration:       time.Millisecond,
		StartedAt:      now.Add(-time.Second),
		FinishedAt:     now,
	}, delivery.RetryDecision{})
	if err != nil {
		t.Fatalf("Complete() error: %v", err)
	}
	if !result.Fenced || result.Applied {
		t.Fatalf("completion after expiry-pass terminalization = %+v", result)
	}
	if row := f.readDelivery(t, d.id); row.state != "expired" || row.attemptCount != 0 {
		t.Fatalf("fenced completion mutated the expired delivery: %+v", row)
	}
	if n := f.outboxCount(t, d.id); n != 0 {
		t.Fatalf("fenced completion created %d outbox rows", n)
	}
}

func TestRepairMissingOutboxIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	gate := time.Now().Add(-time.Minute)
	d := f.seedDelivery(t, seedSpec{nextAttemptAt: gate})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Repair.Applied != 1 {
		t.Fatalf("repair accounting = %+v", report.Repair)
	}

	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.dispatchSeq != 1 {
		t.Fatalf("repair mutated delivery: %+v", row)
	}
	outbox, ok := f.outboxFor(t, d.id, 1)
	if !ok {
		t.Fatal("repair did not insert the missing outbox row")
	}
	if outbox.status != "pending" || outbox.attempts != 0 {
		t.Fatalf("repaired outbox = %+v", outbox)
	}
	if outbox.availableAt.Before(gate) {
		t.Fatalf("repaired gate %s precedes delivery gate %s", outbox.availableAt, gate)
	}
	if outbox.payload == "" || outbox.leaseToken != "" {
		t.Fatalf("repaired outbox payload/lease = %+v", outbox)
	}

	report, err = sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if report.Repair.Applied != 0 {
		t.Fatalf("duplicate execution repaired again: %+v", report.Repair)
	}
	if n := f.outboxCount(t, d.id); n != 1 {
		t.Fatalf("duplicate execution created %d outbox rows", n)
	}
}

func TestRepairSkipsIneligibleDeliveriesIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()

	future := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(time.Hour)})
	leased := f.seedDelivery(t, seedSpec{
		nextAttemptAt:  now.Add(-time.Minute),
		leaseToken:     testUUID(t),
		leaseExpiresAt: now.Add(time.Hour),
	})
	existing := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	f.insertOutbox(t, existing.id, outboxSpec{sequence: 1, status: "pending", payload: `{"source":"existing"}`})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Repair.Applied != 0 {
		t.Fatalf("repair applied to ineligible deliveries: %+v", report.Repair)
	}
	for _, d := range []seededDelivery{future, leased} {
		if n := f.outboxCount(t, d.id); n != 0 {
			t.Fatalf("ineligible delivery %s received %d outbox rows", d.id, n)
		}
	}
	if n := f.outboxCount(t, existing.id); n != 1 {
		t.Fatalf("existing outbox row count = %d", n)
	}
}

func TestRearmFailedGenerationIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()

	past := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	pastOutbox := f.insertOutbox(t, past.id, outboxSpec{
		sequence:    1,
		status:      "failed",
		attempts:    4,
		availableAt: now.Add(-time.Minute),
		lastError:   "retained failure",
	})
	futureGate := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	futureOutbox := f.insertOutbox(t, futureGate.id, outboxSpec{
		sequence:    1,
		status:      "failed",
		attempts:    4,
		availableAt: now.Add(30 * time.Minute),
	})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.ReArm.Applied != 2 {
		t.Fatalf("re-arm accounting = %+v", report.ReArm)
	}

	rearmed := f.readOutbox(t, pastOutbox)
	if rearmed.status != "pending" || rearmed.attempts != 0 {
		t.Fatalf("re-armed outbox = %+v", rearmed)
	}
	if rearmed.lastError != "retained failure" {
		t.Fatalf("re-arm cleared last_error: %q", rearmed.lastError)
	}
	if rearmed.availableAt.Before(now.Add(-time.Second)) {
		t.Fatalf("re-armed gate moved backwards: %s", rearmed.availableAt)
	}

	held := f.readOutbox(t, futureOutbox)
	if held.status != "pending" || held.attempts != 0 {
		t.Fatalf("future-gated re-arm = %+v", held)
	}
	if held.availableAt.Before(now.Add(29 * time.Minute)) {
		t.Fatalf("re-arm shortened the publisher gate: %s", held.availableAt)
	}

	report, err = sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if report.ReArm.Applied != 0 {
		t.Fatalf("already re-armed rows were re-armed again: %+v", report.ReArm)
	}
	if n := f.outboxCount(t, past.id); n != 1 {
		t.Fatalf("re-arm created extra outbox rows: %d", n)
	}
}

func TestStaleExpiredLeaseRedispatchIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()
	d := f.seedDelivery(t, seedSpec{
		nextAttemptAt:  now.Add(-time.Minute),
		leaseToken:     testUUID(t),
		leaseExpiresAt: now.Add(-time.Minute),
	})
	old := f.insertOutbox(t, d.id, outboxSpec{
		sequence:    1,
		status:      "published",
		publishedAt: now.Add(-2 * time.Minute),
	})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Stale.Applied != 1 {
		t.Fatalf("stale accounting = %+v", report.Stale)
	}

	row := f.readDelivery(t, d.id)
	if row.dispatchSeq != 2 || row.state != "pending" || row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("stale redispatch did not advance/clear delivery: %+v", row)
	}
	if oldRow := f.readOutbox(t, old); oldRow.status != "published" {
		t.Fatalf("stale redispatch mutated the old generation: %+v", oldRow)
	}
	newRow, ok := f.outboxFor(t, d.id, 2)
	if !ok {
		t.Fatal("stale redispatch did not insert generation 2")
	}
	if newRow.status != "pending" || newRow.attempts != 0 || newRow.leaseToken != "" {
		t.Fatalf("new generation = %+v", newRow)
	}

	report, err = sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("second Run() error: %v", err)
	}
	if report.Stale.Applied != 0 {
		t.Fatalf("duplicate stale redispatch: %+v", report.Stale)
	}
	if n := f.outboxCount(t, d.id); n != 2 {
		t.Fatalf("duplicate stale redispatch outbox count = %d", n)
	}
}

func TestStalePublishedGenerationRedispatchIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()

	oldPublished := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	f.insertOutbox(t, oldPublished.id, outboxSpec{
		sequence:    1,
		status:      "published",
		publishedAt: now.Add(-2 * time.Minute),
	})

	recentPublished := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	f.insertOutbox(t, recentPublished.id, outboxSpec{
		sequence:    1,
		status:      "published",
		publishedAt: now.Add(-time.Second),
	})

	pending := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	f.insertOutbox(t, pending.id, outboxSpec{sequence: 1, status: "pending"})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Stale.Applied != 1 {
		t.Fatalf("stale accounting = %+v", report.Stale)
	}

	if row := f.readDelivery(t, oldPublished.id); row.dispatchSeq != 2 {
		t.Fatalf("old published generation was not redispatched: %+v", row)
	}
	if _, ok := f.outboxFor(t, oldPublished.id, 2); !ok {
		t.Fatal("old published generation has no generation 2")
	}
	if row := f.readDelivery(t, recentPublished.id); row.dispatchSeq != 1 {
		t.Fatalf("recent published generation was redispatched: %+v", row)
	}
	if row := f.readDelivery(t, pending.id); row.dispatchSeq != 1 {
		t.Fatalf("pending generation was redispatched: %+v", row)
	}
}

func TestConcurrentSweepWorkersIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()

	const deliveries = 12
	ids := make([]string, 0, deliveries)
	for i := 0; i < deliveries; i++ {
		d := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
		ids = append(ids, d.id)
	}

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := sweep.Run(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent Run() error: %v", err)
		}
	}

	total := 0
	for _, id := range ids {
		n := f.outboxCount(t, id)
		if n != 1 {
			t.Fatalf("delivery %s has %d outbox rows, want 1", id, n)
		}
		total += n
	}
	if total != deliveries {
		t.Fatalf("concurrent sweeps produced %d outbox rows, want %d", total, deliveries)
	}
}

func TestSweepSkipsLockedDeliveryIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()

	locked := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	free := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})

	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT 1 FROM deliveries WHERE id = $1::uuid FOR UPDATE`, locked.id); err != nil {
		t.Fatalf("lock delivery: %v", err)
	}

	if _, err := sweep.Run(ctx); err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if n := f.outboxCount(t, locked.id); n != 0 {
		t.Fatalf("locked delivery was repaired: %d outbox rows", n)
	}
	if n := f.outboxCount(t, free.id); n != 1 {
		t.Fatalf("unlocked delivery was not repaired: %d outbox rows", n)
	}
}

func TestSweepDefersContendedOutboxIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	now := time.Now()
	d := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})
	outboxID := f.insertOutbox(t, d.id, outboxSpec{
		sequence:    1,
		status:      "failed",
		attempts:    4,
		availableAt: now.Add(-time.Minute),
	})

	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `SELECT 1 FROM outbox_messages WHERE id = $1::uuid FOR UPDATE`, outboxID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("lock outbox row: %v", err)
	}

	report, err := sweep.Run(ctx)
	if err != nil {
		_ = tx.Rollback(ctx)
		t.Fatalf("Run() error: %v", err)
	}
	if report.ReArm.Deferred != 1 || report.ReArm.Applied != 0 {
		_ = tx.Rollback(ctx)
		t.Fatalf("contended re-arm accounting = %+v", report.ReArm)
	}
	if row := f.readOutbox(t, outboxID); row.status != "failed" {
		_ = tx.Rollback(ctx)
		t.Fatalf("contended re-arm mutated row: %+v", row)
	}
	if err := tx.Rollback(ctx); err != nil {
		t.Fatalf("rollback lock transaction: %v", err)
	}

	report, err = sweep.Run(ctx)
	if err != nil {
		t.Fatalf("retry Run() error: %v", err)
	}
	if report.ReArm.Applied != 1 {
		t.Fatalf("retry re-arm accounting = %+v", report.ReArm)
	}
	if row := f.readOutbox(t, outboxID); row.status != "pending" || row.attempts != 0 {
		t.Fatalf("retry did not re-arm row: %+v", row)
	}
}

func TestRepairDoesNotOverwriteExistingGenerationIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, nil)
	d := f.seedDelivery(t, seedSpec{nextAttemptAt: time.Now().Add(-time.Minute)})
	existing := f.insertOutbox(t, d.id, outboxSpec{
		sequence:    1,
		status:      "pending",
		availableAt: time.Now().Add(time.Hour),
		payload:     `{"source":"external"}`,
	})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Repair.Applied != 0 {
		t.Fatalf("repair overwrote an existing generation: %+v", report.Repair)
	}
	row := f.readOutbox(t, existing)
	if row.payload != `{"source": "external"}` || row.status != "pending" {
		t.Fatalf("existing generation changed: %+v", row)
	}
	if n := f.outboxCount(t, d.id); n != 1 {
		t.Fatalf("existing generation duplicated: %d", n)
	}
}

func TestPerPassCandidateReservationIntegration(t *testing.T) {
	f := newRecoveryFixture(t)
	sweep := newTestSweep(t, f, func(opts *Options) {
		opts.Budget.Expire.MaxCandidates = 5
		opts.Budget.Repair.MaxCandidates = 1
	})
	now := time.Now()

	expiredIDs := make([]string, 0, 5)
	for i := 0; i < 5; i++ {
		d := f.seedDelivery(t, seedSpec{
			eventReceived:  now.Add(-2 * time.Hour),
			eventExpiresAt: now.Add(-time.Hour),
		})
		expiredIDs = append(expiredIDs, d.id)
	}
	live := f.seedDelivery(t, seedSpec{nextAttemptAt: now.Add(-time.Minute)})

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Run() error: %v", err)
	}
	if report.Expire.Applied != 5 {
		t.Fatalf("expire pass did not use its own allowance: %+v", report.Expire)
	}
	if report.Repair.Applied != 1 {
		t.Fatalf("repair pass lost its reserved allowance: %+v", report.Repair)
	}
	for _, id := range expiredIDs {
		if row := f.readDelivery(t, id); row.state != "expired" {
			t.Fatalf("delivery %s was not expired: %+v", id, row)
		}
	}
	if n := f.outboxCount(t, live.id); n != 1 {
		t.Fatalf("live delivery was not repaired after expire capacity use: %d", n)
	}
}
