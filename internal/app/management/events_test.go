package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	eventapp "github.com/metamin44/hookflow/internal/app/event"
	"github.com/metamin44/hookflow/internal/modules/apikey"
	"github.com/metamin44/hookflow/internal/modules/event"
	"github.com/metamin44/hookflow/internal/modules/organization"
)

type stubAcceptor struct {
	result eventapp.Result
	err    error
	called int
	input  eventapp.AcceptInput
}

func (s *stubAcceptor) AcceptEvent(_ context.Context, input eventapp.AcceptInput) (eventapp.Result, error) {
	s.called++
	s.input = input
	return s.result, s.err
}

func discardLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func TestIdempotencyKeyHeader(t *testing.T) {
	cases := []struct {
		name   string
		values []string
		want   string
		code   string
	}{
		{name: "missing", values: nil, code: "missing_idempotency_key"},
		{name: "empty", values: []string{""}, code: "missing_idempotency_key"},
		{name: "multiple", values: []string{"a", "b"}, code: "invalid_idempotency_key"},
		{name: "two hundred bytes", values: []string{strings.Repeat("k", 200)}, want: strings.Repeat("k", 200)},
		{name: "too long", values: []string{strings.Repeat("k", 201)}, code: "invalid_idempotency_key"},
		{name: "invalid utf8", values: []string{"k\xff"}, code: "invalid_idempotency_key"},
		{name: "control character", values: []string{"key\x01"}, code: "invalid_idempotency_key"},
		{name: "delete character", values: []string{"key\x7f"}, code: "invalid_idempotency_key"},
		{name: "not trimmed", values: []string{" padded "}, want: " padded "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodPost, eventsPath, nil)
			for _, value := range tc.values {
				request.Header.Add("Idempotency-Key", value)
			}
			got, appErr := idempotencyKeyHeader(request)
			if tc.code != "" {
				if appErr == nil {
					t.Fatalf("accepted %q, want %s", tc.values, tc.code)
				}
				if appErr.Status != http.StatusBadRequest || appErr.Code != tc.code {
					t.Fatalf("rejection = %d %s, want 400 %s", appErr.Status, appErr.Code, tc.code)
				}
				return
			}
			if appErr != nil {
				t.Fatalf("unexpected rejection: %v", appErr.Code)
			}
			if got != tc.want {
				t.Fatalf("key = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestOrganizationWriteAllowed(t *testing.T) {
	if appErr := organizationWriteAllowed(organization.StatusActive); appErr != nil {
		t.Fatalf("active rejected: %v", appErr.Code)
	}
	for _, status := range []string{organization.StatusSuspended, organization.StatusArchived, "missing"} {
		appErr := organizationWriteAllowed(status)
		if appErr == nil || appErr.Status != http.StatusForbidden {
			t.Fatalf("%s allowed, want 403", status)
		}
	}
	if code := organizationWriteAllowed(organization.StatusSuspended).Code; code != "organization_suspended" {
		t.Errorf("suspended code = %q", code)
	}
	if code := organizationWriteAllowed(organization.StatusArchived).Code; code != "organization_archived" {
		t.Errorf("archived code = %q", code)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("boom") }

func TestReadIngestBody(t *testing.T) {
	limit := maxIngestBodyBytes
	exact := bytes.Repeat([]byte("a"), limit)
	oversized := bytes.Repeat([]byte("a"), limit+1)

	t.Run("exactly the limit", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, eventsPath, bytes.NewReader(exact))
		recorder := httptest.NewRecorder()
		body, appErr := readIngestBody(recorder, request)
		if appErr != nil {
			t.Fatalf("rejected exact limit: %v", appErr.Code)
		}
		if len(body) != limit {
			t.Fatalf("read %d bytes, want %d", len(body), limit)
		}
	})
	t.Run("one byte over", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, eventsPath, bytes.NewReader(oversized))
		recorder := httptest.NewRecorder()
		_, appErr := readIngestBody(recorder, request)
		if appErr == nil || appErr.Code != "payload_too_large" || appErr.Status != http.StatusBadRequest {
			t.Fatalf("oversize result = %+v", appErr)
		}
		if got := recorder.Header().Get("Connection"); got != "close" {
			t.Errorf("Connection header = %q, want close", got)
		}
	})
	t.Run("declared length over the limit is refused early", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, eventsPath, strings.NewReader("{}"))
		request.ContentLength = int64(limit) + 1
		recorder := httptest.NewRecorder()
		_, appErr := readIngestBody(recorder, request)
		if appErr == nil || appErr.Code != "payload_too_large" {
			t.Fatalf("declared oversize result = %+v", appErr)
		}
		if got := recorder.Header().Get("Connection"); got != "close" {
			t.Errorf("Connection header = %q, want close", got)
		}
	})
	t.Run("chunked oversize", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, eventsPath, bytes.NewReader(oversized))
		request.ContentLength = -1
		recorder := httptest.NewRecorder()
		_, appErr := readIngestBody(recorder, request)
		if appErr == nil || appErr.Code != "payload_too_large" {
			t.Fatalf("chunked oversize result = %+v", appErr)
		}
	})
	t.Run("empty body is passed through", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, eventsPath, nil)
		recorder := httptest.NewRecorder()
		body, appErr := readIngestBody(recorder, request)
		if appErr != nil || len(body) != 0 {
			t.Fatalf("empty body = %d bytes, %v", len(body), appErr)
		}
	})
	t.Run("unreadable body", func(t *testing.T) {
		request := httptest.NewRequest(http.MethodPost, eventsPath, nil)
		request.Body = io.NopCloser(errorReader{})
		recorder := httptest.NewRecorder()
		_, appErr := readIngestBody(recorder, request)
		if appErr == nil || appErr.Code != "invalid_body" {
			t.Fatalf("read error result = %+v", appErr)
		}
	})
}

