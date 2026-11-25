package delivery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

type stubBeginner struct {
	begins int
}

func (s *stubBeginner) Begin(context.Context) (pgx.Tx, error) {
	s.begins++
	return nil, errors.New("unexpected begin")
}

func testStoreOptions() Options {
	return Options{
		NewID: func() (string, error) {
			return "01900000-0000-7000-8000-000000000001", nil
		},
		BuildRetryMessage: func(input RetryMessageInput) (Message, error) {
			return Message{
				ID:       "01900000-0000-7000-8000-000000000002",
				Sequence: input.Sequence,
				Kind:     "delivery.retry",
				Topic:    "hookflow.deliveries",
				Payload:  `{"delivery_id":"placeholder"}`,
			}, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func validClaim() Claim {
	return Claim{
		OrganizationID: "11111111-1111-4111-8111-111111111111",
		DeliveryID:     "22222222-2222-4222-8222-222222222222",
		EventID:        "33333333-3333-4333-8333-333333333333",
		EndpointID:     "55555555-5555-4555-8555-555555555555",
		DispatchSeq:    1,
		LeaseToken:     "44444444-4444-4444-8444-444444444444",
		LeaseExpiresAt: time.Date(2026, 11, 23, 13, 0, 0, 0, time.UTC),
		AttemptCount:   0,
	}
}

func validOutcome(kind OutcomeKind) Outcome {
	status := 204
	started := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	return Outcome{
		Kind:           kind,
		RequestMethod:  "POST",
		ResponseStatus: &status,
		ErrorClass:     "http_status",
		ErrorMessage:   "upstream failed",
		Duration:       150 * time.Millisecond,
		StartedAt:      started,
		FinishedAt:     started.Add(150 * time.Millisecond),
	}
}

func TestNewStoreRequiresDependencies(t *testing.T) {
	db := &stubBeginner{}
	if _, err := NewStore(nil, testStoreOptions()); err == nil {
		t.Fatal("nil database accepted")
	}
	opts := testStoreOptions()
	opts.NewID = nil
	if _, err := NewStore(db, opts); err == nil {
		t.Fatal("nil id generator accepted")
	}
	opts = testStoreOptions()
	opts.BuildRetryMessage = nil
	if _, err := NewStore(db, opts); err == nil {
		t.Fatal("nil message builder accepted")
	}
	if _, err := NewStore(db, testStoreOptions()); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
}

func TestAcquireOptionsValidate(t *testing.T) {
	valid := AcquireOptions{
		LeaseDuration: time.Minute,
		MaxAttempts:   3,
		NewToken:      RandomToken,
	}
	if err := valid.validate(); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*AcquireOptions)
	}{
		{"lease", func(o *AcquireOptions) { o.LeaseDuration = 0 }},
		{"attempts", func(o *AcquireOptions) { o.MaxAttempts = 0 }},
		{"token", func(o *AcquireOptions) { o.NewToken = nil }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			opts := valid
			tc.mutate(&opts)
			if err := opts.validate(); err == nil {
				t.Fatal("invalid options accepted")
			}
		})
	}
}

