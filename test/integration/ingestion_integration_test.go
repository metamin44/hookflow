//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/app/delivery"
	eventapp "github.com/metamin44/hookflow/internal/app/event"
	"github.com/metamin44/hookflow/internal/app/idempotency"
	"github.com/metamin44/hookflow/internal/modules/event"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/database"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

// ingestFixture owns only what ingestion needs: PostgreSQL, no broker. It
// records every seeded organization so cleanup removes exactly its own rows.
type ingestFixture struct {
	t       *testing.T
	pool    *pgxpool.Pool
	service *eventapp.Service
	orgs    []string
}

func newIngestFixture(t *testing.T) *ingestFixture {
	t.Helper()
	cfg, err := config.Load()
	if err != nil {
		t.Skipf("integration database configuration unavailable: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	pool, err := database.NewPool(ctx, cfg.Database)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	t.Cleanup(pool.Close)

	var deliveriesCount, outboxCount int
	if err := pool.Raw().QueryRow(context.Background(),
		`SELECT (SELECT count(*) FROM deliveries), (SELECT count(*) FROM outbox_messages)`,
	).Scan(&deliveriesCount, &outboxCount); err != nil {
		t.Fatalf("check integration database isolation: %v", err)
	}
	if deliveriesCount != 0 || outboxCount != 0 {
		t.Skipf("integration tests require an isolated database; found %d deliveries and %d outbox rows",
			deliveriesCount, outboxCount)
	}

	service, err := eventapp.NewService(pool.Raw(), eventapp.Options{
		Metrics: observability.New(),
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("construct ingestion service: %v", err)
	}
	f := &ingestFixture{t: t, pool: pool.Raw(), service: service}
	t.Cleanup(f.cleanup)
	return f
}

func (f *ingestFixture) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	for _, org := range f.orgs {
		for _, statement := range []string{
			`DELETE FROM idempotency_records WHERE organization_id = $1::uuid`,
			`DELETE FROM delivery_attempts WHERE organization_id = $1::uuid`,
			`DELETE FROM outbox_messages WHERE organization_id = $1::uuid`,
			`DELETE FROM deliveries WHERE organization_id = $1::uuid`,
			`DELETE FROM events WHERE organization_id = $1::uuid`,
			`DELETE FROM endpoint_subscriptions WHERE organization_id = $1::uuid`,
			`DELETE FROM endpoints WHERE organization_id = $1::uuid`,
			`DELETE FROM organizations WHERE id = $1::uuid`,
		} {
			if _, err := f.pool.Exec(ctx, statement, org); err != nil {
				f.t.Errorf("cleanup ingestion rows: %v", err)
				return
			}
		}
	}
}

func (f *ingestFixture) exec(sql string, args ...any) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx, sql, args...); err != nil {
		f.t.Fatalf("execute %q: %v", sql, err)
	}
}

func (f *ingestFixture) count(sql string, args ...any) int {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var n int
	if err := f.pool.QueryRow(ctx, sql, args...).Scan(&n); err != nil {
		f.t.Fatalf("count query failed: %v", err)
	}
	return n
}

func (f *ingestFixture) createOrg(status string) string {
	f.t.Helper()
	id := mustID(f.t)
	f.exec(`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, $3)`,
		id, "ingest-"+id, status)
	f.orgs = append(f.orgs, id)
	return id
}

func (f *ingestFixture) createEndpoint(orgID, name, status string, headers string) string {
	f.t.Helper()
	id := mustID(f.t)
	f.exec(`INSERT INTO endpoints
	        (id, organization_id, name, url, secret_encrypted, http_headers, status)
	        VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6::jsonb, $7)`,
		id, orgID, name, "https://example.test/"+name, "cipher-"+name, headers, status)
	return id
}

func (f *ingestFixture) subscribe(orgID, endpointID, eventType string, active bool) string {
	f.t.Helper()
	id := mustID(f.t)
	f.exec(`INSERT INTO endpoint_subscriptions
	        (id, organization_id, endpoint_id, event_type, is_active)
	        VALUES ($1::uuid, $2::uuid, $3::uuid, $4, $5)`,
		id, orgID, endpointID, eventType, active)
	return id
}

func (f *ingestFixture) accept(orgID, key string, body []byte) (eventapp.Result, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	return f.service.AcceptEvent(ctx, eventapp.AcceptInput{
		OrganizationID: orgID,
		IdempotencyKey: key,
		Body:           body,
	})
}

