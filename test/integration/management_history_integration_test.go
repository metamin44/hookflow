//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"
)

var historyBase = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

type historyEventDoc struct {
	ID               string          `json:"id"`
	Type             string          `json:"type"`
	ReceivedAt       time.Time       `json:"received_at"`
	CreatedAt        time.Time       `json:"created_at"`
	Payload          json.RawMessage `json:"payload"`
	DestinationCount int             `json:"destination_count"`
	DeliverySummary  struct {
		Total     int `json:"total"`
		Pending   int `json:"pending"`
		Retrying  int `json:"retrying"`
		Succeeded int `json:"succeeded"`
		Failed    int `json:"failed"`
		Expired   int `json:"expired"`
	} `json:"delivery_summary"`
	Deliveries          []historyDeliveryDoc `json:"deliveries"`
	DeliveriesTruncated bool                 `json:"deliveries_truncated"`
}

type historyDeliveryDoc struct {
	ID                 string     `json:"id"`
	EventID            string     `json:"event_id"`
	EndpointID         string     `json:"endpoint_id"`
	State              string     `json:"state"`
	AttemptCount       int        `json:"attempt_count"`
	InProgress         bool       `json:"in_progress"`
	CreatedAt          time.Time  `json:"created_at"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
	LastAttemptAt      *time.Time `json:"last_attempt_at"`
	CompletedAt        *time.Time `json:"completed_at"`
	LastResponseStatus *int       `json:"last_response_status"`
	LeaseExpiresAt     *time.Time `json:"lease_expires_at"`
	Diagnostic         string     `json:"diagnostic"`
	EventType          string     `json:"event_type"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

type historyAttemptDoc struct {
	AttemptNumber  int       `json:"attempt_number"`
	Outcome        string    `json:"outcome"`
	ResponseStatus *int      `json:"response_status"`
	ErrorClass     *string   `json:"error_class"`
	ErrorMessage   string    `json:"error_message"`
	DurationMS     *int      `json:"duration_ms"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
}

type historyEventsPage struct {
	Events     []historyEventDoc `json:"events"`
	NextCursor string            `json:"next_cursor"`
}

type historyDeliveriesPage struct {
	Deliveries []historyDeliveryDoc `json:"deliveries"`
	NextCursor string               `json:"next_cursor"`
}

type historyAttemptsPage struct {
	Attempts   []historyAttemptDoc `json:"attempts"`
	NextCursor string              `json:"next_cursor"`
}

func (f *mgmtFixture) historyToken() string {
	f.t.Helper()
	token, _ := f.createKey("history-read", "deliveries:read")
	return token
}

func (f *mgmtFixture) seedEndpoint(organizationID, endpointID, name string) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO endpoints (id, organization_id, name, url, secret_encrypted, status)
		 VALUES ($1::uuid, $2::uuid, $3, 'https://receiver.invalid/hook', '', 'active')`,
		endpointID, organizationID, name,
	); err != nil {
		f.t.Fatalf("seed endpoint: %v", err)
	}
}

func (f *mgmtFixture) seedEvent(organizationID, eventID, eventType string, received time.Time, payload string, destinationCount int) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO events
		     (id, organization_id, type, payload, destination_count, headers,
		      received_at, expires_at, created_at, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3, $4::jsonb, $5, $6::jsonb, $7::timestamptz,
		         $7::timestamptz + interval '90 days', $7::timestamptz, $7::timestamptz)`,
		eventID, organizationID, eventType, payload, destinationCount,
		`{"authorization":"Bearer EVENT-HEADER-SECRET","x-request-id":"seed"}`, received,
	); err != nil {
		f.t.Fatalf("seed event: %v", err)
	}
}

type seedDelivery struct {
	ID             string
	EventID        string
	EndpointID     string
	State          string
	CreatedAt      time.Time
	AttemptCount   int
	NextAttemptAt  *time.Time
	LastAttemptAt  *time.Time
	CompletedAt    *time.Time
	LastStatus     *int
	LastError      *string
	LeaseExpiresAt *time.Time
}

func (f *mgmtFixture) seedDelivery(organizationID string, spec seedDelivery) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var leaseToken any
	if spec.LeaseExpiresAt != nil {
		leaseToken = spec.ID
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO deliveries
		     (id, organization_id, event_id, endpoint_id, url, secret_encrypted,
		      state, dispatch_seq, attempt_count, next_attempt_at, last_attempt_at,
		      last_response_status, last_error, created_at, updated_at, completed_at,
		      dispatch_lease_token, dispatch_lease_expires_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid,
		         'https://receiver.invalid/hook?token=DELIVERY-URL-SECRET', 'ENVELOPE-SECRET',
		         $5, 1, $6, $7, $8, $9, $10, $11, $11, $12, $13::uuid, $14)`,
		spec.ID, organizationID, spec.EventID, spec.EndpointID,
		spec.State, spec.AttemptCount, spec.NextAttemptAt, spec.LastAttemptAt,
		spec.LastStatus, spec.LastError, spec.CreatedAt, spec.CompletedAt,
		leaseToken, spec.LeaseExpiresAt,
	); err != nil {
		f.t.Fatalf("seed delivery: %v", err)
	}
}