func TestAcquireEligible(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	liveExpiry := pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true}
	liveLease := pgtype.Timestamptz{Time: now.Add(time.Minute), Valid: true}

	if !acquireEligible("pending", now.Add(-time.Minute), now.Add(time.Hour), 0, 3, "", pgtype.Timestamptz{}, now) {
		t.Fatal("eligible delivery rejected")
	}
	if acquireEligible("succeeded", now.Add(-time.Minute), now.Add(time.Hour), 0, 3, "", pgtype.Timestamptz{}, now) {
		t.Fatal("terminal delivery accepted")
	}
	if acquireEligible("pending", now.Add(time.Minute), now.Add(time.Hour), 0, 3, "", pgtype.Timestamptz{}, now) {
		t.Fatal("future-gated delivery accepted")
	}
	if acquireEligible("pending", now.Add(-time.Minute), now, 0, 3, "", pgtype.Timestamptz{}, now) {
		t.Fatal("expired-event delivery accepted")
	}
	if acquireEligible("pending", now.Add(-time.Minute), now.Add(time.Hour), 3, 3, "", pgtype.Timestamptz{}, now) {
		t.Fatal("attempt-exhausted delivery accepted")
	}
	if acquireEligible("pending", now.Add(-time.Minute), now.Add(time.Hour), 0, 3, "token", liveLease, now) {
		t.Fatal("live lease permitted re-acquisition")
	}
	if !acquireEligible("pending", now.Add(-time.Minute), now.Add(time.Hour), 0, 3, "token", liveExpiry, now) {
		t.Fatal("expired lease was not acquirable")
	}
	if acquireEligible("pending", now.Add(-time.Minute), now.Add(time.Hour), 0, 3, "token", pgtype.Timestamptz{}, now) {
		t.Fatal("lease token without expiry was considered acquirable")
	}
}