func (f *ingestFixture) mustAccept(orgID, key string, body []byte) eventapp.Result {
	f.t.Helper()
	result, err := f.accept(orgID, key, body)
	if err != nil {
		f.t.Fatalf("accept %q: %v", key, err)
	}
	return result
}

func ingestBody(eventType string, payload string) []byte {
	return []byte(fmt.Sprintf(`{"event_type":%q,"payload":%s}`, eventType, payload))
}

func TestIngestionFreshClaimPersistsApprovedRows(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "fresh", "active", `{"x-endpoint":"static"}`)
	f.subscribe(org, endpoint, "order.created", true)

	result := f.mustAccept(org, "fresh-key", ingestBody("order.created", `{"order":{"id":42}}`))
	if result.Replayed {
		t.Fatal("fresh claim reported as replay")
	}
	if result.DestinationCount != 1 {
		t.Fatalf("destination count = %d, want 1", result.DestinationCount)
	}
	if result.ReceivedAt.IsZero() {
		t.Fatal("received_at not returned")
	}

	var (
		eventType        string
		payloadMatches   bool
		destinationCount int
		provenanceNull   bool
		expiryOK         bool
	)
	ctx := context.Background()
	if err := f.pool.QueryRow(ctx, `
		SELECT type, payload = '{"order":{"id":42}}'::jsonb, destination_count,
		       (api_key_id IS NULL AND source_ip IS NULL AND headers IS NULL AND occurred_at IS NULL),
		       expires_at - received_at = interval '90 days'
		  FROM events WHERE id = $1::uuid AND organization_id = $2::uuid`,
		result.EventID, org,
	).Scan(&eventType, &payloadMatches, &destinationCount, &provenanceNull, &expiryOK); err != nil {
		t.Fatalf("read accepted event: %v", err)
	}
	if eventType != "order.created" || !payloadMatches || destinationCount != 1 || !provenanceNull || !expiryOK {
		t.Fatalf("event row = type %q payload_ok %v count %d provenance_null %v expiry_ok %v",
			eventType, payloadMatches, destinationCount, provenanceNull, expiryOK)
	}

	var (
		storedEventID string
		hashLength    int
		algorithmOK   bool
		windowOK      bool
	)
	if err := f.pool.QueryRow(ctx, `
		SELECT COALESCE(event_id::text, ''), octet_length(request_hash), hash_algorithm = 'sha256',
		       expires_at - created_at = interval '72 hours'
		  FROM idempotency_records WHERE organization_id = $1::uuid AND idempotency_key = $2`,
		org, "fresh-key",
	).Scan(&storedEventID, &hashLength, &algorithmOK, &windowOK); err != nil {
		t.Fatalf("read idempotency record: %v", err)
	}
	if storedEventID != result.EventID || hashLength != 32 || !algorithmOK || !windowOK {
		t.Fatalf("idempotency row = event %q hash_len %d algorithm_ok %v window_ok %v",
			storedEventID, hashLength, algorithmOK, windowOK)
	}

	var (
		snapshotURL     string
		snapshotHeaders string
		snapshotSecret  string
		state           string
		dispatchSeq     int
		attemptCount    int
		nextAttemptAt   time.Time
	)
	if err := f.pool.QueryRow(ctx, `
		SELECT url, http_headers::text, secret_encrypted, state, dispatch_seq, attempt_count, next_attempt_at
		  FROM deliveries WHERE event_id = $1::uuid`,
		result.EventID,
	).Scan(&snapshotURL, &snapshotHeaders, &snapshotSecret, &state,
		&dispatchSeq, &attemptCount, &nextAttemptAt); err != nil {
		t.Fatalf("read delivery snapshot: %v", err)
	}
	if snapshotURL != "https://example.test/fresh" || snapshotSecret != "cipher-fresh" || snapshotHeaders != `{"x-endpoint": "static"}` {
		t.Fatalf("snapshot = url %q headers %q secret %q", snapshotURL, snapshotHeaders, snapshotSecret)
	}
	if state != "pending" || dispatchSeq != 1 || attemptCount != 0 {
		t.Fatalf("initial delivery = state %q seq %d attempts %d", state, dispatchSeq, attemptCount)
	}
	if nextAttemptAt.Before(result.ReceivedAt) {
		t.Errorf("next_attempt_at %v predates received_at %v", nextAttemptAt, result.ReceivedAt)
	}

	var (
		outboxKind    string
		outboxTopic   string
		outboxStatus  string
		outboxAttempt int
		outboxSeq     int
		availableAt   time.Time
		unnamed       bool
		leaseFree     bool
		payloadIDs    string
	)
	if err := f.pool.QueryRow(ctx, `
		SELECT kind, topic, status, attempts, sequence, available_at,
		       published_at IS NULL,
		       (locked_at IS NULL AND locked_by IS NULL AND lease_token IS NULL),
		       payload ->> 'delivery_id'
		  FROM outbox_messages WHERE organization_id = $1::uuid`,
		org,
	).Scan(&outboxKind, &outboxTopic, &outboxStatus, &outboxAttempt, &outboxSeq,
		&availableAt, &unnamed, &leaseFree, &payloadIDs); err != nil {
		t.Fatalf("read initial outbox row: %v", err)
	}
	if outboxKind != "delivery.dispatch" || outboxTopic != "delivery" || outboxStatus != "pending" ||
		outboxAttempt != 0 || outboxSeq != 1 || !unnamed || !leaseFree {
		t.Fatalf("outbox row = kind %q topic %q status %q attempts %d seq %d unpublished %v lease_free %v",
			outboxKind, outboxTopic, outboxStatus, outboxAttempt, outboxSeq, unnamed, leaseFree)
	}
	if !availableAt.Equal(nextAttemptAt) {
		t.Errorf("outbox available_at %v != delivery next_attempt_at %v", availableAt, nextAttemptAt)
	}
	if payloadIDs == "" {
		t.Error("outbox payload lacks delivery_id")
	}

	if attempts := f.count(`SELECT count(*) FROM delivery_attempts WHERE organization_id = $1::uuid`, org); attempts != 0 {
		t.Errorf("attempt rows written at ingestion: %d", attempts)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, result.EventID); deliveries != result.DestinationCount {
		t.Errorf("destination_count %d != delivery rows %d", result.DestinationCount, deliveries)
	}
}