type seedAttempt struct {
	DeliveryID string
	Number     int
	Outcome    string
	Status     *int
	ErrorClass *string
	Error      *string
	Duration   *int
	StartedAt  time.Time
}

func (f *mgmtFixture) seedAttempt(organizationID string, spec seedAttempt) {
	f.t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO delivery_attempts
		     (id, organization_id, delivery_id, attempt_number, request_method, request_url,
		      request_headers, response_status, response_headers, response_body_snippet,
		      outcome, error_class, error_message, duration_ms, started_at, finished_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4, 'POST',
		         'https://receiver.invalid/hook?token=URL-SECRET-TOKEN',
		         '{"authorization":"Bearer HEADER-SECRET"}', $5,
		         '{"set-cookie":"HEADER-SECRET"}', 'raw receiver body BODY-SNIPPET-SECRET',
		         $6, $7, $8, $9, $10::timestamptz, $10::timestamptz + interval '2 seconds')`,
		mustID(f.t), organizationID, spec.DeliveryID, spec.Number, spec.Status,
		spec.Outcome, spec.ErrorClass, spec.Error, spec.Duration, spec.StartedAt,
	); err != nil {
		f.t.Fatalf("seed attempt: %v", err)
	}
}

func (f *mgmtFixture) cleanupOrgRows(organizationID string) {
	f.t.Helper()
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		for _, statement := range []string{
			`DELETE FROM delivery_attempts WHERE organization_id = $1::uuid`,
			`DELETE FROM deliveries WHERE organization_id = $1::uuid`,
			`DELETE FROM events WHERE organization_id = $1::uuid`,
			`DELETE FROM endpoints WHERE organization_id = $1::uuid`,
		} {
			if _, err := f.pool.Exec(ctx, statement, organizationID); err != nil {
				f.t.Errorf("cleanup organization rows: %v", err)
			}
		}
	})
}

func (f *mgmtFixture) cleanupSecondOrg(organizationID string) {
	f.t.Helper()
	f.t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		for _, statement := range []string{
			`DELETE FROM delivery_attempts WHERE organization_id = $1::uuid`,
			`DELETE FROM deliveries WHERE organization_id = $1::uuid`,
			`DELETE FROM events WHERE organization_id = $1::uuid`,
			`DELETE FROM endpoints WHERE organization_id = $1::uuid`,
			`DELETE FROM organizations WHERE id = $1::uuid`,
		} {
			if _, err := f.pool.Exec(ctx, statement, organizationID); err != nil {
				f.t.Errorf("cleanup second organization: %v", err)
			}
		}
	})
}

func TestDeliveryHistoryAuthorizationAndTenancy(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	historyToken := f.historyToken()
	eventsToken, _ := f.createKey("events-write", "events:write")

	for _, path := range []string{"/v1/events", "/v1/deliveries"} {
		if reply := f.do(http.MethodGet, path, "", ""); reply.status != http.StatusUnauthorized {
			t.Fatalf("anonymous GET %s = %d", path, reply.status)
		}
		wantProblem(t, f.do(http.MethodGet, path, eventsToken, ""),
			http.StatusForbidden, "insufficient_scope", "")
	}

	endpointID := mustID(t)
	f.seedEndpoint(f.orgID, endpointID, "history-endpoint")
	eventID := mustID(t)
	f.seedEvent(f.orgID, eventID, "order.created", historyBase, `{"ok":true}`, 1)
	deliveryID := mustID(t)
	next := historyBase.Add(time.Minute)
	f.seedDelivery(f.orgID, seedDelivery{
		ID: deliveryID, EventID: eventID, EndpointID: endpointID,
		State: "pending", CreatedAt: historyBase, AttemptCount: 0, NextAttemptAt: &next,
	})
	f.seedAttempt(f.orgID, seedAttempt{DeliveryID: deliveryID, Number: 1, Outcome: "retryable_failure",
		StartedAt: historyBase})

	detailPath := "/v1/events/" + eventID
	deliveryPath := "/v1/deliveries/" + deliveryID
	attemptPath := deliveryPath + "/attempts"
	for _, path := range []string{"/v1/events", detailPath, "/v1/deliveries", deliveryPath, attemptPath} {
		wantProblem(t, f.do(http.MethodGet, path, eventsToken, ""),
			http.StatusForbidden, "insufficient_scope", "")
	}

	var auditBefore int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE organization_id = $1::uuid`, f.orgID).Scan(&auditBefore); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}

	for _, path := range []string{"/v1/events", detailPath, "/v1/deliveries", deliveryPath, attemptPath} {
		reply := f.do(http.MethodGet, path, historyToken, "")
		wantStatus(t, reply, http.StatusOK, "read scope GET "+path)
		if cache := reply.header.Get("Cache-Control"); cache != "no-store" {
			t.Fatalf("%s Cache-Control = %q", path, cache)
		}
	}
	var auditAfter int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM audit_logs WHERE organization_id = $1::uuid`, f.orgID).Scan(&auditAfter); err != nil {
		t.Fatalf("count audit rows: %v", err)
	}
	if auditAfter != auditBefore {
		t.Fatalf("read routes wrote %d audit rows", auditAfter-auditBefore)
	}

	otherOrg := mustID(t)
	f.cleanupSecondOrg(otherOrg)
	if _, err := f.pool.Exec(context.Background(),
		`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
		otherOrg, "history-other-"+otherOrg); err != nil {
		t.Fatalf("seed second organization: %v", err)
	}
	otherToken := f.createKeyFor(otherOrg, "history-other", "deliveries:read")
	otherEndpoint := mustID(t)
	otherEvent := mustID(t)
	otherDelivery := mustID(t)
	f.seedEndpoint(otherOrg, otherEndpoint, "other-endpoint")
	f.seedEvent(otherOrg, otherEvent, "other.event", historyBase.Add(time.Hour), `{"other":true}`, 1)
	f.seedDelivery(otherOrg, seedDelivery{
		ID: otherDelivery, EventID: otherEvent, EndpointID: otherEndpoint,
		State: "expired", CreatedAt: historyBase, CompletedAt: ptr(historyBase.Add(time.Minute)),
	})

	for _, path := range []string{
		"/v1/events/" + otherEvent,
		"/v1/deliveries/" + otherDelivery,
		"/v1/deliveries/" + otherDelivery + "/attempts",
	} {
		wantProblem(t, f.do(http.MethodGet, path, historyToken, ""),
			http.StatusNotFound, "not_found", "")
		wantStatus(t, f.do(http.MethodGet, path, otherToken, ""), http.StatusOK, "owner access "+path)
	}

	var ownEvents historyEventsPage
	reply := f.do(http.MethodGet, "/v1/events", historyToken, "")
	wantStatus(t, reply, http.StatusOK, "own events")
	ownEvents = decodeMgmt[historyEventsPage](t, reply)
	if len(ownEvents.Events) != 1 || ownEvents.Events[0].ID != eventID {
		t.Fatalf("own event list = %+v", ownEvents.Events)
	}
	var otherEvents historyEventsPage
	reply = f.do(http.MethodGet, "/v1/events", otherToken, "")
	wantStatus(t, reply, http.StatusOK, "other events")
	otherEvents = decodeMgmt[historyEventsPage](t, reply)
	if len(otherEvents.Events) != 1 || otherEvents.Events[0].ID != otherEvent {
		t.Fatalf("other event list = %+v", otherEvents.Events)
	}

	wantProblem(t, f.do(http.MethodGet, "/v1/events/not-a-uuid", historyToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, "/v1/deliveries/"+mustID(t), historyToken, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, "/v1/deliveries/"+mustID(t)+"/attempts", historyToken, ""),
		http.StatusNotFound, "not_found", "")

	method := f.do(http.MethodPost, "/v1/deliveries", historyToken, "")
	wantStatus(t, method, http.StatusMethodNotAllowed, "delivery method restriction")
	if allow := method.header.Get("Allow"); allow != "GET" {
		t.Fatalf("delivery Allow = %q", allow)
	}
	method = f.do(http.MethodPost, "/v1/deliveries/"+deliveryID+"/attempts", historyToken, "")
	wantStatus(t, method, http.StatusMethodNotAllowed, "attempt method restriction")
	if allow := method.header.Get("Allow"); allow != "GET" {
		t.Fatalf("attempt Allow = %q", allow)
	}
}