func TestValidateClaim(t *testing.T) {
	if err := validateClaim(validClaim()); err != nil {
		t.Fatalf("valid claim rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Claim)
	}{
		{"organization", func(c *Claim) { c.OrganizationID = "bad" }},
		{"delivery", func(c *Claim) { c.DeliveryID = "bad" }},
		{"event", func(c *Claim) { c.EventID = "bad" }},
		{"endpoint", func(c *Claim) { c.EndpointID = "bad" }},
		{"generation", func(c *Claim) { c.DispatchSeq = 0 }},
		{"token", func(c *Claim) { c.LeaseToken = "bad" }},
		{"expiry", func(c *Claim) { c.LeaseExpiresAt = time.Time{} }},
		{"attempts", func(c *Claim) { c.AttemptCount = -1 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			claim := validClaim()
			tc.mutate(&claim)
			if err := validateClaim(claim); err == nil {
				t.Fatal("invalid claim accepted")
			}
		})
	}
}

func TestValidateOutcome(t *testing.T) {
	if err := validateOutcome(validOutcome(OutcomeSuccess)); err != nil {
		t.Fatalf("valid outcome rejected: %v", err)
	}
	unicodeOutcome := validOutcome(OutcomeSuccess)
	unicodeOutcome.ResponseBodySnippet = strings.Repeat("界", 2000)
	if err := validateOutcome(unicodeOutcome); err != nil {
		t.Fatalf("UTF-8 snippet within the 2000-character limit rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Outcome)
	}{
		{"kind", func(o *Outcome) { o.Kind = "cancelled" }},
		{"method", func(o *Outcome) { o.RequestMethod = " " }},
		{"status low", func(o *Outcome) { v := 99; o.ResponseStatus = &v }},
		{"status high", func(o *Outcome) { v := 600; o.ResponseStatus = &v }},
		{"headers", func(o *Outcome) { o.ResponseHeaders = "not-json" }},
		{"body", func(o *Outcome) { o.ResponseBodySnippet = strings.Repeat("x", 2001) }},
		{"class", func(o *Outcome) { o.ErrorClass = "panic" }},
		{"message", func(o *Outcome) { o.ErrorMessage = strings.Repeat("x", 501) }},
		{"duration", func(o *Outcome) { o.Duration = -1 }},
		{"started", func(o *Outcome) { o.StartedAt = time.Time{} }},
		{"finished", func(o *Outcome) { o.FinishedAt = time.Time{} }},
		{"order", func(o *Outcome) { o.FinishedAt = o.StartedAt.Add(-time.Second) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			outcome := validOutcome(OutcomeSuccess)
			tc.mutate(&outcome)
			if err := validateOutcome(outcome); err == nil {
				t.Fatal("invalid outcome accepted")
			}
		})
	}
}

func TestSanitizeRequestHeadersRedactsCredentialNames(t *testing.T) {
	input := `{"Authorization":"Bearer private","X-Auth-Token":"private","X-Custom-Credential":"private","X-Trace":"visible"}`
	got, err := sanitizeRequestHeaders(input)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(got, "private") || !strings.Contains(got, `"X-Trace":"visible"`) {
		t.Fatalf("sanitized headers = %s", got)
	}
}

func TestValidateDecision(t *testing.T) {
	if err := validateDecision(OutcomeSuccess, RetryDecision{}); err != nil {
		t.Fatalf("success decision rejected: %v", err)
	}
	if err := validateDecision(OutcomeSuccess, RetryDecision{Retry: true, Backoff: time.Minute}); err == nil {
		t.Fatal("success retry accepted")
	}
	if err := validateDecision(OutcomeTerminalFailure, RetryDecision{Retry: true, Backoff: time.Minute}); err == nil {
		t.Fatal("terminal retry accepted")
	}
	if err := validateDecision(OutcomeRetryableFailure, RetryDecision{Retry: true}); err == nil {
		t.Fatal("zero retry backoff accepted")
	}
	if err := validateDecision(OutcomeRetryableFailure, RetryDecision{Retry: true, Backoff: time.Minute}); err != nil {
		t.Fatalf("valid retry rejected: %v", err)
	}
	if err := validateDecision(OutcomeRetryableFailure, RetryDecision{}); err != nil {
		t.Fatalf("explicit terminal decision rejected: %v", err)
	}
}

func TestValidateMessage(t *testing.T) {
	valid := Message{
		ID:       "01900000-0000-7000-8000-000000000002",
		Sequence: 2,
		Kind:     "delivery.retry",
		Topic:    "hookflow.deliveries",
		Payload:  `{"delivery_id":"x"}`,
		Headers:  `{"x-generation":2}`,
	}
	if err := validateMessage(valid, 2); err != nil {
		t.Fatalf("valid message rejected: %v", err)
	}
	cases := []struct {
		name   string
		mutate func(*Message)
	}{
		{"id", func(m *Message) { m.ID = "bad" }},
		{"sequence", func(m *Message) { m.Sequence = 3 }},
		{"kind", func(m *Message) { m.Kind = "delivery.dispatch" }},
		{"topic", func(m *Message) { m.Topic = " " }},
		{"payload", func(m *Message) { m.Payload = "{" }},
		{"headers", func(m *Message) { m.Headers = "not-json" }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			message := valid
			tc.mutate(&message)
			if err := validateMessage(message, 2); err == nil {
				t.Fatal("invalid message accepted")
			}
		})
	}
}

func TestBoundedDiagnostic(t *testing.T) {
	if got := boundedDiagnostic("  a\n\tb\x00c  "); got != "a b c" {
		t.Fatalf("diagnostic = %q", got)
	}
	if got := boundedDiagnostic(strings.Repeat("x", 600)); len(got) != diagnosticLimit {
		t.Fatalf("diagnostic length = %d", len(got))
	}
	if got := boundedDiagnostic(" \n\t "); got != "" {
		t.Fatalf("blank diagnostic = %q", got)
	}
	if got := boundedDiagnostic("\xff\xfevalid\x00"); !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 survived normalization: %q", got)
	}
}