func TestIngestionSameHashReplayIsByteStable(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "replay", "active", `{}`)
	f.subscribe(org, endpoint, "replay.test", true)

	body := ingestBody("replay.test", `{"n":1}`)
	first := f.mustAccept(org, "replay-key", body)
	second, err := f.accept(org, "replay-key", body)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	if !second.Replayed {
		t.Fatal("identical retry not reported as replay")
	}
	if second.EventID != first.EventID || second.DestinationCount != first.DestinationCount {
		t.Fatalf("replay = %+v, first = %+v", second, first)
	}
	if !second.ReceivedAt.Equal(first.ReceivedAt) {
		t.Errorf("replay received_at %v != stored %v", second.ReceivedAt, first.ReceivedAt)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
		t.Errorf("replay created an event: %d rows", events)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid`, org); deliveries != 1 {
		t.Errorf("replay created deliveries: %d rows", deliveries)
	}
	if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != 1 {
		t.Errorf("replay created outbox rows: %d", outboxRows)
	}
}

func TestIngestionDifferentHashConflicts(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "conflict", "active", `{}`)
	f.subscribe(org, endpoint, "conflict.test", true)

	first := f.mustAccept(org, "conflict-key", ingestBody("conflict.test", `{"n":1}`))
	_, err := f.accept(org, "conflict-key", ingestBody("conflict.test", `{"n":2}`))
	if !errors.Is(err, eventapp.ErrIdempotencyConflict) {
		t.Fatalf("conflicting body error = %v, want ErrIdempotencyConflict", err)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
		t.Errorf("conflict created an event: %d rows", events)
	}
	if linked := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid AND event_id = $2::uuid`,
		org, first.EventID); linked != 1 {
		t.Errorf("conflict changed the stored link: %d", linked)
	}
}

