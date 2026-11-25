//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/modules/apikey"
)

const eventsEndpoint = "/v1/events"

type ingestAPIResponse struct {
	EventID          string `json:"event_id"`
	DestinationCount int    `json:"destination_count"`
	ReceivedAt       string `json:"received_at"`
}

type hiddenLengthReader struct{ inner io.Reader }

func (r hiddenLengthReader) Read(p []byte) (int, error) { return r.inner.Read(p) }

func doEventRequest(t *testing.T, f *mgmtFixture, token string, body []byte, keys []string, chunked bool) mgmtReply {
	t.Helper()
	var reader io.Reader = bytes.NewReader(body)
	if chunked {
		reader = hiddenLengthReader{inner: bytes.NewReader(body)}
	}
	request, err := http.NewRequest(http.MethodPost, f.server.URL+eventsEndpoint, reader)
	if err != nil {
		t.Fatalf("build ingestion request: %v", err)
	}
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
	}
	request.Header.Set("Content-Type", "application/json")
	for _, key := range keys {
		request.Header.Add("Idempotency-Key", key)
	}
	response, err := f.client.Do(request)
	if err != nil {
		t.Fatalf("send ingestion request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read ingestion response: %v", err)
	}
	return mgmtReply{status: response.StatusCode, header: response.Header, body: data}
}

func (f *mgmtFixture) countIdempotencyKeys(key string) int {
	return f.countIdempotencyFor(f.orgID, key)
}

