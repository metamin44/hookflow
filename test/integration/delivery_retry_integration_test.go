//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/platform/id"
)

type retryDeliveryDoc struct {
	ID               string    `json:"id"`
	EventID          string    `json:"event_id"`
	EndpointID       string    `json:"endpoint_id"`
	Generation       int       `json:"generation"`
	ParentDeliveryID *string   `json:"parent_delivery_id"`
	State            string    `json:"state"`
	AttemptCount     int       `json:"attempt_count"`
	CreatedAt        time.Time `json:"created_at"`
}

type retryGridRow struct {
	ID               string
	State            string
	Generation       int
	ParentDeliveryID *string
	DispatchSeq      int
	AttemptCount     int
	URL              string
	Headers          map[string]string
	SecretEncrypted  string
	SubscriptionID   *string
	NextAttemptAt    pgtype.Timestamptz
	CompletedAt      pgtype.Timestamptz
	LeaseToken       string
	LeaseExpiresAt   pgtype.Timestamptz
}

func (f *mgmtFixture) retryKey(scopes ...string) (string, string) {
	f.t.Helper()
	return f.createKey("delivery-retry", scopes...)
}

func (f *mgmtFixture) retryPath(sourceID string) string {
	return "/v1/deliveries/" + sourceID + "/retry"
}

func (f *mgmtFixture) retryDelivery(token, sourceID, key string) mgmtReply {
	f.t.Helper()
	return f.do(http.MethodPost, f.retryPath(sourceID), token, "", "Idempotency-Key", key)
}