func TestIngestionExpiredKeyTakesOverInPlace(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "takeover", "active", `{}`)
	f.subscribe(org, endpoint, "takeover.test", true)

	first := f.mustAccept(org, "takeover-key", ingestBody("takeover.test", `{"n":1}`))
	var recordID string
	var createdAt time.Time
	var firstHash []byte
	if err := f.pool.QueryRow(context.Background(), `
		SELECT id::text, created_at, request_hash
		  FROM idempotency_records WHERE organization_id = $1::uuid AND idempotency_key = $2`,
		org, "takeover-key",
	).Scan(&recordID, &createdAt, &firstHash); err != nil {
		t.Fatalf("read first record: %v", err)
	}
	f.exec(`UPDATE idempotency_records SET created_at = now() - interval '73 hours',
	                 expires_at = now() - interval '1 hour'
	          WHERE organization_id = $1::uuid AND idempotency_key = $2`, org, "takeover-key")

	second := f.mustAccept(org, "takeover-key", ingestBody("takeover.test", `{"n":2}`))
	if second.Replayed {
		t.Fatal("expired key treated as replay")
	}
	if second.EventID == first.EventID {
		t.Fatal("expired takeover reused the previous event")
	}

	var (
		sameID     string
		linked     string
		newHash    []byte
		newCreated time.Time
		windowOK   bool
	)
	if err := f.pool.QueryRow(context.Background(), `
		SELECT id::text, COALESCE(event_id::text, ''), request_hash, created_at,
		       expires_at - created_at = interval '72 hours'
		  FROM idempotency_records WHERE organization_id = $1::uuid AND idempotency_key = $2`,
		org, "takeover-key",
	).Scan(&sameID, &linked, &newHash, &newCreated, &windowOK); err != nil {
		t.Fatalf("read taken-over record: %v", err)
	}
	if sameID != recordID {
		t.Errorf("takeover created a new record %q instead of reusing %q", sameID, recordID)
	}
	if linked != second.EventID {
		t.Errorf("takeover link = %q, want %q", linked, second.EventID)
	}
	if string(newHash) == string(firstHash) {
		t.Error("takeover kept the previous request hash")
	}
	if newCreated.Before(createdAt) || !windowOK {
		t.Errorf("takeover window = created %v (previous %v) window_ok %v", newCreated, createdAt, windowOK)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 2 {
		t.Errorf("event rows after takeover = %d, want 2", events)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, second.EventID); deliveries != 1 {
		t.Errorf("takeover deliveries = %d, want 1", deliveries)
	}
}