func (f *mgmtFixture) countIdempotencyFor(orgID, key string) int {
	f.t.Helper()
	var n int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid AND idempotency_key = $2`,
		orgID, key,
	).Scan(&n); err != nil {
		f.t.Fatalf("count idempotency records: %v", err)
	}
	return n
}

func ingestionBodyOfSize(t *testing.T, eventType string, size int) []byte {
	t.Helper()
	prefix := fmt.Sprintf(`{"event_type":%q,"payload":{"pad":"`, eventType)
	suffix := `"}}`
	padding := size - len(prefix) - len(suffix)
	if padding < 0 {
		t.Fatalf("size %d is smaller than the envelope", size)
	}
	return []byte(prefix + strings.Repeat("x", padding) + suffix)
}

func TestIngestionAPIAcceptReplayAndConflict(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("ingest", apikey.ScopeEventsWrite, apikey.ScopeEndpointsWrite)
	endpoint := f.createEndpoint(token, `{"name":"api-ingest","url":"https://example.test/api-ingest"}`)
	subscription := f.do(http.MethodPost, f.endpoint(endpoint.ID)+"/subscriptions", token, `{"event_type":"api.test"}`)
	if subscription.status != http.StatusCreated {
		t.Fatalf("create subscription = %d %s", subscription.status, subscription.body)
	}

	body := []byte(`{"event_type":"api.test","payload":{"n":1}}`)
	first := doEventRequest(t, f, token, body, []string{"api-key-1"}, false)
	if first.status != http.StatusAccepted {
		t.Fatalf("accept = %d %s", first.status, first.body)
	}
	if got := first.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := first.header.Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := first.header.Get("Location"); got != "" {
		t.Errorf("unexpected Location header %q", got)
	}
	if got := first.header.Get("HookFlow-Idempotent-Replay"); got != "" {
		t.Errorf("first accept replay header = %q", got)
	}
	if !strings.HasPrefix(string(first.body), `{"event_id":`) ||
		!strings.HasSuffix(string(first.body), "\n") {
		t.Fatalf("unexpected accept body %q", first.body)
	}
	accepted := decodeMgmt[ingestAPIResponse](t, first)
	if accepted.DestinationCount != 1 {
		t.Fatalf("destination count = %d, want 1", accepted.DestinationCount)
	}
	if !regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z$`).MatchString(accepted.ReceivedAt) {
		t.Fatalf("received_at = %q, want UTC RFC 3339 seconds", accepted.ReceivedAt)
	}

	var (
		storedReceived time.Time
		provenanceNull bool
	)
	if err := f.pool.QueryRow(context.Background(), `
		SELECT received_at,
		       (api_key_id IS NULL AND source_ip IS NULL AND headers IS NULL AND occurred_at IS NULL)
		  FROM events WHERE id = $1::uuid AND organization_id = $2::uuid`,
		accepted.EventID, f.orgID,
	).Scan(&storedReceived, &provenanceNull); err != nil {
		t.Fatalf("read accepted event: %v", err)
	}
	if !provenanceNull {
		t.Error("ingestion populated provenance columns that must stay NULL")
	}
	if got, want := accepted.ReceivedAt, storedReceived.UTC().Format(time.RFC3339); got != want {
		t.Errorf("response received_at %q != stored %q", got, want)
	}

	replay := doEventRequest(t, f, token, body, []string{"api-key-1"}, false)
	if replay.status != http.StatusAccepted {
		t.Fatalf("replay = %d %s", replay.status, replay.body)
	}
	if got := replay.header.Get("HookFlow-Idempotent-Replay"); got != "true" {
		t.Errorf("replay header = %q, want true", got)
	}
	if !bytes.Equal(replay.body, first.body) {
		t.Errorf("replay body %q != first body %q", replay.body, first.body)
	}
	if rows := f.countIdempotencyKeys("api-key-1"); rows != 1 {
		t.Errorf("idempotency rows = %d, want 1", rows)
	}

	conflict := doEventRequest(t, f, token,
		[]byte(`{"event_type":"api.test","payload":{"n":2}}`), []string{"api-key-1"}, false)
	if conflict.status != http.StatusConflict {
		t.Fatalf("conflict = %d %s", conflict.status, conflict.body)
	}
	problem := decodeMgmt[problemDoc](t, conflict)
	if problem.Code != "idempotency_key_conflict" || problem.Status != http.StatusConflict {
		t.Fatalf("conflict problem = %+v", problem)
	}
}

func TestIngestionAPIHeaderValidation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("ingest-headers", apikey.ScopeEventsWrite)
	body := []byte(`{"event_type":"headers.test","payload":{}}`)

	missing := doEventRequest(t, f, token, body, nil, false)
	if missing.status != http.StatusBadRequest {
		t.Fatalf("missing key = %d %s", missing.status, missing.body)
	}
	if problem := decodeMgmt[problemDoc](t, missing); problem.Code != "missing_idempotency_key" {
		t.Errorf("missing key code = %q", problem.Code)
	}

	multiple := doEventRequest(t, f, token, body, []string{"one", "two"}, false)
	if multiple.status != http.StatusBadRequest {
		t.Fatalf("multiple keys = %d %s", multiple.status, multiple.body)
	}
	if problem := decodeMgmt[problemDoc](t, multiple); problem.Code != "invalid_idempotency_key" {
		t.Errorf("multiple key code = %q", problem.Code)
	}

	tooLong := doEventRequest(t, f, token, body, []string{strings.Repeat("k", 201)}, false)
	if tooLong.status != http.StatusBadRequest {
		t.Fatalf("long key = %d %s", tooLong.status, tooLong.body)
	}
	if problem := decodeMgmt[problemDoc](t, tooLong); problem.Code != "invalid_idempotency_key" {
		t.Errorf("long key code = %q", problem.Code)
	}

	maximum := doEventRequest(t, f, token, body, []string{strings.Repeat("k", 200)}, false)
	if maximum.status != http.StatusAccepted {
		t.Fatalf("200-byte key = %d %s", maximum.status, maximum.body)
	}
}

func TestIngestionAPIBodyValidation(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("ingest-body", apikey.ScopeEventsWrite)

	cases := []struct {
		name   string
		body   string
		status int
		code   string
		field  string
	}{
		{name: "malformed", body: `{"event_type":`, status: http.StatusBadRequest, code: "invalid_json_document"},
		{name: "multiple documents", body: `{"event_type":"a.b","payload":{}}{}`, status: http.StatusBadRequest, code: "invalid_json_document"},
		{name: "non-object document", body: `[]`, status: http.StatusBadRequest, code: "invalid_json_document"},
		{name: "unknown member", body: `{"event_type":"a.b","payload":{},"meta":true}`, status: http.StatusBadRequest, code: "unknown_field", field: "meta"},
		{name: "event type not string", body: `{"event_type":7,"payload":{}}`, status: http.StatusBadRequest, code: "invalid_event_type", field: "event_type"},
		{name: "event type syntax", body: `{"event_type":"Not Valid","payload":{}}`, status: http.StatusBadRequest, code: "invalid_event_type", field: "event_type"},
		{name: "payload not object", body: `{"event_type":"a.b","payload":[]}`, status: http.StatusBadRequest, code: "invalid_json_document", field: "payload"},
		{name: "duplicate member", body: `{"event_type":"a.b","payload":{},"payload":{}}`, status: http.StatusBadRequest, code: "duplicate_json_key"},
		{name: "unsafe integer", body: `{"event_type":"a.b","payload":{"n":9007199254740992}}`, status: http.StatusBadRequest, code: "integer_out_of_safe_range"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			key := "body-" + strings.ReplaceAll(tc.name, " ", "-")
			reply := doEventRequest(t, f, token, []byte(tc.body), []string{key}, false)
			if reply.status != tc.status {
				t.Fatalf("status = %d %s, want %d", reply.status, reply.body, tc.status)
			}
			problem := decodeMgmt[problemDoc](t, reply)
			if problem.Code != tc.code || problem.Field != tc.field {
				t.Fatalf("problem = %+v, want code %q field %q", problem, tc.code, tc.field)
			}
			if rows := f.countIdempotencyKeys(key); rows != 0 {
				t.Errorf("rejected body wrote %d idempotency rows", rows)
			}
		})
	}
}

func TestIngestionAPIBodyLimit(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("ingest-limit", apikey.ScopeEventsWrite)

	exact := ingestionBodyOfSize(t, "limit.test", 1<<20)
	accepted := doEventRequest(t, f, token, exact, []string{"limit-exact"}, false)
	if accepted.status != http.StatusAccepted {
		t.Fatalf("exact 1 MiB = %d %s", accepted.status, accepted.body)
	}

	oversized := ingestionBodyOfSize(t, "limit.test", 1<<20+1)
	rejected := doEventRequest(t, f, token, oversized, []string{"limit-over"}, false)
	if rejected.status != http.StatusBadRequest {
		t.Fatalf("1 MiB + 1 = %d %s, want 400", rejected.status, rejected.body)
	}
	if problem := decodeMgmt[problemDoc](t, rejected); problem.Code != "payload_too_large" {
		t.Errorf("oversize code = %q", problem.Code)
	}

	chunked := doEventRequest(t, f, token, oversized, []string{"limit-chunked"}, true)
	if chunked.status != http.StatusBadRequest {
		t.Fatalf("chunked oversize = %d %s, want 400", chunked.status, chunked.body)
	}
	if problem := decodeMgmt[problemDoc](t, chunked); problem.Code != "payload_too_large" {
		t.Errorf("chunked code = %q", problem.Code)
	}

	// The Idempotency-Key check precedes the body probe.
	order := doEventRequest(t, f, token, oversized, nil, false)
	if order.status != http.StatusBadRequest {
		t.Fatalf("missing key with oversize body = %d %s", order.status, order.body)
	}
	if problem := decodeMgmt[problemDoc](t, order); problem.Code != "missing_idempotency_key" {
		t.Errorf("precedence code = %q, want missing_idempotency_key", problem.Code)
	}

	for _, key := range []string{"limit-over", "limit-chunked"} {
		if rows := f.countIdempotencyKeys(key); rows != 0 {
			t.Errorf("oversize key %q wrote %d idempotency rows", key, rows)
		}
	}
	if rows := f.countIdempotencyKeys("limit-exact"); rows != 1 {
		t.Errorf("exact body key rows = %d, want 1", rows)
	}
}

func TestIngestionAPIAuthorizationAndOrganizationState(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	readOnly, _ := f.createKey("read-only", apikey.ScopeEndpointsRead)
	body := []byte(`{"event_type":"auth.test","payload":{}}`)

	unauthorized := doEventRequest(t, f, "", body, []string{"auth-none"}, false)
	if unauthorized.status != http.StatusUnauthorized {
		t.Fatalf("no token = %d %s", unauthorized.status, unauthorized.body)
	}
	if problem := decodeMgmt[problemDoc](t, unauthorized); problem.Code != "invalid_api_key" {
		t.Errorf("no token code = %q", problem.Code)
	}

	insufficient := doEventRequest(t, f, readOnly, body, []string{"auth-scope"}, false)
	if insufficient.status != http.StatusForbidden {
		t.Fatalf("read-only scope = %d %s", insufficient.status, insufficient.body)
	}
	if problem := decodeMgmt[problemDoc](t, insufficient); problem.Code != "insufficient_scope" {
		t.Errorf("scope code = %q", problem.Code)
	}

	// Empty scopes mean events:write only.
	defaultToken, _ := f.createKey("default-scope")
	accepted := doEventRequest(t, f, defaultToken, body, []string{"auth-default"}, false)
	if accepted.status != http.StatusAccepted {
		t.Fatalf("default scope = %d %s", accepted.status, accepted.body)
	}

	for _, status := range []string{"suspended", "archived"} {
		t.Run(status, func(t *testing.T) {
			orgID := mustID(t)
			if _, err := f.pool.Exec(context.Background(),
				`INSERT INTO organizations (id, name, status) VALUES ($1::uuid, $2, 'active')`,
				orgID, "ingest-"+status+"-"+orgID,
			); err != nil {
				t.Fatalf("seed organization: %v", err)
			}
			t.Cleanup(func() {
				ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
				defer cancel()
				if _, err := f.pool.Exec(ctx, `DELETE FROM organizations WHERE id = $1::uuid`, orgID); err != nil {
					t.Errorf("cleanup organization: %v", err)
				}
			})
			token := f.createKeyFor(orgID, "state-"+status, apikey.ScopeEventsWrite)
			if _, err := f.pool.Exec(context.Background(),
				`UPDATE organizations SET status = $2 WHERE id = $1::uuid`, orgID, status,
			); err != nil {
				t.Fatalf("set organization state: %v", err)
			}

			// The malformed body proves the state check happens before parsing.
			reply := doEventRequest(t, f, token, []byte(`{"event_type":`), []string{"state-key"}, false)
			if reply.status != http.StatusForbidden {
				t.Fatalf("%s organization = %d %s, want 403", status, reply.status, reply.body)
			}
			problem := decodeMgmt[problemDoc](t, reply)
			wantCode := "organization_" + status
			if problem.Code != wantCode {
				t.Fatalf("code = %q, want %q", problem.Code, wantCode)
			}
			if rows := f.countIdempotencyFor(orgID, "state-key"); rows != 0 {
				t.Errorf("%s organization wrote %d idempotency rows", status, rows)
			}
		})
	}
}

func TestIngestionAPIZeroDestinationsAndMethods(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("ingest-zero", apikey.ScopeEventsWrite)

	accepted := doEventRequest(t, f, token, []byte(`{"event_type":"zero.dest","payload":{"n":1}}`),
		[]string{"zero-key"}, false)
	if accepted.status != http.StatusAccepted {
		t.Fatalf("zero destination accept = %d %s", accepted.status, accepted.body)
	}
	if decoded := decodeMgmt[ingestAPIResponse](t, accepted); decoded.DestinationCount != 0 {
		t.Fatalf("destination count = %d, want 0", decoded.DestinationCount)
	}

	method := f.do(http.MethodGet, eventsEndpoint, token, "")
	if method.status != http.StatusMethodNotAllowed {
		t.Fatalf("GET events = %d %s", method.status, method.body)
	}
	if got := method.header.Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
}