// retryDuplicateKey sends two distinct Idempotency-Key field lines. The shared
// roundTrip helper uses Set, so it cannot express this malformed request.
func (f *mgmtFixture) retryDuplicateKey(token, sourceID string) mgmtReply {
	f.t.Helper()
	request, err := http.NewRequest(http.MethodPost, f.server.URL+f.retryPath(sourceID), nil)
	if err != nil {
		f.t.Fatalf("build duplicate-key retry request: %v", err)
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Add("Idempotency-Key", "one")
	request.Header.Add("Idempotency-Key", "two")
	response, err := f.client.Do(request)
	if err != nil {
		f.t.Fatalf("duplicate-key retry: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		f.t.Fatalf("read duplicate-key retry response: %v", err)
	}
	return mgmtReply{status: response.StatusCode, header: response.Header, body: body}
}

func (f *mgmtFixture) readRetryGridRow(id string) retryGridRow {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var row retryGridRow
	var headers string
	err := f.pool.QueryRow(ctx,
		`SELECT id::text, state, generation, parent_delivery_id::text, dispatch_seq,
		        attempt_count, url, http_headers::text, secret_encrypted,
		        endpoint_subscription_id::text, next_attempt_at, completed_at,
		        COALESCE(dispatch_lease_token::text, ''), dispatch_lease_expires_at
		   FROM deliveries WHERE organization_id = $1::uuid AND id = $2::uuid`,
		f.orgID, id,
	).Scan(&row.ID, &row.State, &row.Generation, &row.ParentDeliveryID, &row.DispatchSeq,
		&row.AttemptCount, &row.URL, &headers, &row.SecretEncrypted,
		&row.SubscriptionID, &row.NextAttemptAt, &row.CompletedAt,
		&row.LeaseToken, &row.LeaseExpiresAt)
	if err != nil {
		f.t.Fatalf("read retry delivery row: %v", err)
	}
	if err := json.Unmarshal([]byte(headers), &row.Headers); err != nil {
		f.t.Fatalf("decode delivery headers: %v", err)
	}
	return row
}

func (f *mgmtFixture) countDeliveries() int {
	f.t.Helper()
	var total int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid`, f.orgID,
	).Scan(&total); err != nil {
		f.t.Fatalf("count deliveries: %v", err)
	}
	return total
}

func (f *mgmtFixture) countOutboxFor(deliveryID string) int {
	f.t.Helper()
	var total int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid AND delivery_id = $2::uuid`,
		f.orgID, deliveryID,
	).Scan(&total); err != nil {
		f.t.Fatalf("count delivery outbox rows: %v", err)
	}
	return total
}

func (f *mgmtFixture) seedRetryEvent(eventID, eventType string, expiresAt time.Time) {
	f.t.Helper()
	received := time.Now().UTC().Add(-2 * time.Hour)
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO events (id, organization_id, type, payload, destination_count, received_at, expires_at)
		 VALUES ($1::uuid, $2::uuid, $3, '{"ok":true}'::jsonb, 1, $4, $5)`,
		eventID, f.orgID, eventType, received, expiresAt,
	); err != nil {
		f.t.Fatalf("seed retry event: %v", err)
	}
}

func (f *mgmtFixture) seedRetrySource(eventID, endpointID, state string, completedAt *time.Time, leaseUntil *time.Time) string {
	f.t.Helper()
	sourceID := mustID(f.t)
	var next *time.Time
	if state == "pending" || state == "retrying" {
		value := time.Now().UTC().Add(time.Minute)
		next = &value
	}
	f.seedDelivery(f.orgID, seedDelivery{
		ID: sourceID, EventID: eventID, EndpointID: endpointID,
		State: state, CreatedAt: time.Now().UTC().Add(-90 * time.Minute),
		AttemptCount: 2, NextAttemptAt: next, CompletedAt: completedAt,
		LeaseExpiresAt: leaseUntil,
	})
	status := http.StatusBadGateway
	errorClass := "http_status"
	f.seedAttempt(f.orgID, seedAttempt{DeliveryID: sourceID, Number: 1, Outcome: "retryable_failure",
		Status: &status, ErrorClass: &errorClass, StartedAt: time.Now().UTC().Add(-80 * time.Minute)})
	f.seedAttempt(f.orgID, seedAttempt{DeliveryID: sourceID, Number: 2, Outcome: "terminal_failure",
		Status: &status, ErrorClass: &errorClass, StartedAt: time.Now().UTC().Add(-70 * time.Minute)})
	return sourceID
}

func (f *mgmtFixture) createRetryEndpoint(token, name string) endpointDoc {
	f.t.Helper()
	return f.createEndpoint(token, fmt.Sprintf(
		`{"name":%q,"url":"https://receiver.invalid/original","http_headers":{"x-tenant":"before"}}`, name))
}

func (f *mgmtFixture) createRetrySubscription(token, endpointID, eventType string) string {
	f.t.Helper()
	reply := f.do(http.MethodPost,
		f.endpoint(endpointID)+"/subscriptions", token,
		fmt.Sprintf(`{"event_type":%q}`, eventType))
	wantStatus(f.t, reply, http.StatusCreated, "create retry subscription")
	var doc struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(reply.body, &doc); err != nil {
		f.t.Fatalf("decode subscription: %v", err)
	}
	return doc.ID
}

func TestManualDeliveryRetryCreatesSnapshotGeneration(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token, keyID := f.retryKey("endpoints:write", "deliveries:retry")
	history, _ := f.createKey("retry-history", "deliveries:read")

	endpoint := f.createRetryEndpoint(token, "retry-endpoint")
	eventID := mustID(t)
	f.seedRetryEvent(eventID, "order.created", time.Now().UTC().Add(24*time.Hour))
	subscriptionID := f.createRetrySubscription(token, endpoint.ID, "order.created")
	sourceID := f.seedRetrySource(eventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)

	// The retry snapshot must read the current endpoint, not the original
	// delivery snapshot. A legacy credential-bearing header is filtered by the
	// existing SafeHeaders rule.
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE endpoints SET url = $2, http_headers = $3::jsonb WHERE id = $1::uuid`,
		endpoint.ID, "https://receiver.invalid/current?token=ENDPOINT-URL-SECRET",
		`{"authorization":"Bearer LEGACY-HEADER-SECRET","x-trace-id":"after"}`,
	); err != nil {
		t.Fatalf("update endpoint snapshot inputs: %v", err)
	}
	secret := f.storedSecret(endpoint.ID)

	reply := f.retryDelivery(token, sourceID, "retry-key-1")
	wantStatus(t, reply, http.StatusCreated, "manual retry")
	if got := reply.header.Get("Location"); got != "/v1/deliveries/"+decodeMgmt[retryDeliveryDoc](t, reply).ID {
		t.Fatalf("Location = %q", got)
	}
	if reply.header.Get("Cache-Control") != "no-store" {
		t.Fatalf("Cache-Control = %q", reply.header.Get("Cache-Control"))
	}
	doc := decodeMgmt[retryDeliveryDoc](t, reply)
	if doc.Generation != 2 || doc.ParentDeliveryID == nil || *doc.ParentDeliveryID != sourceID ||
		doc.State != "pending" || doc.AttemptCount != 0 || doc.EventID != eventID || doc.EndpointID != endpoint.ID {
		t.Fatalf("retry response = %+v", doc)
	}
	for _, secretValue := range []string{"ENDPOINT-URL-SECRET", "LEGACY-HEADER-SECRET", endpoint.Secret, secret} {
		if secretValue != "" && bytes.Contains(reply.body, []byte(secretValue)) {
			t.Fatal("retry response exposed destination or secret material")
		}
	}

	created := f.readRetryGridRow(doc.ID)
	if created.Generation != 2 || created.ParentDeliveryID == nil || *created.ParentDeliveryID != sourceID ||
		created.State != "pending" || created.DispatchSeq != 1 || created.AttemptCount != 0 ||
		created.LeaseToken != "" || created.CompletedAt.Valid || !created.NextAttemptAt.Valid {
		t.Fatalf("created generation row = %+v", created)
	}
	if created.URL != "https://receiver.invalid/current?token=ENDPOINT-URL-SECRET" {
		t.Fatalf("snapshot url = %q", created.URL)
	}
	if len(created.Headers) != 1 || created.Headers["x-trace-id"] != "after" {
		t.Fatalf("snapshot headers = %+v", created.Headers)
	}
	if created.SecretEncrypted != secret || created.SecretEncrypted == "ENVELOPE-SECRET" {
		t.Fatal("retry snapshot did not copy the current endpoint envelope")
	}
	if created.SubscriptionID == nil || *created.SubscriptionID != subscriptionID {
		t.Fatalf("subscription provenance = %v", created.SubscriptionID)
	}

	source := f.readRetryGridRow(sourceID)
	if source.State != "failed" || source.AttemptCount != 2 || source.Generation != 1 ||
		source.ParentDeliveryID != nil || source.URL != "https://receiver.invalid/hook?token=DELIVERY-URL-SECRET" ||
		source.SecretEncrypted != "ENVELOPE-SECRET" {
		t.Fatalf("source delivery mutated: %+v", source)
	}
	var attempts int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM delivery_attempts WHERE delivery_id = $1::uuid`, sourceID,
	).Scan(&attempts); err != nil || attempts != 2 {
		t.Fatalf("source attempts = %d, err %v", attempts, err)
	}
	if f.countOutboxFor(sourceID) != 0 || f.countOutboxFor(doc.ID) != 1 {
		t.Fatal("retry outbox rows are not isolated to the new generation")
	}
	outbox := f.readRetryOutbox(doc.ID)
	if outbox.sequence != 1 || outbox.kind != "delivery.dispatch" || outbox.status != "pending" ||
		outbox.attempts != 0 || !strings.Contains(outbox.payload, doc.ID) {
		t.Fatalf("retry outbox = %+v", outbox)
	}
	if bytes.Contains([]byte(outbox.payload+outbox.headers), []byte("ENDPOINT-URL-SECRET")) {
		t.Fatal("retry outbox exposed destination material")
	}
	var destinationCount int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT destination_count FROM events WHERE id = $1::uuid`, eventID,
	).Scan(&destinationCount); err != nil || destinationCount != 1 {
		t.Fatalf("event destination_count = %d, err %v", destinationCount, err)
	}

	audits := f.auditRows("delivery.retried")
	if len(audits) != 1 || audits[0].TargetID == nil || *audits[0].TargetID != doc.ID ||
		audits[0].ActorID == nil || *audits[0].ActorID != keyID {
		t.Fatalf("retry audit = %+v", audits)
	}
	if len(audits[0].Metadata) != 4 || audits[0].Metadata["source_delivery_id"] != sourceID ||
		audits[0].Metadata["new_delivery_id"] != doc.ID || audits[0].Metadata["endpoint_id"] != endpoint.ID ||
		audits[0].Metadata["generation"] != float64(2) {
		t.Fatalf("retry audit metadata = %+v", audits[0].Metadata)
	}

	eventReply := f.do(http.MethodGet, "/v1/events/"+eventID, history, "")
	wantStatus(t, eventReply, http.StatusOK, "retry event history")
	event := decodeMgmt[historyEventDoc](t, eventReply)
	if event.DestinationCount != 1 || event.DeliverySummary.Total != 2 ||
		event.DeliverySummary.InitialTotal != 1 || event.DeliverySummary.RetryTotal != 1 {
		t.Fatalf("event history summary = %+v", event.DeliverySummary)
	}
	if len(event.Deliveries) != 2 || event.Deliveries[0].ID != doc.ID ||
		event.Deliveries[0].Generation != 2 || event.Deliveries[0].ParentDeliveryID == nil ||
		*event.Deliveries[0].ParentDeliveryID != sourceID ||
		event.Deliveries[1].ID != sourceID || event.Deliveries[1].Generation != 1 ||
		event.Deliveries[1].ParentDeliveryID != nil {
		t.Fatalf("event history deliveries = %+v", event.Deliveries)
	}
	newAttempts := f.do(http.MethodGet, "/v1/deliveries/"+doc.ID+"/attempts", history, "")
	wantStatus(t, newAttempts, http.StatusOK, "new generation attempts")
	if !bytes.Contains(newAttempts.body, []byte(`"attempts":[]`)) {
		t.Fatalf("new generation attempts = %s", newAttempts.body)
	}
}