func TestIngestionConcurrentSameKeyHasSingleWinner(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "concurrent", "active", `{}`)
	f.subscribe(org, endpoint, "concurrent.test", true)

	const contenders = 8
	body := ingestBody("concurrent.test", `{"n":1}`)
	results := make([]eventapp.Result, contenders)
	errs := make([]error, contenders)
	var wg sync.WaitGroup
	start := make(chan struct{})
	for i := 0; i < contenders; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = f.accept(org, "concurrent-key", body)
		}(i)
	}
	close(start)
	wg.Wait()

	owners := 0
	eventID := ""
	for i := 0; i < contenders; i++ {
		if errs[i] != nil {
			t.Fatalf("contender %d failed: %v", i, errs[i])
		}
		if !results[i].Replayed {
			owners++
		}
		if eventID == "" {
			eventID = results[i].EventID
		} else if results[i].EventID != eventID {
			t.Fatalf("contender %d saw event %s, want %s", i, results[i].EventID, eventID)
		}
	}
	if owners != 1 {
		t.Fatalf("owners = %d, want exactly 1", owners)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
		t.Errorf("events = %d, want 1", events)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid`, org); deliveries != 1 {
		t.Errorf("deliveries = %d, want 1", deliveries)
	}
	if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != 1 {
		t.Errorf("outbox rows = %d, want 1", outboxRows)
	}
}

func TestIngestionConcurrentExpiredTakeoverHasSingleWinner(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "takeover-race", "active", `{}`)
	f.subscribe(org, endpoint, "takeover.race", true)

	first := f.mustAccept(org, "takeover-race-key", ingestBody("takeover.race", `{"n":1}`))
	f.exec(`UPDATE idempotency_records SET created_at = now() - interval '73 hours',
	                 expires_at = now() - interval '1 hour'
	          WHERE organization_id = $1::uuid AND idempotency_key = $2`, org, "takeover-race-key")

	body := ingestBody("takeover.race", `{"n":2}`)
	var wg sync.WaitGroup
	results := make([]eventapp.Result, 2)
	errs := make([]error, 2)
	start := make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			results[index], errs[index] = f.accept(org, "takeover-race-key", body)
		}(i)
	}
	close(start)
	wg.Wait()

	owners := 0
	for i := 0; i < 2; i++ {
		if errs[i] != nil {
			t.Fatalf("contender %d failed: %v", i, errs[i])
		}
		if !results[i].Replayed {
			owners++
		}
		if results[i].EventID != results[0].EventID {
			t.Fatalf("contender %d saw %s, want %s", i, results[i].EventID, results[0].EventID)
		}
		if results[i].EventID == first.EventID {
			t.Fatalf("contender %d reused the expired event", i)
		}
	}
	if owners != 1 {
		t.Fatalf("owners = %d, want exactly 1", owners)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 2 {
		t.Errorf("events = %d, want old + takeover", events)
	}
	if linked := f.count(`SELECT count(*) FROM idempotency_records
	                       WHERE organization_id = $1::uuid AND event_id = $2::uuid`,
		org, results[0].EventID); linked != 1 {
		t.Errorf("takeover link rows = %d, want 1", linked)
	}
}

// TestIngestionSweeperDeletionRace races the approved expired-row delete
// against a takeover of the same key. Either side may win; both outcomes must
// leave exactly one live record linked to the returned event.
func TestIngestionSweeperDeletionRace(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "sweeper-race", "active", `{}`)
	f.subscribe(org, endpoint, "sweeper.race", true)

	const iterations = 20
	for i := 0; i < iterations; i++ {
		key := fmt.Sprintf("sweeper-race-%02d", i)
		first := f.mustAccept(org, key, ingestBody("sweeper.race", `{"n":1}`))
		f.exec(`UPDATE idempotency_records SET created_at = now() - interval '73 hours',
		                 expires_at = now() - interval '1 hour'
		          WHERE organization_id = $1::uuid AND idempotency_key = $2`, org, key)

		var wg sync.WaitGroup
		var deleteErr error
		var result eventapp.Result
		var acceptErr error
		start := make(chan struct{})
		wg.Add(2)
		go func() {
			defer wg.Done()
			<-start
			ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
			defer cancel()
			_, deleteErr = f.pool.Exec(ctx, `DELETE FROM idempotency_records
				WHERE organization_id = $1::uuid AND idempotency_key = $2 AND expires_at <= now()`,
				org, key)
		}()
		go func() {
			defer wg.Done()
			<-start
			result, acceptErr = f.accept(org, key, ingestBody("sweeper.race", `{"n":2}`))
		}()
		close(start)
		wg.Wait()

		if deleteErr != nil {
			t.Fatalf("iteration %d sweeper delete: %v", i, deleteErr)
		}
		if acceptErr != nil {
			t.Fatalf("iteration %d accept: %v", i, acceptErr)
		}
		if result.Replayed {
			t.Fatalf("iteration %d replayed an expired key", i)
		}
		if result.EventID == first.EventID {
			t.Fatalf("iteration %d reused the expired event", i)
		}
		if linked := f.count(`SELECT count(*) FROM idempotency_records
		                       WHERE organization_id = $1::uuid AND idempotency_key = $2 AND event_id = $3::uuid`,
			org, key, result.EventID); linked != 1 {
			t.Fatalf("iteration %d live record rows = %d, want 1", i, linked)
		}
		if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, result.EventID); deliveries != 1 {
			t.Fatalf("iteration %d deliveries = %d, want 1", i, deliveries)
		}
	}
}

func TestIngestionOrganizationIsolation(t *testing.T) {
	f := newIngestFixture(t)
	orgA := f.createOrg("active")
	orgB := f.createOrg("active")
	endpointA := f.createEndpoint(orgA, "isolation-a", "active", `{}`)
	endpointB := f.createEndpoint(orgB, "isolation-b", "active", `{}`)
	f.subscribe(orgA, endpointA, "isolation.test", true)
	f.subscribe(orgB, endpointB, "isolation.test", true)

	body := ingestBody("isolation.test", `{"n":1}`)
	resultA := f.mustAccept(orgA, "shared-key", body)
	resultB := f.mustAccept(orgB, "shared-key", body)
	if resultA.Replayed || resultB.Replayed {
		t.Fatal("independent organizations shared idempotency state")
	}
	if resultA.EventID == resultB.EventID {
		t.Fatal("organizations shared an event id")
	}
	for _, org := range []string{orgA, orgB} {
		if records := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`, org); records != 1 {
			t.Errorf("org %s idempotency rows = %d, want 1", org, records)
		}
		if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
			t.Errorf("org %s events = %d, want 1", org, events)
		}
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, resultA.EventID); deliveries != 1 {
		t.Errorf("org A deliveries = %d, want 1", deliveries)
	}
}

