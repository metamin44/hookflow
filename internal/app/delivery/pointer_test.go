package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

func validPointer() Pointer {
	return Pointer{
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		DeliveryID:     "22222222-2222-4222-8222-222222222222",
		OutboxID:       "33333333-3333-4333-8333-333333333333",
		DispatchSeq:    1,
	}
}

func TestPointerValidate(t *testing.T) {
	if err := validPointer().Validate(); err != nil {
		t.Fatalf("valid pointer rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Pointer)
	}{
		{"missing organization", func(p *Pointer) { p.OrganizationID = "" }},
		{"missing delivery", func(p *Pointer) { p.DeliveryID = "" }},
		{"missing outbox", func(p *Pointer) { p.OutboxID = "" }},
		{"uppercase organization", func(p *Pointer) { p.OrganizationID = "A" + p.OrganizationID[1:] }},
		{"uppercase delivery", func(p *Pointer) { p.DeliveryID = "A" + p.DeliveryID[1:] }},
		{"uppercase outbox", func(p *Pointer) { p.OutboxID = "A" + p.OutboxID[1:] }},
		{"braced organization", func(p *Pointer) { p.OrganizationID = "{" + p.OrganizationID + "}" }},
		{"unhyphenated delivery", func(p *Pointer) { p.DeliveryID = strings.ReplaceAll(p.DeliveryID, "-", "") }},
		{"non hexadecimal outbox", func(p *Pointer) { p.OutboxID = "g" + p.OutboxID[1:] }},
		{"zero sequence", func(p *Pointer) { p.DispatchSeq = 0 }},
		{"negative sequence", func(p *Pointer) { p.DispatchSeq = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pointer := validPointer()
			tc.mutate(&pointer)
			if err := pointer.Validate(); err == nil {
				t.Fatal("invalid pointer accepted")
			}
		})
	}
}

func TestPointerStatusValues(t *testing.T) {
	statuses := map[PointerStatus]string{
		PointerAcquired:   "acquired",
		PointerPublishing: "publishing",
		PointerStale:      "stale",
		PointerObsolete:   "obsolete",
		PointerExpired:    "expired",
		PointerDuplicate:  "duplicate",
		PointerNotDue:     "not_due",
		PointerInvalid:    "invalid",
	}
	for status, want := range statuses {
		if string(status) != want {
			t.Errorf("status = %q, want %q", status, want)
		}
	}
}

func TestPointerAcquisitionStatus(t *testing.T) {
	now := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	pointer := validPointer()
	base := pointerRow{
		claim: Claim{
			OrganizationID: pointer.OrganizationID,
			DeliveryID:     pointer.DeliveryID,
			EventID:        "44444444-4444-4444-8444-444444444444",
			DispatchSeq:    pointer.DispatchSeq,
			EventExpiresAt: now.Add(time.Hour),
		},
		state:          "pending",
		nextAttemptAt:  now.Add(-time.Minute),
		outboxID:       pointer.OutboxID,
		outboxSeq:      pointer.DispatchSeq,
		outboxStatus:   "published",
		leaseExpiresAt: pgtype.Timestamptz{},
	}

	cases := []struct {
		name   string
		mutate func(*pointerRow)
		want   PointerStatus
	}{
		{"acquired", func(*pointerRow) {}, PointerAcquired},
		{"missing outbox", func(r *pointerRow) { r.outboxID = "" }, PointerInvalid},
		{"terminal", func(r *pointerRow) { r.state = "succeeded" }, PointerObsolete},
		{"expired event", func(r *pointerRow) { r.claim.EventExpiresAt = now }, PointerExpired},
		{"sequence mismatch", func(r *pointerRow) { r.claim.DispatchSeq++ }, PointerStale},
		{"outbox sequence mismatch", func(r *pointerRow) { r.outboxSeq++ }, PointerStale},
		{"unpublished outbox", func(r *pointerRow) { r.outboxStatus = "pending" }, PointerStale},
		{"publication awaiting T2", func(r *pointerRow) { r.outboxStatus = "publishing" }, PointerPublishing},
		{"attempts exhausted", func(r *pointerRow) { r.claim.AttemptCount = 3 }, PointerObsolete},
		{"live lease", func(r *pointerRow) {
			r.claim.LeaseToken = "55555555-5555-4555-8555-555555555555"
			r.leaseExpiresAt = pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}
		}, PointerDuplicate},
		{"expired lease", func(r *pointerRow) {
			r.claim.LeaseToken = "55555555-5555-4555-8555-555555555555"
			r.leaseExpiresAt = pgtype.Timestamptz{Time: now, Valid: true}
		}, PointerAcquired},
		{"not due", func(r *pointerRow) { r.nextAttemptAt = now.Add(time.Nanosecond) }, PointerNotDue},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			row := base
			tc.mutate(&row)
			if got := pointerAcquisitionStatus(row, pointer, 3, now); got != tc.want {
				t.Fatalf("status = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestPointerSentinels(t *testing.T) {
	sentinels := []error{ErrPointerStale, ErrEventExpired, ErrInsufficientLease}
	for i, sentinel := range sentinels {
		if sentinel == nil {
			t.Fatalf("sentinel %d is nil", i)
		}
		wrapped := fmt.Errorf("prepare HTTP: %w", sentinel)
		if !errors.Is(wrapped, sentinel) {
			t.Errorf("errors.Is did not preserve sentinel %d", i)
		}
		if errors.Is(wrapped, sentinels[(i+1)%len(sentinels)]) {
			t.Errorf("sentinel %d aliases another sentinel", i)
		}
	}
}

func TestHTTPPreparationGuards(t *testing.T) {
	claim := validClaim()
	now := claim.LeaseExpiresAt.Add(-time.Minute)
	base := httpDelivery{
		eventID:        claim.EventID,
		endpointID:     claim.EndpointID,
		dispatchSeq:    claim.DispatchSeq,
		state:          "pending",
		leaseToken:     claim.LeaseToken,
		leaseExpiresAt: pgtype.Timestamptz{Time: claim.LeaseExpiresAt, Valid: true},
		eventExpiresAt: now.Add(time.Hour),
	}
	cases := []struct {
		name     string
		mutate   func(*httpDelivery)
		required time.Duration
		want     error
	}{
		{"sufficient", func(*httpDelivery) {}, 30 * time.Second, nil},
		{"exact budget", func(*httpDelivery) {}, time.Minute, nil},
		{"insufficient", func(*httpDelivery) {}, time.Minute + time.Nanosecond, ErrInsufficientLease},
		{"expired lease", func(d *httpDelivery) {
			d.leaseExpiresAt = pgtype.Timestamptz{Time: now, Valid: true}
		}, time.Nanosecond, ErrInsufficientLease},
		{"missing lease expiry", func(d *httpDelivery) {
			d.leaseExpiresAt = pgtype.Timestamptz{}
		}, time.Nanosecond, ErrInsufficientLease},
		{"expired event", func(d *httpDelivery) { d.eventExpiresAt = now }, time.Second, ErrEventExpired},
		{"wrong event", func(d *httpDelivery) { d.eventID = "99999999-9999-4999-8999-999999999999" }, time.Second, ErrPointerStale},
		{"wrong endpoint", func(d *httpDelivery) { d.endpointID = "99999999-9999-4999-8999-999999999999" }, time.Second, ErrPointerStale},
		{"wrong sequence", func(d *httpDelivery) { d.dispatchSeq++ }, time.Second, ErrPointerStale},
		{"wrong token", func(d *httpDelivery) { d.leaseToken = "99999999-9999-4999-8999-999999999999" }, time.Second, ErrPointerStale},
		{"terminal", func(d *httpDelivery) { d.state = "failed" }, time.Second, ErrPointerStale},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			locked := base
			tc.mutate(&locked)
			if got := validateHTTPPreparation(locked, claim, now, tc.required); !errors.Is(got, tc.want) {
				t.Fatalf("validateHTTPPreparation() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRedactDiagnostic(t *testing.T) {
	cases := []string{
		"request failed Authorization: Bearer sk_live_12345",
		`{"client_secret":"do-not-persist","ordinary":"kept"}`,
		strings.Repeat("é", diagnosticLimit+1),
		" \n\t ",
	}
	for _, input := range cases {
		if got, want := RedactDiagnostic(input), boundedDiagnostic(input); got != want {
			t.Errorf("RedactDiagnostic() = %q, want %q", got, want)
		}
	}
}

func TestSanitizeRequestHeaders(t *testing.T) {
	input := `{
		"Authorization":"Bearer authorization-secret",
		"proxy-authorization":"Basic proxy-secret",
		"Proxy_Authorization":"proxy-underscore-secret",
		"Cookie":"session=cookie-secret",
		"set-cookie":"sid=set-cookie-secret",
		"SET_COOKIE":"sid=set-cookie-underscore-secret",
		"X-API-Key":"x-api-key-secret",
		"x_api_key":"x-api-key-underscore-secret",
		"api-key":"api-key-secret",
		"Access-Token":"access-token-secret",
		"refresh_token":"refresh-token-secret",
		"Client-Secret":"client-secret-secret",
		"PASSWORD":"password-secret",
		"secret":"secret-secret",
		"Content-Type":"application/json",
		"X-Trace-ID":"trace-123",
		"X-Retry-Count":3,
		"X-Context":{"ordinary":true}
	}`
	secrets := []string{
		"authorization-secret", "proxy-secret", "proxy-underscore-secret",
		"cookie-secret", "set-cookie-secret", "set-cookie-underscore-secret",
		"x-api-key-secret", "x-api-key-underscore-secret", "api-key-secret",
		"access-token-secret", "refresh-token-secret", "client-secret-secret",
		"password-secret", "secret-secret",
	}

	got, err := sanitizeRequestHeaders(input)
	if err != nil {
		t.Fatalf("sanitizeRequestHeaders() error: %v", err)
	}
	for _, secret := range secrets {
		if strings.Contains(got, secret) {
			t.Errorf("credential %q survived sanitization: %s", secret, got)
		}
	}

	var headers map[string]json.RawMessage
	if err := json.Unmarshal([]byte(got), &headers); err != nil {
		t.Fatalf("sanitized headers are not JSON: %v", err)
	}
	for name, value := range headers {
		if strings.Contains(string(value), "[REDACTED]") {
			continue
		}
		switch name {
		case "Content-Type":
			if string(value) != `"application/json"` {
				t.Errorf("Content-Type changed: %s", value)
			}
		case "X-Trace-ID":
			if string(value) != `"trace-123"` {
				t.Errorf("X-Trace-ID changed: %s", value)
			}
		case "X-Retry-Count":
			if string(value) != "3" {
				t.Errorf("numeric header changed: %s", value)
			}
		case "X-Context":
			if string(value) != `{"ordinary":true}` {
				t.Errorf("ordinary object changed: %s", value)
			}
		default:
			t.Errorf("sensitive header %q was not redacted: %s", name, value)
		}
	}
}

func TestSanitizeRequestHeadersRejectsNonObjects(t *testing.T) {
	for _, input := range []string{"", "{", "[]", "null", `"raw-secret"`, "123", "{} {}"} {
		t.Run(input, func(t *testing.T) {
			got, err := sanitizeRequestHeaders(input)
			if err == nil {
				t.Fatalf("invalid header document accepted: %q", input)
			}
			if got != "" {
				t.Fatalf("invalid header document returned raw data: %q", got)
			}
		})
	}
}

func TestPointerMethodsValidateBeforeDatabase(t *testing.T) {
	ctx := context.Background()
	db := &stubBeginner{}
	store, err := NewStore(db, testStoreOptions())
	if err != nil {
		t.Fatalf("NewStore() error: %v", err)
	}
	opts := AcquireOptions{LeaseDuration: time.Minute, MaxAttempts: 3, NewToken: RandomToken}

	pointer := validPointer()
	pointer.OutboxID = "bad"
	if result, err := store.AcquirePointer(ctx, pointer, opts); err == nil || result.Status != PointerInvalid {
		t.Fatalf("AcquirePointer(invalid) = %+v, %v", result, err)
	}
	badOpts := opts
	badOpts.LeaseDuration = 0
	if _, err := store.AcquirePointer(ctx, validPointer(), badOpts); err == nil {
		t.Fatal("AcquirePointer() accepted invalid options")
	}
	if changed, err := store.ExpirePointer(ctx, pointer); err == nil || changed {
		t.Fatalf("ExpirePointer(invalid) = %t, %v", changed, err)
	}
	claim := validClaim()
	claim.LeaseToken = "bad"
	if changed, err := store.ReleasePointer(ctx, claim); err == nil || changed {
		t.Fatalf("ReleasePointer(invalid) = %t, %v", changed, err)
	}
	if _, err := store.PrepareHTTP(ctx, validClaim(), 0); err == nil {
		t.Fatal("PrepareHTTP() accepted a zero budget")
	}
	claim = validClaim()
	claim.EventID = "bad"
	if _, err := store.PrepareHTTP(ctx, claim, time.Second); err == nil {
		t.Fatal("PrepareHTTP() accepted an invalid claim")
	}
	if db.begins != 0 {
		t.Fatalf("validation failures began %d transactions", db.begins)
	}
}

func TestSanitizedOrdinaryHeadersRemainEquivalent(t *testing.T) {
	input := `{"Accept":"application/json","X-Number":1.25,"X-List":["a","b"]}`
	got, err := sanitizeRequestHeaders(input)
	if err != nil {
		t.Fatalf("sanitizeRequestHeaders() error: %v", err)
	}
	var before, after any
	if err := json.Unmarshal([]byte(input), &before); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &after); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("ordinary headers changed: before=%v after=%v", before, after)
	}
}
