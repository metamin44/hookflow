//go:build integration

package outbox

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
)

type publisherFixture struct {
	pool       *pgxpool.Pool
	orgID      string
	eventID    string
	endpointID string
	deliveryID string
}

func newPublisherFixture(t *testing.T) *publisherFixture {
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

	f := &publisherFixture{
		pool:       pool.Raw(),
		orgID:      testUUID(t),
		eventID:    testUUID(t),
		endpointID: testUUID(t),
		deliveryID: testUUID(t),
	}

	if _, err := f.pool.Exec(ctx,
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		f.orgID, "publisher-test-"+f.orgID,
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
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO events (id, organization_id, type, payload, destination_count, expires_at)
		 VALUES ($1::uuid, $2::uuid, 'test.publisher', '{}'::jsonb, 1, now() + interval '1 hour')`,
		f.eventID, f.orgID,
	); err != nil {
		t.Fatalf("insert event: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO deliveries
		     (id, organization_id, event_id, endpoint_id, url, http_headers, secret_encrypted,
		      state, dispatch_seq, attempt_count, next_attempt_at, created_at, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'https://example.test/hook', '{}'::jsonb,
		         'test-ciphertext', 'pending', 1, 0, now() - interval '1 minute',
		         now() - interval '10 minutes', now() - interval '10 minutes')`,
		f.deliveryID, f.orgID, f.eventID, f.endpointID,
	); err != nil {
		t.Fatalf("insert delivery: %v", err)
	}
	return f
}

type outboxRow struct {
	status      string
	attempts    int
	availableAt time.Time
	lockedBy    string
	lockedAt    pgtype.Timestamptz
	leaseToken  string
	publishedAt pgtype.Timestamptz
	lastError   string
}