func TestIngestErrorMapping(t *testing.T) {
	rejection := event.UnknownField("meta")
	mapped := ingestError(rejection)
	var httpErr *HTTPError
	if !errors.As(mapped, &httpErr) {
		t.Fatalf("rejection mapped to %T", mapped)
	}
	if httpErr.Status != http.StatusBadRequest || httpErr.Code != event.CodeUnknownField || httpErr.Field != "meta" {
		t.Fatalf("rejection = %d %s field %q", httpErr.Status, httpErr.Code, httpErr.Field)
	}
	if httpErr.Detail == "" {
		t.Error("rejection detail is empty")
	}

	for _, tc := range []struct {
		status string
		code   string
	}{
		{status: organization.StatusSuspended, code: "organization_suspended"},
		{status: organization.StatusArchived, code: "organization_archived"},
		{status: "missing", code: "organization_archived"},
	} {
		mapped = ingestError(&eventapp.InactiveOrganizationError{Status: tc.status})
		if !errors.As(mapped, &httpErr) || httpErr.Status != http.StatusForbidden || httpErr.Code != tc.code {
			t.Fatalf("inactive %s mapped to %+v", tc.status, mapped)
		}
	}

	mapped = ingestError(eventapp.ErrIdempotencyConflict)
	if !errors.As(mapped, &httpErr) || httpErr.Status != http.StatusConflict ||
		httpErr.Code != "idempotency_key_conflict" {
		t.Fatalf("conflict mapped to %+v", mapped)
	}

	internal := errors.New("database is down")
	if got := ingestError(internal); !errors.Is(got, internal) {
		t.Fatalf("internal error was rewritten: %v", got)
	}
}