func TestDeliveryHistoryEventExposure(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token := f.historyToken()

	endpointOne := mustID(t)
	endpointTwo := mustID(t)
	f.seedEndpoint(f.orgID, endpointOne, "endpoint-one")
	f.seedEndpoint(f.orgID, endpointTwo, "endpoint-two")

	eventOne := mustID(t)
	eventTwo := mustID(t)
	eventThree := mustID(t)
	f.seedEvent(f.orgID, eventOne, "order.created", historyBase.Add(30*time.Second),
		`{"marker":"EVENT-PAYLOAD-MARKER","amount":10}`, 2)
	f.seedEvent(f.orgID, eventTwo, "order.paid", historyBase.Add(10*time.Second), `{"paid":true}`, 0)
	f.seedEvent(f.orgID, eventThree, "user.updated", historyBase.Add(20*time.Second), `{"user":"u1"}`, 0)

	next := historyBase.Add(10 * time.Minute)
	lease := historyBase.Add(5 * time.Minute)
	succeededAt := historyBase.Add(40 * time.Second)
	status := http.StatusOK
	f.seedDelivery(f.orgID, seedDelivery{
		ID: mustID(t), EventID: eventOne, EndpointID: endpointOne, State: "succeeded",
		CreatedAt: historyBase.Add(31 * time.Second), AttemptCount: 1,
		CompletedAt: &succeededAt, LastStatus: &status,
	})
	f.seedDelivery(f.orgID, seedDelivery{
		ID: mustID(t), EventID: eventOne, EndpointID: endpointTwo, State: "pending",
		CreatedAt: historyBase.Add(32 * time.Second), NextAttemptAt: &next, LeaseExpiresAt: &lease,
	})

	var page historyEventsPage
	reply := f.do(http.MethodGet, "/v1/events", token, "")
	wantStatus(t, reply, http.StatusOK, "event list")
	page = decodeMgmt[historyEventsPage](t, reply)
	if len(page.Events) != 3 {
		t.Fatalf("event count = %d", len(page.Events))
	}
	if page.Events[0].ID != eventOne || page.Events[1].ID != eventThree || page.Events[2].ID != eventTwo {
		t.Fatalf("event ordering = %v", []string{page.Events[0].ID, page.Events[1].ID, page.Events[2].ID})
	}
	summary := page.Events[0].DeliverySummary
	if summary.Total != 2 || summary.Succeeded != 1 || summary.Pending != 1 || summary.Failed != 0 {
		t.Fatalf("delivery summary = %+v", summary)
	}
	if bytes.Contains(reply.body, []byte("EVENT-PAYLOAD-MARKER")) ||
		bytes.Contains(reply.body, []byte("EVENT-HEADER-SECRET")) ||
		bytes.Contains(reply.body, []byte(`"payload"`)) {
		t.Fatal("event list exposed a payload or header secret")
	}

	detail := decodeMgmt[historyEventDoc](t, f.do(http.MethodGet, "/v1/events/"+eventOne, token, ""))
	if detail.Payload == nil || !strings.Contains(string(detail.Payload), "EVENT-PAYLOAD-MARKER") {
		t.Fatalf("event detail payload = %v", detail.Payload)
	}
	if len(detail.Deliveries) != 2 {
		t.Fatalf("event detail deliveries = %d", len(detail.Deliveries))
	}
	if detail.DestinationCount != 2 || detail.DeliverySummary.Total != 2 {
		t.Fatalf("event detail counts = %d/%+v", detail.DestinationCount, detail.DeliverySummary)
	}
	detailReply := f.do(http.MethodGet, "/v1/events/"+eventOne, token, "")
	if bytes.Contains(detailReply.body, []byte("EVENT-HEADER-SECRET")) ||
		bytes.Contains(detailReply.body, []byte("DELIVERY-URL-SECRET")) ||
		bytes.Contains(detailReply.body, []byte("ENVELOPE-SECRET")) {
		t.Fatal("event detail exposed a header, URL or envelope secret")
	}

	emptyDetail := decodeMgmt[historyEventDoc](t, f.do(http.MethodGet, "/v1/events/"+eventTwo, token, ""))
	if emptyDetail.Deliveries == nil || len(emptyDetail.Deliveries) != 0 {
		t.Fatalf("zero-delivery event detail = %+v", emptyDetail.Deliveries)
	}
	if emptyDetail.DeliverySummary.Total != 0 {
		t.Fatalf("zero-delivery summary = %+v", emptyDetail.DeliverySummary)
	}

	filtered := decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?type=order.paid", token, ""))
	if len(filtered.Events) != 1 || filtered.Events[0].ID != eventTwo {
		t.Fatalf("type filter = %+v", filtered.Events)
	}
	filtered = decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?endpoint_id="+endpointTwo, token, ""))
	if len(filtered.Events) != 1 || filtered.Events[0].ID != eventOne {
		t.Fatalf("endpoint filter = %+v", filtered.Events)
	}
	filtered = decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?received_from=2026-01-01T00:00:15Z", token, ""))
	if len(filtered.Events) != 2 || filtered.Events[0].ID != eventOne || filtered.Events[1].ID != eventThree {
		t.Fatalf("received_from filter = %+v", filtered.Events)
	}
	filtered = decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?received_to=2026-01-01T00:00:30Z", token, ""))
	if len(filtered.Events) != 2 || filtered.Events[0].ID != eventThree || filtered.Events[1].ID != eventTwo {
		t.Fatalf("received_to filter = %+v", filtered.Events)
	}
	empty := f.do(http.MethodGet, "/v1/events?type=missing.event", token, "")
	wantStatus(t, empty, http.StatusOK, "empty filter")
	if !bytes.Contains(empty.body, []byte(`"events":[]`)) {
		t.Fatalf("empty page = %s", empty.body)
	}
}