type retryOutboxRow struct {
	sequence int
	kind     string
	status   string
	attempts int
	payload  string
	headers  string
}

func (f *mgmtFixture) readRetryOutbox(deliveryID string) retryOutboxRow {
	f.t.Helper()
	var row retryOutboxRow
	err := f.pool.QueryRow(context.Background(),
		`SELECT sequence, kind, status, attempts, payload::text, headers::text
		   FROM outbox_messages WHERE organization_id = $1::uuid AND delivery_id = $2::uuid`,
		f.orgID, deliveryID,
	).Scan(&row.sequence, &row.kind, &row.status, &row.attempts, &row.payload, &row.headers)
	if err != nil {
		f.t.Fatalf("read retry outbox: %v", err)
	}
	return row
}

func ptrTime(value time.Time) *time.Time { return &value }

func TestManualDeliveryRetryIdempotencyAndConcurrency(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token, _ := f.retryKey("endpoints:write", "deliveries:retry")
	endpoint := f.createRetryEndpoint(token, "idempotent-endpoint")
	eventID := mustID(t)
	f.seedRetryEvent(eventID, "order.created", time.Now().UTC().Add(24*time.Hour))
	sourceID := f.seedRetrySource(eventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)

	first := decodeMgmt[retryDeliveryDoc](t, f.retryDelivery(token, sourceID, "same-key"))
	replayReply := f.retryDelivery(token, sourceID, "same-key")
	wantStatus(t, replayReply, http.StatusOK, "idempotent retry replay")
	if replayReply.header.Get("HookFlow-Idempotent-Replay") != "true" {
		t.Fatalf("replay header = %q", replayReply.header.Get("HookFlow-Idempotent-Replay"))
	}
	replay := decodeMgmt[retryDeliveryDoc](t, replayReply)
	if replay.ID != first.ID || replay.Generation != 2 {
		t.Fatalf("replay = %+v, first = %+v", replay, first)
	}
	if f.countDeliveries() != 2 || f.auditCount("delivery.retried") != 1 {
		t.Fatal("idempotent replay created a second generation or audit row")
	}

	otherEventID := mustID(t)
	f.seedRetryEvent(otherEventID, "order.updated", time.Now().UTC().Add(24*time.Hour))
	otherSource := f.seedRetrySource(otherEventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)
	wantProblem(t, f.retryDelivery(token, otherSource, "same-key"),
		http.StatusConflict, "idempotency_key_conflict", "")

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE delivery_retry_requests SET request_hash = $2
		   WHERE organization_id = $1::uuid AND idempotency_key = 'same-key'`,
		f.orgID, bytes.Repeat([]byte{0x11}, 32)); err != nil {
		t.Fatalf("tamper retry request hash: %v", err)
	}
	wantProblem(t, f.retryDelivery(token, sourceID, "same-key"),
		http.StatusConflict, "idempotency_key_conflict", "")

	var wg sync.WaitGroup
	statuses := make(chan int, 2)
	ids := make(chan string, 2)
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			reply, err := f.roundTrip(http.MethodPost, f.retryPath(sourceID), token, "", "Idempotency-Key", "concurrent-key")
			if err != nil {
				t.Errorf("concurrent retry: %v", err)
				return
			}
			statuses <- reply.status
			var doc retryDeliveryDoc
			if err := json.Unmarshal(reply.body, &doc); err == nil {
				ids <- doc.ID
			}
		}()
	}
	wg.Wait()
	close(statuses)
	close(ids)
	gotStatuses := make([]int, 0, 2)
	for status := range statuses {
		gotStatuses = append(gotStatuses, status)
	}
	gotIDs := make(map[string]struct{})
	for id := range ids {
		gotIDs[id] = struct{}{}
	}
	if len(gotStatuses) != 2 || len(gotIDs) != 1 ||
		!((gotStatuses[0] == http.StatusCreated && gotStatuses[1] == http.StatusOK) ||
			(gotStatuses[0] == http.StatusOK && gotStatuses[1] == http.StatusCreated)) {
		t.Fatalf("concurrent same-key statuses = %v ids = %v", gotStatuses, gotIDs)
	}
	if f.auditCount("delivery.retried") != 2 || f.countDeliveries() != 4 {
		t.Fatal("concurrent replay created extra work")
	}

	// Two different keys for the same source must both create distinct,
	// monotonically increasing generations with the same parent.
	var concurrent [2]retryDeliveryDoc
	var wg2 sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg2.Add(1)
		go func(index int) {
			defer wg2.Done()
			reply, err := f.roundTrip(http.MethodPost, f.retryPath(sourceID), token, "",
				"Idempotency-Key", fmt.Sprintf("distinct-key-%d", index))
			if err != nil {
				t.Errorf("distinct retry: %v", err)
				return
			}
			if reply.status != http.StatusCreated {
				t.Errorf("distinct retry status = %d %s", reply.status, reply.body)
				return
			}
			if err := json.Unmarshal(reply.body, &concurrent[index]); err != nil {
				t.Errorf("decode distinct retry: %v", err)
			}
		}(i)
	}
	wg2.Wait()
	if concurrent[0].ID == concurrent[1].ID || concurrent[0].Generation == concurrent[1].Generation ||
		(concurrent[0].Generation != 4 && concurrent[0].Generation != 5) ||
		(concurrent[1].Generation != 4 && concurrent[1].Generation != 5) {
		t.Fatalf("distinct concurrent retries = %+v", concurrent)
	}
	if concurrent[0].ParentDeliveryID == nil || *concurrent[0].ParentDeliveryID != sourceID ||
		concurrent[1].ParentDeliveryID == nil || *concurrent[1].ParentDeliveryID != sourceID {
		t.Fatalf("distinct concurrent parents = %+v", concurrent)
	}
	if f.countDeliveries() != 6 || f.auditCount("delivery.retried") != 4 {
		t.Fatal("distinct concurrent retries did not create exactly two generations")
	}
}

func TestManualDeliveryRetryRejectsIneligibleRequests(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token, _ := f.retryKey("endpoints:write", "deliveries:retry")
	readOnly, _ := f.createKey("retry-read-only", "events:write")
	endpoint := f.createRetryEndpoint(token, "reject-endpoint")
	eventID := mustID(t)
	f.seedRetryEvent(eventID, "order.created", time.Now().UTC().Add(24*time.Hour))
	failed := f.seedRetrySource(eventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)
	baseline := f.countDeliveries()

	for _, path := range []string{f.retryPath(failed), f.retryPath(mustID(t)), f.retryPath("not-a-uuid")} {
		if reply := f.do(http.MethodPost, path, "", "", "Idempotency-Key", "anonymous"); reply.status != http.StatusUnauthorized {
			t.Fatalf("anonymous retry status = %d", reply.status)
		}
	}
	wantProblem(t, f.retryDelivery(readOnly, failed, "scope-key"),
		http.StatusForbidden, "insufficient_scope", "")
	wantProblem(t, f.do(http.MethodPost, f.retryPath(failed), token, ""),
		http.StatusBadRequest, "missing_idempotency_key", "")
	wantProblem(t, f.retryDuplicateKey(token, failed),
		http.StatusBadRequest, "invalid_idempotency_key", "")
	wantProblem(t, f.do(http.MethodGet, f.retryPath(failed), token, ""),
		http.StatusMethodNotAllowed, "method_not_allowed", "")

	pendingEventID := mustID(t)
	f.seedRetryEvent(pendingEventID, "order.pending", time.Now().UTC().Add(24*time.Hour))
	pending := f.seedRetrySource(pendingEventID, endpoint.ID, "pending", nil, nil)
	succeededEventID := mustID(t)
	f.seedRetryEvent(succeededEventID, "order.succeeded", time.Now().UTC().Add(24*time.Hour))
	succeeded := f.seedRetrySource(succeededEventID, endpoint.ID, "succeeded", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)
	leasedEventID := mustID(t)
	f.seedRetryEvent(leasedEventID, "order.leased", time.Now().UTC().Add(24*time.Hour))
	leased := f.seedRetrySource(leasedEventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), ptrTime(time.Now().UTC().Add(time.Hour)))
	expiredEventID := mustID(t)
	f.seedRetryEvent(expiredEventID, "order.expired", time.Now().UTC().Add(-time.Minute))
	expiredEventSource := f.seedRetrySource(expiredEventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)

	cases := []struct {
		name   string
		source string
		code   string
	}{
		{name: "pending", source: pending, code: "delivery_not_retryable"},
		{name: "succeeded", source: succeeded, code: "delivery_not_retryable"},
		{name: "active lease", source: leased, code: "delivery_leased"},
		{name: "expired event", source: expiredEventSource, code: "event_expired"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			wantProblem(t, f.retryDelivery(token, testCase.source, "reject-"+testCase.name),
				http.StatusConflict, testCase.code, "")
		})
	}
	if f.countDeliveries() != baseline+4 {
		t.Fatalf("rejected requests created deliveries: baseline %d now %d", baseline, f.countDeliveries())
	}
	if f.auditCount("delivery.retried") != 0 {
		t.Fatal("rejected requests wrote retry audit rows")
	}

	if _, err := f.pool.Exec(context.Background(),
		`UPDATE endpoints SET status = 'paused' WHERE id = $1::uuid`, endpoint.ID); err != nil {
		t.Fatalf("pause endpoint: %v", err)
	}
	wantProblem(t, f.retryDelivery(token, failed, "paused-key"),
		http.StatusConflict, "endpoint_ineligible", "")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE endpoints SET status = 'active', deleted_at = now() WHERE id = $1::uuid`, endpoint.ID); err != nil {
		t.Fatalf("soft-delete endpoint: %v", err)
	}
	wantProblem(t, f.retryDelivery(token, failed, "deleted-key"),
		http.StatusConflict, "endpoint_ineligible", "")
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE endpoints SET deleted_at = NULL, status = 'active' WHERE id = $1::uuid`, endpoint.ID); err != nil {
		t.Fatalf("restore endpoint: %v", err)
	}

	secondOrg := mustID(t)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		secondOrg, "retry-second-"+secondOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	f.cleanupSecondOrg(secondOrg)
	secondToken := f.createKeyFor(secondOrg, "retry-cross-tenant", "deliveries:retry")
	wantProblem(t, f.retryDelivery(secondToken, failed, "cross-tenant"),
		http.StatusNotFound, "not_found", "")

	for _, status := range []string{"suspended", "archived"} {
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE organizations SET status = $2 WHERE id = $1::uuid`, f.orgID, status); err != nil {
			t.Fatalf("set organization status: %v", err)
		}
		wantProblem(t, f.retryDelivery(token, failed, "org-"+status),
			http.StatusForbidden, "organization_"+status, "")
	}
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE organizations SET status = 'active' WHERE id = $1::uuid`, f.orgID); err != nil {
		t.Fatalf("restore organization: %v", err)
	}
}

func TestManualDeliveryRetryGenerationChainAndWorkerEligibility(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token, _ := f.retryKey("endpoints:write", "deliveries:retry")
	history, _ := f.createKey("retry-chain-history", "deliveries:read")
	endpoint := f.createRetryEndpoint(token, "chain-endpoint")
	eventID := mustID(t)
	f.seedRetryEvent(eventID, "order.created", time.Now().UTC().Add(24*time.Hour))
	sourceID := f.seedRetrySource(eventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)

	gen2 := decodeMgmt[retryDeliveryDoc](t, f.retryDelivery(token, sourceID, "chain-key-2"))
	if gen2.Generation != 2 {
		t.Fatalf("generation 2 = %+v", gen2)
	}
	if f.countOutboxFor(gen2.ID) != 1 {
		t.Fatal("generation 2 lacks its initial outbox row")
	}

	// The new generation is visible to the existing worker acquisition query with
	// its own attempt counter and dispatch sequence.
	claim, err := acquireDeliveryForTest(f, gen2.ID)
	if err != nil {
		t.Fatalf("acquire retry generation: %v", err)
	}
	if claim == nil || claim.DeliveryID != gen2.ID || claim.AttemptCount != 0 || claim.DispatchSeq != 1 {
		t.Fatalf("worker claim = %+v", claim)
	}
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE deliveries SET state = 'failed', completed_at = now(), next_attempt_at = NULL,
		        dispatch_lease_token = NULL, dispatch_lease_expires_at = NULL, updated_at = now()
		   WHERE id = $1::uuid AND organization_id = $2::uuid`, gen2.ID, f.orgID,
	); err != nil {
		t.Fatalf("complete generation 2 as failed: %v", err)
	}

	gen3 := decodeMgmt[retryDeliveryDoc](t, f.retryDelivery(token, gen2.ID, "chain-key-3"))
	if gen3.Generation != 3 || gen3.ParentDeliveryID == nil || *gen3.ParentDeliveryID != gen2.ID {
		t.Fatalf("generation 3 = %+v", gen3)
	}
	event := decodeMgmt[historyEventDoc](t,
		f.do(http.MethodGet, "/v1/events/"+eventID, history, ""))
	if event.DeliverySummary.Total != 3 || event.DeliverySummary.InitialTotal != 1 ||
		event.DeliverySummary.RetryTotal != 2 {
		t.Fatalf("chain summary = %+v", event.DeliverySummary)
	}
	if len(event.Deliveries) != 3 || event.Deliveries[0].ID != gen3.ID ||
		event.Deliveries[0].Generation != 3 || event.Deliveries[1].ID != gen2.ID ||
		event.Deliveries[1].Generation != 2 || event.Deliveries[2].ID != sourceID {
		t.Fatalf("chain order = %+v", event.Deliveries)
	}
}