func TestWriteIngestResponse(t *testing.T) {
	receivedAt := time.Date(2026, 11, 25, 12, 34, 56, 789_000_000, time.FixedZone("plus2", 2*60*60))
	result := eventapp.Result{
		EventID:          "01920000-0000-7000-8000-000000000001",
		DestinationCount: 2,
		ReceivedAt:       receivedAt,
	}

	first := httptest.NewRecorder()
	writeIngestResponse(first, result)
	if first.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want 202", first.Code)
	}
	if got := first.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q", got)
	}
	if got := first.Header().Get("Cache-Control"); got != "no-store" {
		t.Errorf("Cache-Control = %q", got)
	}
	if got := first.Header().Get("HookFlow-Idempotent-Replay"); got != "" {
		t.Errorf("replay header on first accept = %q", got)
	}
	if got := first.Header().Get("Location"); got != "" {
		t.Errorf("unexpected Location header %q", got)
	}
	want := `{"event_id":"01920000-0000-7000-8000-000000000001","destination_count":2,"received_at":"2026-11-25T10:34:56Z"}` + "\n"
	if first.Body.String() != want {
		t.Fatalf("body = %q, want %q", first.Body.String(), want)
	}

	replay := httptest.NewRecorder()
	result.Replayed = true
	writeIngestResponse(replay, result)
	if got := replay.Header().Get("HookFlow-Idempotent-Replay"); got != "true" {
		t.Errorf("replay header = %q, want true", got)
	}
	if replay.Body.String() != first.Body.String() {
		t.Errorf("replay body %q != first body %q", replay.Body.String(), first.Body.String())
	}

	var decoded map[string]json.RawMessage
	if err := json.Unmarshal(first.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response is not JSON: %v", err)
	}
	if len(decoded) != 3 {
		t.Errorf("response has %d fields, want exactly 3", len(decoded))
	}
	if !strings.HasPrefix(first.Body.String(), `{"event_id":`) {
		t.Errorf("field order drift: %s", first.Body.String())
	}
}

func TestEventsRouteMethodNotAllowed(t *testing.T) {
	handler := &Handler{
		service: &Service{},
		auth:    &Authenticator{},
		events:  &stubAcceptor{},
		logger:  discardLogger(),
		mux:     http.NewServeMux(),
	}
	handler.routes()
	request := httptest.NewRequest(http.MethodGet, eventsPath, nil)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("GET status = %d, want 405", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "POST" {
		t.Errorf("Allow = %q, want POST", got)
	}
	var body problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("problem body: %v", err)
	}
	if body.Code != "method_not_allowed" {
		t.Errorf("code = %q", body.Code)
	}
}

func TestAuthenticatedIngestionStillRequiresBearer(t *testing.T) {
	acceptor := &stubAcceptor{}
	handler := &Handler{
		service: &Service{},
		auth:    &Authenticator{},
		events:  acceptor,
		logger:  discardLogger(),
		mux:     http.NewServeMux(),
	}
	handler.routes()
	request := httptest.NewRequest(http.MethodPost, eventsPath, strings.NewReader(`{"event_type":"a.b","payload":{}}`))
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
	var body problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("problem body: %v", err)
	}
	if body.Code != "invalid_api_key" {
		t.Errorf("code = %q", body.Code)
	}
	if acceptor.called != 0 {
		t.Errorf("unauthorized request reached the use case %d times", acceptor.called)
	}
}

// TestInvalidTokenResponseCarriesBearerChallenge checks the RFC 6750 challenge
// on the invalid-token path as rendered by the HTTP layer. The challenge text
// is identical to the unauthenticated path, so it reveals nothing about why
// the key was rejected.
func TestInvalidTokenResponseCarriesBearerChallenge(t *testing.T) {
	authenticator, store := authenticatorFixture(t, apikey.AuthRecord{}, "")
	handler := &Handler{
		service: &Service{},
		auth:    authenticator,
		events:  &stubAcceptor{},
		logger:  discardLogger(),
		mux:     http.NewServeMux(),
	}
	handler.routes()
	request := httptest.NewRequest(http.MethodPost, eventsPath, strings.NewReader(`{"event_type":"a.b","payload":{}}`))
	request.Header.Set("Authorization", "Bearer short")
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", recorder.Code)
	}
	if got := recorder.Header().Get("WWW-Authenticate"); got != "Bearer" {
		t.Errorf("WWW-Authenticate = %q, want Bearer", got)
	}
	var body problem
	if err := json.Unmarshal(recorder.Body.Bytes(), &body); err != nil {
		t.Fatalf("problem body: %v", err)
	}
	if body.Code != "invalid_api_key" {
		t.Errorf("code = %q, want invalid_api_key", body.Code)
	}
	if store.calls != 0 {
		t.Errorf("store calls = %d, want 0 for a malformed token", store.calls)
	}
}