func (f *publisherFixture) insertOutbox(t *testing.T, sequence int, status string, attempts int, availableAt time.Time, lastError string) string {
	t.Helper()

	id := testUUID(t)
	var lockedBy any
	var lockedAt any
	var token any
	if status == "publishing" {
		lockedBy = "publisher-test"
		lockedAt = time.Now().Add(-time.Hour)
		token = testUUID(t)
	}
	var lastErrorArg any
	if lastError != "" {
		lastErrorArg = lastError
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	_, err := f.pool.Exec(ctx,
		`INSERT INTO outbox_messages
		     (id, organization_id, delivery_id, sequence, kind, topic, payload, status, attempts,
		      available_at, locked_at, locked_by, lease_token, last_error)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'delivery.dispatch', 'test.publisher',
		         '{"delivery_id":"fixture"}'::jsonb, $5, $6, $7, $8, $9, $10::uuid, $11)`,
		id, f.orgID, f.deliveryID, sequence, status, attempts, availableAt, lockedAt, lockedBy, token, lastErrorArg,
	)
	if err != nil {
		t.Fatalf("insert outbox row: %v", err)
	}
	return id
}

func (f *publisherFixture) readOutbox(t *testing.T, id string) outboxRow {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	var row outboxRow
	err := f.pool.QueryRow(ctx,
		`SELECT status, attempts, available_at,
		        COALESCE(locked_by, ''), locked_at, COALESCE(lease_token::text, ''),
		        published_at, COALESCE(last_error, '')
		   FROM outbox_messages WHERE id = $1::uuid`,
		id,
	).Scan(&row.status, &row.attempts, &row.availableAt, &row.lockedBy, &row.lockedAt, &row.leaseToken, &row.publishedAt, &row.lastError)
	if err != nil {
		t.Fatalf("read outbox row: %v", err)
	}
	return row
}

func testUUID(t *testing.T) string {
	t.Helper()

	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatalf("generate test uuid: %v", err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

func TestClaimT1AccountingIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()
	now := time.Now()

	eligible := f.insertOutbox(t, 1, "pending", 0, now.Add(-time.Minute), "retained error")
	finalClaim := f.insertOutbox(t, 2, "pending", 2, now.Add(-time.Minute), "")
	future := f.insertOutbox(t, 3, "pending", 0, now.Add(time.Hour), "")
	failed := f.insertOutbox(t, 4, "failed", 0, now.Add(-time.Minute), "")
	publishing := f.insertOutbox(t, 5, "publishing", 1, now.Add(-time.Minute), "")
	exhausted := f.insertOutbox(t, 6, "pending", 3, now.Add(-time.Minute), "")

	claims, err := store.Claim(ctx, ClaimOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      RandomToken,
	})
	if err != nil {
		t.Fatalf("Claim() error: %v", err)
	}
	if len(claims) != 2 {
		t.Fatalf("Claim() returned %d claims, want 2", len(claims))
	}

	byID := make(map[string]Claim, len(claims))
	for _, c := range claims {
		byID[c.MessageID] = c
		if c.Owner != "publisher-test" || c.MaxClaims != 3 {
			t.Fatalf("unexpected claim metadata: %+v", c)
		}
		if c.ClaimCount < 1 || c.ClaimCount > 3 {
			t.Fatalf("claim count %d outside budget", c.ClaimCount)
		}
		if !validUUID(c.LeaseToken) {
			t.Fatalf("claim token is not a UUID: %q", c.LeaseToken)
		}
	}

	e := f.readOutbox(t, eligible)
	if e.status != "publishing" || e.attempts != 1 || e.leaseToken == "" || e.lockedBy != "publisher-test" {
		t.Fatalf("eligible row after claim = %+v", e)
	}
	if e.lastError != "retained error" {
		t.Fatalf("claim cleared last_error: %q", e.lastError)
	}

	fc := f.readOutbox(t, finalClaim)
	if fc.status != "publishing" || fc.attempts != 3 {
		t.Fatalf("final permitted claim = %+v", fc)
	}

	if row := f.readOutbox(t, future); row.status != "pending" || row.attempts != 0 {
		t.Fatalf("future row changed: %+v", row)
	}
	if row := f.readOutbox(t, failed); row.status != "failed" || row.attempts != 0 {
		t.Fatalf("failed row changed: %+v", row)
	}
	if row := f.readOutbox(t, exhausted); row.status != "pending" || row.attempts != 3 {
		t.Fatalf("exhausted row changed: %+v", row)
	}
	if row := f.readOutbox(t, publishing); row.status != "publishing" || row.attempts != 1 {
		t.Fatalf("publishing row changed: %+v", row)
	}
}

func TestClaimSkipsLockedRowsIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()

	lockedID := f.insertOutbox(t, 1, "pending", 0, time.Now().Add(-time.Minute), "")
	freeID := f.insertOutbox(t, 2, "pending", 0, time.Now().Add(-time.Minute), "")

	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err := tx.Exec(ctx, `SELECT id FROM outbox_messages WHERE id = $1::uuid FOR UPDATE`, lockedID); err != nil {
		t.Fatalf("lock outbox row: %v", err)
	}

	claims, err := store.Claim(ctx, ClaimOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      RandomToken,
	})
	if err != nil {
		t.Fatalf("Claim() error: %v", err)
	}
	for _, c := range claims {
		if c.MessageID == lockedID {
			t.Fatal("Claim() returned a row locked by another transaction")
		}
	}
	if len(claims) == 0 || claims[0].MessageID != freeID {
		t.Fatalf("Claim() did not claim the unlocked row: %+v", claims)
	}
	if row := f.readOutbox(t, lockedID); row.status != "pending" || row.attempts != 0 {
		t.Fatalf("locked row changed: %+v", row)
	}
}

func TestClaimRollsBackOnTokenFailureIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	id := f.insertOutbox(t, 1, "pending", 0, time.Now().Add(-time.Minute), "")

	_, err := store.Claim(context.Background(), ClaimOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      func() (string, error) { return "", errors.New("token source failed") },
	})
	if err == nil {
		t.Fatal("Claim() succeeded despite token generator failure")
	}
	if row := f.readOutbox(t, id); row.status != "pending" || row.attempts != 0 || row.leaseToken != "" {
		t.Fatalf("claim was not rolled back: %+v", row)
	}
}

func TestConcurrentClaimsAreDisjointIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())

	const rows = 12
	for i := 1; i <= rows; i++ {
		f.insertOutbox(t, i, "pending", 0, time.Now().Add(-time.Minute), "")
	}

	var wg sync.WaitGroup
	results := make([][]Claim, 2)
	errs := make([]error, 2)
	for i := range results {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			results[i], errs[i] = store.Claim(context.Background(), ClaimOptions{
				BatchSize:     rows,
				MaxClaims:     3,
				LeaseDuration: time.Minute,
				ProcessID:     fmt.Sprintf("publisher-test-%d", i),
				NewToken:      RandomToken,
			})
		}(i)
	}
	wg.Wait()

	for _, err := range errs {
		if err != nil {
			t.Fatalf("concurrent Claim() error: %v", err)
		}
	}

	seen := make(map[string]struct{})
	for _, claims := range results {
		for _, c := range claims {
			if _, exists := seen[c.MessageID]; exists {
				t.Fatalf("message %s claimed twice", c.MessageID)
			}
			seen[c.MessageID] = struct{}{}
			if c.ClaimCount != 1 {
				t.Fatalf("concurrent first claim count = %d", c.ClaimCount)
			}
		}
	}
	if len(seen) != rows {
		t.Fatalf("concurrent claims covered %d rows, want %d", len(seen), rows)
	}
}

func TestMarkPublishedFencingIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()
	id := f.insertOutbox(t, 1, "pending", 2, time.Now().Add(-time.Minute), "retained error")

	claims, err := store.Claim(ctx, ClaimOptions{
		BatchSize:     1,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      RandomToken,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim() = %v, %v", claims, err)
	}
	claim := claims[0]

	badToken := claim
	badToken.LeaseToken = testUUID(t)
	applied, err := store.MarkPublished(ctx, badToken)
	if err != nil {
		t.Fatalf("MarkPublished(wrong token) error: %v", err)
	}
	if applied {
		t.Fatal("MarkPublished() accepted a wrong token")
	}
	if row := f.readOutbox(t, id); row.status != "publishing" || row.publishedAt.Valid ||
		row.attempts != 3 || row.lockedBy != claim.Owner || row.leaseToken != claim.LeaseToken {
		t.Fatalf("fenced success mutated row: %+v", row)
	}

	badOwner := claim
	badOwner.Owner = "other-publisher"
	applied, err = store.MarkPublished(ctx, badOwner)
	if err != nil {
		t.Fatalf("MarkPublished(wrong owner) error: %v", err)
	}
	if applied {
		t.Fatal("MarkPublished() accepted a wrong owner")
	}
	if row := f.readOutbox(t, id); row.status != "publishing" || row.publishedAt.Valid ||
		row.attempts != 3 || row.lockedBy != claim.Owner || row.leaseToken != claim.LeaseToken {
		t.Fatalf("fenced success mutated row: %+v", row)
	}

	applied, err = store.MarkPublished(ctx, claim)
	if err != nil {
		t.Fatalf("MarkPublished() error: %v", err)
	}
	if !applied {
		t.Fatal("MarkPublished() rejected the matching claim")
	}
	row := f.readOutbox(t, id)
	if row.status != "published" || row.attempts != 3 || !row.publishedAt.Valid {
		t.Fatalf("published row = %+v", row)
	}
	if row.lockedBy != "" || row.lockedAt.Valid || row.leaseToken != "" {
		t.Fatalf("published row retained lease fields: %+v", row)
	}
	if row.lastError != "" {
		t.Fatalf("published row retained last_error: %q", row.lastError)
	}

	publishedAt := row.publishedAt.Time
	applied, err = store.MarkPublished(ctx, claim)
	if err != nil {
		t.Fatalf("MarkPublished(wrong status) error: %v", err)
	}
	if applied {
		t.Fatal("MarkPublished() accepted a non-publishing row")
	}
	if row := f.readOutbox(t, id); row.status != "published" || !row.publishedAt.Time.Equal(publishedAt) {
		t.Fatalf("fenced success rewrote a published row: %+v", row)
	}
}