func TestDeliveryHistoryDeliveryStatesAndAttempts(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token := f.historyToken()

	pendingEndpoint := mustID(t)
	f.seedEndpoint(f.orgID, pendingEndpoint, "states-pending")
	retryingEndpoint := mustID(t)
	f.seedEndpoint(f.orgID, retryingEndpoint, "states-retrying")
	succeededEndpoint := mustID(t)
	f.seedEndpoint(f.orgID, succeededEndpoint, "states-succeeded")
	failedEndpoint := mustID(t)
	f.seedEndpoint(f.orgID, failedEndpoint, "states-failed")
	expiredEndpoint := mustID(t)
	f.seedEndpoint(f.orgID, expiredEndpoint, "states-expired")
	eventID := mustID(t)
	f.seedEvent(f.orgID, eventID, "order.created", historyBase, `{"state":true}`, 5)

	next := historyBase.Add(10 * time.Minute)
	lease := time.Now().Add(5 * time.Minute)
	completed := historyBase.Add(2 * time.Minute)
	okStatus := http.StatusOK
	failedStatus := http.StatusBadGateway
	lastError := "upstream rejected Authorization: Bearer LAST-ERROR-SECRET-123"
	pendingID := mustID(t)
	retryingID := mustID(t)
	succeededID := mustID(t)
	failedID := mustID(t)
	expiredID := mustID(t)
	f.seedDelivery(f.orgID, seedDelivery{ID: pendingID, EventID: eventID, EndpointID: pendingEndpoint,
		State: "pending", CreatedAt: historyBase.Add(5 * time.Second), NextAttemptAt: &next,
		LeaseExpiresAt: &lease})
	f.seedDelivery(f.orgID, seedDelivery{ID: retryingID, EventID: eventID, EndpointID: retryingEndpoint,
		State: "retrying", CreatedAt: historyBase.Add(4 * time.Second), AttemptCount: 2,
		NextAttemptAt: &next})
	f.seedDelivery(f.orgID, seedDelivery{ID: succeededID, EventID: eventID, EndpointID: succeededEndpoint,
		State: "succeeded", CreatedAt: historyBase.Add(3 * time.Second), AttemptCount: 1,
		CompletedAt: &completed, LastStatus: &okStatus})
	f.seedDelivery(f.orgID, seedDelivery{ID: failedID, EventID: eventID, EndpointID: failedEndpoint,
		State: "failed", CreatedAt: historyBase.Add(2 * time.Second), AttemptCount: 3,
		CompletedAt: &completed, LastStatus: &failedStatus, LastError: &lastError})
	f.seedDelivery(f.orgID, seedDelivery{ID: expiredID, EventID: eventID, EndpointID: expiredEndpoint,
		State: "expired", CreatedAt: historyBase.Add(time.Second), CompletedAt: &completed})

	var page historyDeliveriesPage
	reply := f.do(http.MethodGet, "/v1/deliveries", token, "")
	wantStatus(t, reply, http.StatusOK, "delivery list")
	page = decodeMgmt[historyDeliveriesPage](t, reply)
	if len(page.Deliveries) != 5 {
		t.Fatalf("delivery count = %d", len(page.Deliveries))
	}
	order := []string{pendingID, retryingID, succeededID, failedID, expiredID}
	for i, doc := range page.Deliveries {
		if doc.ID != order[i] {
			t.Fatalf("delivery order[%d] = %s, want %s", i, doc.ID, order[i])
		}
	}
	if !page.Deliveries[0].InProgress || page.Deliveries[0].AttemptCount != 0 ||
		page.Deliveries[0].NextAttemptAt == nil {
		t.Fatalf("pending delivery = %+v", page.Deliveries[0])
	}
	if page.Deliveries[1].InProgress || page.Deliveries[1].AttemptCount != 2 {
		t.Fatalf("retrying delivery = %+v", page.Deliveries[1])
	}
	if page.Deliveries[2].InProgress || page.Deliveries[2].CompletedAt == nil ||
		page.Deliveries[2].State != "succeeded" {
		t.Fatalf("succeeded delivery = %+v", page.Deliveries[2])
	}
	if !strings.Contains(page.Deliveries[3].Diagnostic, "[REDACTED]") ||
		strings.Contains(page.Deliveries[3].Diagnostic, "LAST-ERROR-SECRET-123") {
		t.Fatalf("failed diagnostic = %q", page.Deliveries[3].Diagnostic)
	}
	if bytes.Contains(reply.body, []byte("DELIVERY-URL-SECRET")) ||
		bytes.Contains(reply.body, []byte("ENVELOPE-SECRET")) ||
		bytes.Contains(reply.body, []byte("LAST-ERROR-SECRET-123")) {
		t.Fatal("delivery list exposed a secret")
	}

	for _, state := range []string{"pending", "retrying", "succeeded", "failed", "expired"} {
		filtered := decodeMgmt[historyDeliveriesPage](t,
			f.do(http.MethodGet, "/v1/deliveries?state="+state, token, ""))
		if len(filtered.Deliveries) != 1 || filtered.Deliveries[0].State != state {
			t.Fatalf("state=%s filter = %+v", state, filtered.Deliveries)
		}
	}
	combined := decodeMgmt[historyDeliveriesPage](t,
		f.do(http.MethodGet, "/v1/deliveries?endpoint_id="+failedEndpoint+"&state=failed&created_from=2026-01-01T00:00:01Z&created_to=2026-01-01T00:00:03Z", token, ""))
	if len(combined.Deliveries) != 1 || combined.Deliveries[0].ID != failedID {
		t.Fatalf("combined filter = %+v", combined.Deliveries)
	}
	future := decodeMgmt[historyDeliveriesPage](t,
		f.do(http.MethodGet, "/v1/deliveries?created_from=2026-01-02T00:00:00Z", token, ""))
	if len(future.Deliveries) != 0 {
		t.Fatalf("future window = %+v", future.Deliveries)
	}

	detailReply := f.do(http.MethodGet, "/v1/deliveries/"+succeededID, token, "")
	wantStatus(t, detailReply, http.StatusOK, "delivery detail")
	detail := decodeMgmt[historyDeliveryDoc](t, detailReply)
	if detail.State != "succeeded" || detail.EventType != "order.created" || detail.CompletedAt == nil ||
		detail.LastResponseStatus == nil || *detail.LastResponseStatus != http.StatusOK {
		t.Fatalf("delivery detail = %+v", detail)
	}
	if bytes.Contains(detailReply.body, []byte("DELIVERY-URL-SECRET")) {
		t.Fatal("delivery detail exposed the destination URL")
	}

	password := "password=ATTEMPT-PASSWORD-SECRET"
	long := password + " " + strings.Repeat("x", 400)
	errorClass := "http_status"
	duration := 120
	for number := 1; number <= 5; number++ {
		message := long
		status := http.StatusInternalServerError
		f.seedAttempt(f.orgID, seedAttempt{DeliveryID: failedID, Number: number, Outcome: "retryable_failure",
			Status: &status, ErrorClass: &errorClass, Error: &message, Duration: &duration,
			StartedAt: historyBase.Add(time.Duration(number) * time.Minute)})
	}

	var attemptsPage historyAttemptsPage
	reply = f.do(http.MethodGet, "/v1/deliveries/"+failedID+"/attempts?page_size=2", token, "")
	wantStatus(t, reply, http.StatusOK, "attempts page one")
	attemptsPage = decodeMgmt[historyAttemptsPage](t, reply)
	if len(attemptsPage.Attempts) != 2 || attemptsPage.Attempts[0].AttemptNumber != 1 ||
		attemptsPage.Attempts[1].AttemptNumber != 2 || attemptsPage.NextCursor == "" {
		t.Fatalf("attempts page one = %+v", attemptsPage)
	}
	if bytes.Contains(reply.body, []byte("BODY-SNIPPET-SECRET")) ||
		bytes.Contains(reply.body, []byte("URL-SECRET-TOKEN")) ||
		bytes.Contains(reply.body, []byte("HEADER-SECRET")) ||
		bytes.Contains(reply.body, []byte("ATTEMPT-PASSWORD-SECRET")) ||
		bytes.Contains(reply.body, []byte("request_headers")) ||
		bytes.Contains(reply.body, []byte("response_body_snippet")) {
		t.Fatal("attempt page exposed receiver content or credentials")
	}
	for _, attempt := range attemptsPage.Attempts {
		if len([]rune(attempt.ErrorMessage)) > 500 || !strings.Contains(attempt.ErrorMessage, "[REDACTED]") {
			t.Fatalf("attempt diagnostic = %q", attempt.ErrorMessage)
		}
	}

	pageTwo := decodeMgmt[historyAttemptsPage](t,
		f.do(http.MethodGet, "/v1/deliveries/"+failedID+"/attempts?page_size=2&cursor="+attemptsPage.NextCursor, token, ""))
	if len(pageTwo.Attempts) != 2 || pageTwo.Attempts[0].AttemptNumber != 3 || pageTwo.NextCursor == "" {
		t.Fatalf("attempts page two = %+v", pageTwo)
	}
	pageThree := decodeMgmt[historyAttemptsPage](t,
		f.do(http.MethodGet, "/v1/deliveries/"+failedID+"/attempts?page_size=2&cursor="+pageTwo.NextCursor, token, ""))
	if len(pageThree.Attempts) != 1 || pageThree.Attempts[0].AttemptNumber != 5 || pageThree.NextCursor != "" {
		t.Fatalf("attempts page three = %+v", pageThree)
	}

	zero := f.do(http.MethodGet, "/v1/deliveries/"+pendingID+"/attempts", token, "")
	wantStatus(t, zero, http.StatusOK, "zero attempts")
	if !bytes.Contains(zero.body, []byte(`"attempts":[]`)) {
		t.Fatalf("zero attempts body = %s", zero.body)
	}

	wantProblem(t, f.do(http.MethodGet, "/v1/deliveries/"+mustID(t)+"/attempts", token, ""),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodGet, "/v1/deliveries/"+failedID+"/attempts?state=failed", token, ""),
		http.StatusBadRequest, "unknown_filter", "state")
	wantProblem(t, f.do(http.MethodGet, "/v1/deliveries/"+failedID+"/attempts?page_size=101", token, ""),
		http.StatusBadRequest, "invalid_page_size", "page_size")
}