func TestIngestionDestinationMatchRules(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	matchA := f.createEndpoint(org, "match-a", "active", `{}`)
	f.subscribe(org, matchA, "match.test", true)
	paused := f.createEndpoint(org, "paused-b", "paused", `{}`)
	f.subscribe(org, paused, "match.test", true)
	deleted := f.createEndpoint(org, "deleted-c", "active", `{}`)
	f.exec(`UPDATE endpoints SET deleted_at = now() WHERE id = $1::uuid`, deleted)
	f.subscribe(org, deleted, "match.test", true)
	otherType := f.createEndpoint(org, "other-type-d", "active", `{}`)
	f.subscribe(org, otherType, "other.test", true)
	inactiveSub := f.createEndpoint(org, "inactive-sub-e", "active", `{}`)
	inactiveSubID := f.subscribe(org, inactiveSub, "match.test", false)

	result := f.mustAccept(org, "match-key-1", ingestBody("match.test", `{"n":1}`))
	if result.DestinationCount != 1 {
		t.Fatalf("matched %d destinations, want only the active subscription", result.DestinationCount)
	}
	if matchedEndpoint := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid AND endpoint_id = $2::uuid`,
		result.EventID, matchA); matchedEndpoint != 1 {
		t.Errorf("active endpoint not snapshotted")
	}

	f.exec(`UPDATE endpoint_subscriptions SET is_active = true WHERE id = $1::uuid`, inactiveSubID)
	second := f.mustAccept(org, "match-key-2", ingestBody("match.test", `{"n":2}`))
	if second.DestinationCount != 2 {
		t.Fatalf("after activation matched %d destinations, want 2", second.DestinationCount)
	}
	if perEvent := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, second.EventID); perEvent != second.DestinationCount {
		t.Errorf("destination_count %d != deliveries %d", second.DestinationCount, perEvent)
	}
}

func TestIngestionZeroDestinations(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")

	body := ingestBody("nobody.listens", `{"n":1}`)
	result := f.mustAccept(org, "zero-key", body)
	if result.DestinationCount != 0 || result.Replayed {
		t.Fatalf("zero-destination result = %+v", result)
	}
	if eventRow := f.count(`SELECT count(*) FROM events WHERE id = $1::uuid AND destination_count = 0`,
		result.EventID); eventRow != 1 {
		t.Errorf("zero-destination event row missing or miscounted")
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid`, org); deliveries != 0 {
		t.Errorf("zero-destination event wrote %d deliveries", deliveries)
	}
	if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != 0 {
		t.Errorf("zero-destination event wrote %d outbox rows", outboxRows)
	}
	if linked := f.count(`SELECT count(*) FROM idempotency_records
	                       WHERE organization_id = $1::uuid AND event_id = $2::uuid`, org, result.EventID); linked != 1 {
		t.Errorf("zero-destination idempotency link = %d, want 1", linked)
	}
	replay, err := f.accept(org, "zero-key", body)
	if err != nil || !replay.Replayed || replay.EventID != result.EventID || replay.DestinationCount != 0 {
		t.Fatalf("zero-destination replay = %+v, %v", replay, err)
	}
}

func TestIngestionSecretSnapshotIsImmutable(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "snapshot", "active", `{"x-endpoint":"static"}`)
	f.subscribe(org, endpoint, "snapshot.test", true)

	first := f.mustAccept(org, "snapshot-key-1", ingestBody("snapshot.test", `{"n":1}`))
	f.exec(`UPDATE endpoints
	           SET secret_encrypted = 'cipher-rotated',
	               url = 'https://rotated.test/hook',
	               http_headers = '{"rotated":"yes"}'::jsonb
	         WHERE id = $1::uuid`, endpoint)

	var (
		url     string
		headers string
		secret  string
	)
	if err := f.pool.QueryRow(context.Background(), `
		SELECT url, http_headers::text, secret_encrypted
		  FROM deliveries WHERE event_id = $1::uuid`, first.EventID,
	).Scan(&url, &headers, &secret); err != nil {
		t.Fatalf("read accepted snapshot: %v", err)
	}
	if url != "https://example.test/snapshot" || secret != "cipher-snapshot" || headers != `{"x-endpoint": "static"}` {
		t.Fatalf("accepted snapshot changed after rotation: url %q headers %q secret %q", url, headers, secret)
	}

	second := f.mustAccept(org, "snapshot-key-2", ingestBody("snapshot.test", `{"n":2}`))
	if err := f.pool.QueryRow(context.Background(), `
		SELECT url, secret_encrypted FROM deliveries WHERE event_id = $1::uuid`, second.EventID,
	).Scan(&url, &secret); err != nil {
		t.Fatalf("read rotated snapshot: %v", err)
	}
	if url != "https://rotated.test/hook" || secret != "cipher-rotated" {
		t.Fatalf("new event snapshot = url %q secret %q", url, secret)
	}
}