func claimOne(t *testing.T, store *Store, maxClaims int) Claim {
	t.Helper()

	claims, err := store.Claim(context.Background(), ClaimOptions{
		BatchSize:     1,
		MaxClaims:     maxClaims,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      RandomToken,
	})
	if err != nil {
		t.Fatalf("Claim() error: %v", err)
	}
	if len(claims) != 1 {
		t.Fatalf("Claim() returned %d claims, want 1", len(claims))
	}
	return claims[0]
}

func TestMarkFailedAccountingIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()
	id := f.insertOutbox(t, 1, "pending", 0, time.Now().Add(-time.Minute), "")

	claims, err := store.Claim(ctx, ClaimOptions{
		BatchSize:     1,
		MaxClaims:     2,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      RandomToken,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim() = %v, %v", claims, err)
	}
	first := claims[0]

	badOwner := first
	badOwner.Owner = "other-publisher"
	fencedOwner, err := store.MarkFailed(ctx, Failure{Claim: badOwner, Backoff: time.Minute, Error: "fenced"})
	if err != nil {
		t.Fatalf("MarkFailed(wrong owner) error: %v", err)
	}
	if fencedOwner.Applied {
		t.Fatal("MarkFailed() accepted a wrong owner")
	}
	if row := f.readOutbox(t, id); row.status != "publishing" || row.attempts != 1 ||
		row.leaseToken != first.LeaseToken || row.lastError != "" {
		t.Fatalf("fenced failure mutated row: %+v", row)
	}

	result, err := store.MarkFailed(ctx, Failure{Claim: first, Backoff: 2 * time.Minute, Error: "transport\nfailure"})
	if err != nil {
		t.Fatalf("MarkFailed() error: %v", err)
	}
	if !result.Applied || result.Status != "pending" || result.Attempts != 1 {
		t.Fatalf("first failure result = %+v", result)
	}
	row := f.readOutbox(t, id)
	if row.status != "pending" || row.attempts != 1 || row.leaseToken != "" || row.lastError != "transport failure" {
		t.Fatalf("row after first failure = %+v", row)
	}
	if !row.availableAt.After(time.Now()) {
		t.Fatalf("failure did not apply a forward backoff gate: %s", row.availableAt)
	}

	// Advance the test clock past the publisher backoff without shortening the
	// production gate path that was just exercised.
	if _, err := f.pool.Exec(ctx,
		`UPDATE outbox_messages SET available_at = now() - interval '1 minute' WHERE id = $1::uuid`, id,
	); err != nil {
		t.Fatalf("advance failure gate: %v", err)
	}

	claims, err = store.Claim(ctx, ClaimOptions{
		BatchSize:     1,
		MaxClaims:     2,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      RandomToken,
	})
	if err != nil || len(claims) != 1 {
		t.Fatalf("second Claim() = %v, %v", claims, err)
	}
	if row := f.readOutbox(t, id); row.lastError != "transport failure" {
		t.Fatalf("claim cleared retained last_error: %q", row.lastError)
	}

	second := claims[0]
	bad := second
	bad.LeaseToken = testUUID(t)
	fenced, err := store.MarkFailed(ctx, Failure{Claim: bad, Backoff: time.Minute, Error: "fenced"})
	if err != nil {
		t.Fatalf("MarkFailed(fenced) error: %v", err)
	}
	if fenced.Applied {
		t.Fatal("MarkFailed() accepted a wrong token")
	}
	if row := f.readOutbox(t, id); row.status != "publishing" || row.lastError != "transport failure" {
		t.Fatalf("fenced failure mutated row: %+v", row)
	}

	result, err = store.MarkFailed(ctx, Failure{Claim: second, Backoff: time.Minute, Error: "final failure"})
	if err != nil {
		t.Fatalf("final MarkFailed() error: %v", err)
	}
	if !result.Applied || result.Status != "failed" || result.Attempts != 2 {
		t.Fatalf("final failure result = %+v", result)
	}
	row = f.readOutbox(t, id)
	if row.status != "failed" || row.attempts != 2 || row.lastError != "final failure" || row.leaseToken != "" {
		t.Fatalf("row after final failure = %+v", row)
	}

	availableAt := row.availableAt
	fencedStatus, err := store.MarkFailed(ctx, Failure{Claim: second, Backoff: time.Minute, Error: "zombie"})
	if err != nil {
		t.Fatalf("MarkFailed(wrong status) error: %v", err)
	}
	if fencedStatus.Applied {
		t.Fatal("MarkFailed() accepted a non-publishing row")
	}
	row = f.readOutbox(t, id)
	if row.status != "failed" || row.attempts != 2 || row.lastError != "final failure" ||
		!row.availableAt.Equal(availableAt) || row.leaseToken != "" {
		t.Fatalf("fenced failure rewrote a terminal row: %+v", row)
	}
}

func TestReapExpiredPublisherLeasesIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()

	pendingID := f.insertOutbox(t, 1, "publishing", 1, time.Now().Add(-time.Hour), "")
	failedID := f.insertOutbox(t, 2, "publishing", 3, time.Now().Add(-time.Hour), "")

	// Make the persisted leases expired while keeping non-publishing rows valid.
	_, err := f.pool.Exec(ctx,
		`UPDATE outbox_messages SET locked_at = now() - interval '10 minutes' WHERE id = ANY($1::uuid[])`,
		[]string{pendingID, failedID},
	)
	if err != nil {
		t.Fatalf("age publisher leases: %v", err)
	}
	liveID := f.insertOutbox(t, 3, "publishing", 0, time.Now().Add(-time.Minute), "")
	_, err = f.pool.Exec(ctx,
		`UPDATE outbox_messages SET locked_at = now() + interval '10 minutes' WHERE id = $1::uuid`, liveID)
	if err != nil {
		t.Fatalf("future-date lease: %v", err)
	}

	result, err := store.ReapExpired(ctx, ReapOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		Backoff:       func(int) (time.Duration, error) { return 2 * time.Minute, nil },
	})
	if err != nil {
		t.Fatalf("ReapExpired() error: %v", err)
	}
	if len(result.Reaped) != 2 {
		t.Fatalf("ReapExpired() reaped %d leases, want 2", len(result.Reaped))
	}

	pending := f.readOutbox(t, pendingID)
	if pending.status != "pending" || pending.attempts != 1 || pending.leaseToken != "" || pending.lastError != reapDiagnostic {
		t.Fatalf("reaped below-limit lease = %+v", pending)
	}
	if !pending.availableAt.After(time.Now()) {
		t.Fatalf("reaped lease did not move gate forward: %s", pending.availableAt)
	}

	failed := f.readOutbox(t, failedID)
	if failed.status != "failed" || failed.attempts != 3 || failed.leaseToken != "" {
		t.Fatalf("reaped at-limit lease = %+v", failed)
	}
	if live := f.readOutbox(t, liveID); live.status != "publishing" {
		t.Fatalf("non-expired lease was reaped: %+v", live)
	}
}

func TestLatePublishAfterReapIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()

	id := f.insertOutbox(t, 1, "publishing", 1, time.Now().Add(-time.Hour), "")
	row := f.readOutbox(t, id)
	stale := Claim{
		MessageID:  id,
		Sequence:   1,
		Owner:      "publisher-test",
		MaxClaims:  3,
		ClaimCount: 1,
		LeaseToken: row.leaseToken,
	}

	reaped, err := store.ReapExpired(ctx, ReapOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		Backoff:       func(int) (time.Duration, error) { return time.Minute, nil },
	})
	if err != nil {
		t.Fatalf("ReapExpired() error: %v", err)
	}
	if len(reaped.Reaped) != 1 {
		t.Fatalf("ReapExpired() reaped %d leases, want 1", len(reaped.Reaped))
	}

	afterReap := f.readOutbox(t, id)
	if afterReap.status != "pending" || afterReap.publishedAt.Valid ||
		afterReap.leaseToken != "" || afterReap.lastError != reapDiagnostic {
		t.Fatalf("reaped row = %+v", afterReap)
	}

	applied, err := store.MarkPublished(ctx, stale)
	if err != nil {
		t.Fatalf("late MarkPublished() error: %v", err)
	}
	if applied {
		t.Fatal("late MarkPublished() revived a reaped lease")
	}
	row = f.readOutbox(t, id)
	if row.status != "pending" || row.attempts != afterReap.attempts ||
		row.publishedAt.Valid || row.leaseToken != "" ||
		row.lastError != reapDiagnostic || !row.availableAt.Equal(afterReap.availableAt) {
		t.Fatalf("late success mutated the reaped row: %+v", row)
	}
}

func TestOldTokenAfterReplacementClaimIntegration(t *testing.T) {
	f := newPublisherFixture(t)
	store := NewStore(f.pool, testLogger())
	ctx := context.Background()

	id := f.insertOutbox(t, 1, "pending", 0, time.Now().Add(-time.Minute), "")
	first := claimOne(t, store, 3)

	// Let the first lease expire and be reaped, then replace the claim.
	if _, err := f.pool.Exec(ctx,
		`UPDATE outbox_messages SET locked_at = now() - interval '10 minutes' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("age first lease: %v", err)
	}
	reaped, err := store.ReapExpired(ctx, ReapOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Minute,
		Backoff:       func(int) (time.Duration, error) { return time.Minute, nil },
	})
	if err != nil {
		t.Fatalf("ReapExpired() error: %v", err)
	}
	if len(reaped.Reaped) != 1 {
		t.Fatalf("ReapExpired() reaped %d leases, want 1", len(reaped.Reaped))
	}
	if _, err := f.pool.Exec(ctx,
		`UPDATE outbox_messages SET available_at = now() - interval '1 minute' WHERE id = $1::uuid`, id); err != nil {
		t.Fatalf("advance gate: %v", err)
	}

	second := claimOne(t, store, 3)
	if second.MessageID != id || second.LeaseToken == first.LeaseToken {
		t.Fatalf("replacement claim = %+v", second)
	}
	replacementRow := f.readOutbox(t, id)
	if replacementRow.status != "publishing" || replacementRow.leaseToken != second.LeaseToken {
		t.Fatalf("replacement claim row = %+v", replacementRow)
	}

	applied, err := store.MarkPublished(ctx, first)
	if err != nil {
		t.Fatalf("old-token MarkPublished() error: %v", err)
	}
	if applied {
		t.Fatal("old token published a replacement claim")
	}
	if row := f.readOutbox(t, id); row.status != "publishing" || row.leaseToken != second.LeaseToken ||
		row.publishedAt.Valid || row.attempts != replacementRow.attempts {
		t.Fatalf("old token mutated the replacement claim: %+v", row)
	}

	applied, err = store.MarkPublished(ctx, second)
	if err != nil {
		t.Fatalf("replacement MarkPublished() error: %v", err)
	}
	if !applied {
		t.Fatal("replacement claim was rejected")
	}
	if row := f.readOutbox(t, id); row.status != "published" || !row.publishedAt.Valid || row.leaseToken != "" {
		t.Fatalf("replacement publication = %+v", row)
	}
}