func TestDeliveryHistoryPaginationAndCursorSafety(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	f.cleanupOrgRows(f.orgID)
	token := f.historyToken()
	secondToken, _ := f.createKey("history-read-two", "deliveries:read")

	for index := 0; index < 60; index++ {
		eventID := mustID(t)
		f.seedEvent(f.orgID, eventID, "order.batch", historyBase.Add(time.Duration(index)*time.Second),
			`{"index":`+itoa(index)+`}`, 0)
	}

	defaultPage := decodeMgmt[historyEventsPage](t, f.do(http.MethodGet, "/v1/events", token, ""))
	if len(defaultPage.Events) != 50 || defaultPage.NextCursor == "" {
		t.Fatalf("default page size = %d/%q", len(defaultPage.Events), defaultPage.NextCursor)
	}
	maxPage := decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?page_size=100", token, ""))
	if len(maxPage.Events) != 60 || maxPage.NextCursor != "" {
		t.Fatalf("max page = %d/%q", len(maxPage.Events), maxPage.NextCursor)
	}
	for _, size := range []string{"0", "101", "-1", "many", "1.5"} {
		wantProblem(t, f.do(http.MethodGet, "/v1/events?page_size="+size, token, ""),
			http.StatusBadRequest, "invalid_page_size", "page_size")
	}

	first := decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?page_size=2", token, ""))
	if len(first.Events) != 2 || first.NextCursor == "" {
		t.Fatalf("first page = %+v", first)
	}
	second := decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?page_size=2&cursor="+first.NextCursor, token, ""))
	if len(second.Events) != 2 || second.Events[0].ID == first.Events[1].ID ||
		second.Events[0].ReceivedAt.After(first.Events[1].ReceivedAt) {
		t.Fatalf("second page = %+v", second)
	}
	if first.Events[0].ID == first.Events[1].ID {
		t.Fatal("first page contains a duplicate")
	}

	tampered := first.NextCursor
	if tampered[0] == 'A' {
		tampered = "B" + tampered[1:]
	} else {
		tampered = "A" + tampered[1:]
	}
	wantProblem(t, f.do(http.MethodGet, "/v1/events?page_size=2&cursor="+tampered, token, ""),
		http.StatusBadRequest, "invalid_cursor", "cursor")
	wantProblem(t, f.do(http.MethodGet, "/v1/deliveries?cursor="+first.NextCursor, token, ""),
		http.StatusBadRequest, "invalid_cursor", "cursor")
	wantProblem(t, f.do(http.MethodGet, "/v1/events?type=order.batch&cursor="+first.NextCursor, token, ""),
		http.StatusBadRequest, "invalid_cursor", "cursor")
	wantProblem(t, f.do(http.MethodGet, "/v1/events?cursor="+first.NextCursor, secondToken, ""),
		http.StatusBadRequest, "invalid_cursor", "cursor")

	seen := map[string]int{}
	cursor := ""
	pages := 0
	for {
		path := "/v1/events?page_size=7"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		page := decodeMgmt[historyEventsPage](t, f.do(http.MethodGet, path, token, ""))
		for _, event := range page.Events {
			seen[event.ID]++
		}
		pages++
		if page.NextCursor == "" {
			break
		}
		cursor = page.NextCursor
	}
	if pages != 9 {
		t.Fatalf("pagination pages = %d", pages)
	}
	if len(seen) != 60 {
		t.Fatalf("pagination saw %d unique events", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("event %s appeared %d times", id, count)
		}
	}

	// Concurrent newer inserts do not duplicate or skip older rows within the
	// established continuation contract.
	anchor := decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?page_size=3", token, ""))
	seen = map[string]int{}
	for _, event := range anchor.Events {
		seen[event.ID]++
	}
	for index := 0; index < 2; index++ {
		f.seedEvent(f.orgID, mustID(t), "order.late", time.Now().UTC().Add(time.Duration(index)*time.Second),
			`{"late":true}`, 0)
	}
	cursor = anchor.NextCursor
	for cursor != "" {
		page := decodeMgmt[historyEventsPage](t,
			f.do(http.MethodGet, "/v1/events?page_size=3&cursor="+cursor, token, ""))
		for _, event := range page.Events {
			seen[event.ID]++
		}
		cursor = page.NextCursor
	}
	if len(seen) != 60 {
		t.Fatalf("concurrent pagination saw %d events, want 60 seeded", len(seen))
	}
	for id, count := range seen {
		if count != 1 {
			t.Fatalf("event %s appeared %d times across pages", id, count)
		}
	}
	fresh := decodeMgmt[historyEventsPage](t,
		f.do(http.MethodGet, "/v1/events?type=order.late", token, ""))
	if len(fresh.Events) != 2 {
		t.Fatalf("late events = %d", len(fresh.Events))
	}
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 8)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func ptr[T any](value T) *T { return &value }