func TestIngestionMaximumFanOut(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	const fanOut = 100
	for i := 0; i < fanOut; i++ {
		endpoint := f.createEndpoint(org, fmt.Sprintf("fan-%03d", i), "active", `{}`)
		f.subscribe(org, endpoint, "fanout.test", true)
	}

	result := f.mustAccept(org, "fanout-key", ingestBody("fanout.test", `{"n":1}`))
	if result.DestinationCount != fanOut {
		t.Fatalf("destination count = %d, want %d", result.DestinationCount, fanOut)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, result.EventID); deliveries != fanOut {
		t.Fatalf("delivery rows = %d, want %d", deliveries, fanOut)
	}
	if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != fanOut {
		t.Fatalf("outbox rows = %d, want %d", outboxRows, fanOut)
	}
	if eventCount := f.count(`SELECT count(*) FROM events WHERE id = $1::uuid AND destination_count = (
	        SELECT count(*) FROM deliveries WHERE event_id = $1::uuid)`, result.EventID); eventCount != 1 {
		t.Error("destination_count does not equal COUNT(deliveries)")
	}
	if distinctSchedules := f.count(`SELECT count(DISTINCT next_attempt_at) FROM deliveries WHERE event_id = $1::uuid`,
		result.EventID); distinctSchedules != 1 {
		t.Errorf("deliveries use %d scheduling instants, want 1", distinctSchedules)
	}
	if notScheduled := f.count(`SELECT count(*) FROM deliveries d
	       JOIN outbox_messages o ON o.delivery_id = d.id
	      WHERE d.event_id = $1::uuid AND o.available_at = d.next_attempt_at`, result.EventID); notScheduled != fanOut {
		t.Errorf("outbox/delivery scheduling mismatch rows = %d, want %d", notScheduled, fanOut)
	}
}