func TestBoundedDiagnosticUTF8(t *testing.T) {
	t.Run("below limit", func(t *testing.T) {
		got := boundedDiagnostic("destination timed out")
		if got != "destination timed out" {
			t.Fatalf("diagnostic = %q", got)
		}
	})
	t.Run("ascii above limit", func(t *testing.T) {
		got := boundedDiagnostic(strings.Repeat("x", 900))
		if len([]rune(got)) != diagnosticLimit || !utf8.ValidString(got) {
			t.Fatalf("diagnostic runes = %d, valid = %t", len([]rune(got)), utf8.ValidString(got))
		}
	})
	t.Run("multibyte ending exactly at boundary", func(t *testing.T) {
		want := strings.Repeat("x", diagnosticLimit-1) + "é"
		got := boundedDiagnostic(want)
		if got != want {
			t.Fatalf("exact-boundary multibyte diagnostic was altered")
		}
		if len([]rune(got)) != diagnosticLimit || !utf8.ValidString(got) {
			t.Fatalf("diagnostic runes = %d, valid = %t", len([]rune(got)), utf8.ValidString(got))
		}
	})
	t.Run("multibyte crossing byte limit", func(t *testing.T) {
		// A byte-based truncation at 500 would split the final 'é'; the
		// character-based limit keeps the result valid UTF-8.
		got := boundedDiagnostic(strings.Repeat("x", diagnosticLimit) + "é")
		if len([]rune(got)) != diagnosticLimit || !utf8.ValidString(got) {
			t.Fatalf("diagnostic runes = %d, valid = %t", len([]rune(got)), utf8.ValidString(got))
		}
	})
	t.Run("mixed ascii and non-ascii", func(t *testing.T) {
		got := boundedDiagnostic(strings.Repeat("é", 300) + strings.Repeat("a", 300))
		if len([]rune(got)) != diagnosticLimit || !utf8.ValidString(got) {
			t.Fatalf("diagnostic runes = %d, valid = %t", len([]rune(got)), utf8.ValidString(got))
		}
	})
}

func TestBoundedDiagnosticCredentials(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		secret string
	}{
		{"authorization", "request failed Authorization: Bearer sk_live_12345", "sk_live_12345"},
		{"authorization basic", "Authorization: Basic dXNlcjpwYXNz", "dXNlcjpwYXNz"},
		{"bearer only", "upstream said Bearer abc.def-123", "abc.def-123"},
		{"api key", "API_KEY=abcdef123", "abcdef123"},
		{"prefixed api key", "x-api-key: xak987", "xak987"},
		{"access token", "access_token: tok123", "tok123"},
		{"refresh token", `"refresh-token":"ref456"`, "ref456"},
		{"password", "password=hunter2", "hunter2"},
		{"client secret", "client_secret=shhh", "shhh"},
		{"secret", "secret: topsecret", "topsecret"},
		{"cookie", "Cookie: session=cookieSecret; theme=dark", "cookieSecret"},
		{"set cookie", "Set-Cookie: sid=setSecret; Path=/; HttpOnly", "setSecret"},
		{"quoted json password", `{"Password":"jsonSecret"}`, "jsonSecret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := boundedDiagnostic(tc.input)
			if strings.Contains(got, tc.secret) {
				t.Fatalf("secret %q survived scrubbing: %q", tc.secret, got)
			}
			if !strings.Contains(got, "[REDACTED]") {
				t.Fatalf("diagnostic was not redacted: %q", got)
			}
		})
	}

	t.Run("preserves context", func(t *testing.T) {
		got := boundedDiagnostic("request failed Authorization: Bearer sk_live_12345 after 2 retries")
		if !strings.Contains(got, "request failed") || !strings.Contains(got, "after 2 retries") {
			t.Fatalf("non-sensitive context was lost: %q", got)
		}
	})
	t.Run("long secret", func(t *testing.T) {
		secret := strings.Repeat("s", 900)
		got := boundedDiagnostic("password=" + secret)
		if strings.Contains(got, secret) || len([]rune(got)) > diagnosticLimit {
			t.Fatalf("long secret survived or limit exceeded: %d runes", len([]rune(got)))
		}
	})
}

func TestRandomToken(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for i := 0; i < 128; i++ {
		token, err := RandomToken()
		if err != nil {
			t.Fatalf("RandomToken() error: %v", err)
		}
		if !validUUID(token) {
			t.Fatalf("token is not a UUID: %q", token)
		}
		if token[14] != '4' {
			t.Fatalf("token version = %c", token[14])
		}
		if _, exists := seen[token]; exists {
			t.Fatalf("duplicate token %s", token)
		}
		seen[token] = struct{}{}
	}
}