func TestRetryGenerationSchemaConstraints(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token, _ := f.retryKey("endpoints:write")
	endpoint := f.createRetryEndpoint(token, "schema-endpoint")
	eventID := mustID(t)
	f.seedRetryEvent(eventID, "order.created", time.Now().UTC().Add(24*time.Hour))
	sourceID := f.seedRetrySource(eventID, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)

	ctx := context.Background()
	check := func(name, statement string, args ...any) {
		t.Helper()
		if _, err := f.pool.Exec(ctx, statement, args...); err == nil {
			t.Fatalf("%s unexpectedly succeeded", name)
		}
	}
	insertDelivery := `INSERT INTO deliveries
		(id, organization_id, event_id, endpoint_id, url, secret_encrypted, state, dispatch_seq,
		 next_attempt_at, completed_at, generation, parent_delivery_id)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'https://receiver.invalid/hook', '',
		        'succeeded', 1, NULL, now(), $5, $6::uuid)`

	check("duplicate generation", insertDelivery,
		mustID(t), f.orgID, eventID, endpoint.ID, 1, nil)
	check("generation one parent", insertDelivery,
		mustID(t), f.orgID, eventID, endpoint.ID, 1, sourceID)
	check("retry generation without parent", insertDelivery,
		mustID(t), f.orgID, eventID, endpoint.ID, 2, nil)
	check("zero generation", insertDelivery,
		mustID(t), f.orgID, eventID, endpoint.ID, 0, nil)

	// A valid generation two provides the new-delivery FK target for the
	// retry-request hash length check.
	generationTwo := mustID(t)
	f.seedDelivery(f.orgID, seedDelivery{
		ID: generationTwo, EventID: eventID, EndpointID: endpoint.ID,
		State: "succeeded", CreatedAt: time.Now().UTC(), AttemptCount: 0,
		CompletedAt: ptrTime(time.Now().UTC()), Generation: 2, ParentID: &sourceID,
	})
	check("retry request bad hash", `INSERT INTO delivery_retry_requests
		(id, organization_id, idempotency_key, request_hash, source_delivery_id, new_delivery_id,
		 created_at, expires_at)
		VALUES ($1::uuid, $2::uuid, 'schema-key', $3, $4::uuid, $5::uuid, now(), now() + interval '72 hours')`,
		mustID(t), f.orgID, []byte{0x01}, sourceID, generationTwo)
	check("retry request non-positive window", `INSERT INTO delivery_retry_requests
		(id, organization_id, idempotency_key, request_hash, source_delivery_id, new_delivery_id,
		 created_at, expires_at)
		VALUES ($1::uuid, $2::uuid, 'schema-window', $3, $4::uuid, $5::uuid, now(), now())`,
		mustID(t), f.orgID, bytes.Repeat([]byte{0x22}, 32), sourceID, generationTwo)
}