func TestIngestionRejectsInactiveOrganization(t *testing.T) {
	f := newIngestFixture(t)
	for _, status := range []string{"suspended", "archived"} {
		t.Run(status, func(t *testing.T) {
			org := f.createOrg(status)
			endpoint := f.createEndpoint(org, "inactive-"+status, "active", `{}`)
			f.subscribe(org, endpoint, "inactive.test", true)

			_, err := f.accept(org, "inactive-key-"+status, ingestBody("inactive.test", `{"n":1}`))
			var inactive *eventapp.InactiveOrganizationError
			if !errors.As(err, &inactive) || inactive.Status != status {
				t.Fatalf("error = %v, want InactiveOrganizationError(%s)", err, status)
			}
			if records := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`, org); records != 0 {
				t.Errorf("inactive organization wrote %d idempotency rows", records)
			}
			if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 0 {
				t.Errorf("inactive organization wrote %d events", events)
			}
		})
	}
}

// TestIngestionRollbackLeavesNoPartialRows proves that a failure at the link,
// delivery and outbox stages rolls back the whole ingestion transaction.
func TestIngestionRollbackLeavesNoPartialRows(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "rollback", "active", `{}`)
	subscription := f.subscribe(org, endpoint, "rollback.test", true)
	ctx := context.Background()

	t.Run("unlinked claim fails the deferred trigger", func(t *testing.T) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		recordID := mustID(t)
		if _, err := idempotency.Claim(ctx, tx, idempotency.ClaimInput{
			RecordID:       recordID,
			OrganizationID: org,
			Key:            "rollback-trigger",
			Hash:           make([]byte, 32),
		}); err != nil {
			t.Fatalf("claim: %v", err)
		}
		if err := tx.Commit(ctx); err == nil {
			t.Fatal("commit succeeded without an event link")
		}
		if rows := f.count(`SELECT count(*) FROM idempotency_records
		                     WHERE organization_id = $1::uuid AND idempotency_key = 'rollback-trigger'`, org); rows != 0 {
			t.Errorf("failed commit left %d idempotency rows", rows)
		}
	})

	t.Run("delivery foreign key failure rolls back event and claim", func(t *testing.T) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		recordID := mustID(t)
		resolution, err := idempotency.Claim(ctx, tx, idempotency.ClaimInput{
			RecordID:       recordID,
			OrganizationID: org,
			Key:            "rollback-delivery",
			Hash:           make([]byte, 32),
		})
		if err != nil || !resolution.Owner {
			t.Fatalf("claim resolution = %+v, %v", resolution, err)
		}
		eventID := mustID(t)
		batch := &pgx.Batch{}
		event.AppendInsert(batch, event.Record{
			ID:               eventID,
			OrganizationID:   org,
			Type:             "rollback.test",
			Payload:          []byte(`{"n":1}`),
			DestinationCount: 1,
		})
		idempotency.AppendLink(batch, resolution.RecordID, eventID)
		batch.Queue(`INSERT INTO deliveries
			(id, organization_id, event_id, endpoint_id, endpoint_subscription_id,
			 url, http_headers, secret_encrypted, state, dispatch_seq, next_attempt_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
			        'https://example.test/x', '{}'::jsonb, '', 'pending', 1, now())`,
			mustID(t), org, eventID, endpoint, mustID(t))

		results := tx.SendBatch(ctx, batch)
		var receivedAt time.Time
		if err := results.QueryRow().Scan(&receivedAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
		if _, err := results.Exec(); err != nil {
			t.Fatalf("link claim: %v", err)
		}
		if _, err := results.Exec(); err == nil {
			t.Fatal("delivery row with unknown subscription id was accepted")
		}
		_ = results.Close()
		_ = tx.Rollback(ctx)

		if rows := f.count(`SELECT count(*) FROM events WHERE id = $1::uuid`, eventID); rows != 0 {
			t.Errorf("failed delivery left %d event rows", rows)
		}
		if rows := f.count(`SELECT count(*) FROM idempotency_records
		                     WHERE organization_id = $1::uuid AND idempotency_key = 'rollback-delivery'`, org); rows != 0 {
			t.Errorf("failed delivery left %d idempotency rows", rows)
		}
	})

	t.Run("outbox constraint failure rolls back everything", func(t *testing.T) {
		tx, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		recordID := mustID(t)
		resolution, err := idempotency.Claim(ctx, tx, idempotency.ClaimInput{
			RecordID:       recordID,
			OrganizationID: org,
			Key:            "rollback-outbox",
			Hash:           make([]byte, 32),
		})
		if err != nil || !resolution.Owner {
			t.Fatalf("claim resolution = %+v, %v", resolution, err)
		}
		eventID := mustID(t)
		deliveryID := mustID(t)
		batch := &pgx.Batch{}
		event.AppendInsert(batch, event.Record{
			ID:               eventID,
			OrganizationID:   org,
			Type:             "rollback.test",
			Payload:          []byte(`{"n":1}`),
			DestinationCount: 1,
		})
		idempotency.AppendLink(batch, resolution.RecordID, eventID)
		if err := delivery.AppendAccepted(batch, delivery.Accepted{
			ID:             deliveryID,
			OrganizationID: org,
			EventID:        eventID,
			EndpointID:     endpoint,
			SubscriptionID: subscription,
			URL:            "https://example.test/rollback",
			Headers:        `{}`,
			NextAttemptAt:  time.Now(),
		}); err != nil {
			t.Fatalf("queue delivery: %v", err)
		}
		batch.Queue(`INSERT INTO outbox_messages
			(id, organization_id, delivery_id, sequence, kind, topic, payload,
			 status, attempts, available_at)
			VALUES ($1::uuid, $2::uuid, $3::uuid, 1, 'bogus.kind', 'delivery', '{}'::jsonb,
			        'pending', 0, now())`,
			mustID(t), org, deliveryID)

		results := tx.SendBatch(ctx, batch)
		var receivedAt time.Time
		if err := results.QueryRow().Scan(&receivedAt); err != nil {
			t.Fatalf("insert event: %v", err)
		}
		if _, err := results.Exec(); err != nil {
			t.Fatalf("link claim: %v", err)
		}
		if _, err := results.Exec(); err != nil {
			t.Fatalf("insert delivery: %v", err)
		}
		if _, err := results.Exec(); err == nil {
			t.Fatal("outbox row with an unknown kind was accepted")
		}
		_ = results.Close()
		_ = tx.Rollback(ctx)

		for _, statement := range []struct {
			name string
			sql  string
		}{
			{"events", `SELECT count(*) FROM events WHERE id = $1::uuid`},
			{"deliveries", `SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid AND event_id = $2::uuid`},
			{"outbox", `SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`},
			{"idempotency", `SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid AND idempotency_key = 'rollback-outbox'`},
		} {
			var rows int
			var err error
			switch statement.name {
			case "events":
				err = f.pool.QueryRow(ctx, statement.sql, eventID).Scan(&rows)
			case "deliveries":
				err = f.pool.QueryRow(ctx, statement.sql, org, eventID).Scan(&rows)
			default:
				err = f.pool.QueryRow(ctx, statement.sql, org).Scan(&rows)
			}
			if err != nil {
				t.Fatalf("count %s: %v", statement.name, err)
			}
			if rows != 0 {
				t.Errorf("failed outbox stage left %d %s rows", rows, statement.name)
			}
		}
	})
}