func TestManualDeliveryRetryExpiredRequestTakeover(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token, _ := f.retryKey("deliveries:retry", "endpoints:write")
	endpoint := f.createRetryEndpoint(token, "expiry-endpoint")
	firstEvent := mustID(t)
	f.seedRetryEvent(firstEvent, "order.created", time.Now().UTC().Add(24*time.Hour))
	secondEvent := mustID(t)
	f.seedRetryEvent(secondEvent, "order.created", time.Now().UTC().Add(24*time.Hour))
	firstSource := f.seedRetrySource(firstEvent, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)
	secondSource := f.seedRetrySource(secondEvent, endpoint.ID, "failed", ptrTime(time.Now().UTC().Add(-time.Hour)), nil)

	created := decodeMgmt[retryDeliveryDoc](t, f.retryDelivery(token, firstSource, "expiring-key"))
	if created.Generation != 2 {
		t.Fatalf("first expiring retry = %+v", created)
	}
	if _, err := f.pool.Exec(context.Background(),
		`UPDATE delivery_retry_requests
		    SET created_at = now() - interval '73 hours', expires_at = now() - interval '1 hour'
		   WHERE organization_id = $1::uuid AND idempotency_key = 'expiring-key'`, f.orgID,
	); err != nil {
		t.Fatalf("expire retry request: %v", err)
	}

	reused := decodeMgmt[retryDeliveryDoc](t, f.retryDelivery(token, secondSource, "expiring-key"))
	if reused.ID == created.ID || reused.EventID != secondEvent || reused.Generation != 2 ||
		reused.ParentDeliveryID == nil || *reused.ParentDeliveryID != secondSource {
		t.Fatalf("expired key reuse = %+v", reused)
	}
	if f.countDeliveries() != 4 || f.auditCount("delivery.retried") != 2 {
		t.Fatal("expired key takeover did not create exactly one new generation")
	}
}

func acquireDeliveryForTest(f *mgmtFixture, wantID string) (*delivery.Claim, error) {
	store, err := delivery.NewStore(f.pool, delivery.Options{
		NewID: id.NewV7, BuildRetryMessage: delivery.BuildRetryMessage, Logger: mgmtLogger(),
	})
	if err != nil {
		return nil, fmt.Errorf("create delivery store: %w", err)
	}
	claim, err := store.Acquire(context.Background(), delivery.AcquireOptions{
		LeaseDuration: time.Minute, MaxAttempts: 5, NewToken: delivery.RandomToken,
	})
	if err != nil {
		return nil, err
	}
	if claim != nil && claim.DeliveryID != wantID {
		return nil, fmt.Errorf("acquired delivery %s, want retry generation %s", claim.DeliveryID, wantID)
	}
	return claim, nil
}
