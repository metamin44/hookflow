package worker

import (
	"math"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func statusPointer(value int) *int { return &value }

func TestNewRetryPolicyValidation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		max     int
		horizon time.Duration
	}{
		{name: "zero attempts", max: 0, horizon: time.Hour},
		{name: "negative attempts", max: -1, horizon: time.Hour},
		{name: "too many attempts", max: 9, horizon: time.Hour},
		{name: "zero horizon", max: 8, horizon: 0},
		{name: "negative horizon", max: 8, horizon: -time.Second},
		{name: "horizon over twelve hours", max: 8, horizon: 12*time.Hour + time.Nanosecond},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if policy, err := NewRetryPolicy(tt.max, tt.horizon); err == nil || policy != nil {
				t.Fatalf("NewRetryPolicy(%d, %s) = (%v, %v), want nil policy and error", tt.max, tt.horizon, policy, err)
			}
		})
	}

	for _, tt := range []struct {
		max     int
		horizon time.Duration
	}{
		{max: 1, horizon: time.Nanosecond},
		{max: 8, horizon: 12 * time.Hour},
	} {
		policy, err := NewRetryPolicy(tt.max, tt.horizon)
		if err != nil || policy == nil {
			t.Fatalf("NewRetryPolicy(%d, %s) = (%v, %v), want valid policy", tt.max, tt.horizon, policy, err)
		}
		if policy.MaxAttempts != tt.max || policy.Horizon != tt.horizon {
			t.Fatalf("policy = %+v, want max %d and horizon %s", policy, tt.max, tt.horizon)
		}
	}
}

func TestRetryPolicyApprovedLadderAndDeterministicJitter(t *testing.T) {
	t.Parallel()

	policy, err := NewRetryPolicy(8, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	ladder := []time.Duration{
		10 * time.Second,
		30 * time.Second,
		2 * time.Minute,
		5 * time.Minute,
		15 * time.Minute,
		time.Hour,
		4 * time.Hour,
		12 * time.Hour,
	}
	golden := []time.Duration{
		8928999345,
		23175841876,
		115076835414,
		242548280920,
		722970933201,
		3532049024408,
		9363938217305,
	}
	for attempt, step := range ladder {
		attemptNumber := attempt + 1
		t.Run(strconv.Itoa(attemptNumber), func(t *testing.T) {
			t.Parallel()
			first := policy.Decision("delivery-a", attemptNumber, statusPointer(503), "")
			second := policy.Decision("delivery-a", attemptNumber, statusPointer(503), "")
			if attemptNumber == 8 {
				if first.Retry || first.Backoff != 0 {
					t.Fatalf("attempt 8 decision = %+v, want no retry", first)
				}
				return
			}
			if !first.Retry {
				t.Fatal("expected retry before the attempt cap")
			}
			if first != second {
				t.Fatalf("decision is not deterministic: %+v != %+v", first, second)
			}
			if first.Backoff < step/2 || first.Backoff > step {
				t.Fatalf("backoff %s outside [%s, %s]", first.Backoff, step/2, step)
			}
			if first.Backoff != golden[attempt] {
				t.Fatalf("backoff = %dns, want deterministic golden %dns", first.Backoff, golden[attempt])
			}
		})
	}

	other := policy.Decision("delivery-b", 1, statusPointer(503), "")
	if other == policy.Decision("delivery-a", 1, statusPointer(503), "") {
		t.Fatal("different delivery IDs unexpectedly produced the same jitter")
	}
}

func TestRetryPolicyAttemptCapAndInvalidAttempts(t *testing.T) {
	t.Parallel()

	policy, err := NewRetryPolicy(3, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	for _, attempt := range []int{-1, 0, 3, 4, 8, 100} {
		decision := policy.Decision("delivery-a", attempt, statusPointer(503), "")
		if decision.Retry || decision.Backoff != 0 {
			t.Errorf("attempt %d decision = %+v, want no retry", attempt, decision)
		}
	}
}

func TestRetryPolicyRetryAfterSeconds(t *testing.T) {
	t.Parallel()

	policy, err := NewRetryPolicy(8, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name   string
		status *int
		value  string
		want   time.Duration
	}{
		{name: "seconds override", status: statusPointer(429), value: "45", want: 45 * time.Second},
		{name: "whitespace and leading zero", status: statusPointer(429), value: " 0045 ", want: 45 * time.Second},
		{name: "one hour cap", status: statusPointer(429), value: "7200", want: time.Hour},
		{name: "huge seconds cap", status: statusPointer(429), value: "999999999999999999999999999999999999", want: time.Hour},
		{name: "ignored on 503", status: statusPointer(503), value: "45"},
		{name: "ignored without status", value: "45"},
		{name: "negative fallback", status: statusPointer(429), value: "-1"},
		{name: "signed fallback", status: statusPointer(429), value: "+5"},
		{name: "fraction fallback", status: statusPointer(429), value: "1.5"},
		{name: "invalid fallback", status: statusPointer(429), value: "not-a-date"},
		{name: "empty fallback", status: statusPointer(429)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			decision := policy.Decision("delivery-a", 1, tt.status, tt.value)
			if !decision.Retry {
				t.Fatal("expected retry")
			}
			if tt.want != 0 {
				if decision.Backoff != tt.want {
					t.Fatalf("backoff = %s, want %s", decision.Backoff, tt.want)
				}
				return
			}
			if decision.Backoff < 5*time.Second || decision.Backoff > 10*time.Second {
				t.Fatalf("fallback backoff = %s, want ladder jitter in [5s, 10s]", decision.Backoff)
			}
		})
	}
}

func TestRetryPolicyRetryAfterHTTPDate(t *testing.T) {
	t.Parallel()

	policy, err := NewRetryPolicy(8, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	future := now.UTC().Add(30 * time.Minute).Truncate(time.Second)
	decision := policy.Decision("delivery-a", 1, statusPointer(429), future.Format(http.TimeFormat))
	if !decision.Retry {
		t.Fatal("expected retry")
	}
	if decision.Backoff <= 29*time.Minute || decision.Backoff > 30*time.Minute {
		t.Fatalf("HTTP-date backoff = %s, want approximately 30m", decision.Backoff)
	}

	capped := policy.Decision("delivery-a", 1, statusPointer(429), now.UTC().Add(2*time.Hour).Format(http.TimeFormat))
	if !capped.Retry || capped.Backoff <= 59*time.Minute || capped.Backoff > time.Hour {
		t.Fatalf("capped HTTP-date decision = %+v, want approximately one hour", capped)
	}

	for _, value := range []string{
		now.UTC().Add(-time.Minute).Format(http.TimeFormat),
		"Sun, 06 Nov 1994 08:49:37 GMT",
		"invalid",
	} {
		fallback := policy.Decision("delivery-a", 1, statusPointer(429), value)
		if !fallback.Retry || fallback.Backoff < 5*time.Second || fallback.Backoff > 10*time.Second {
			t.Errorf("Retry-After %q decision = %+v, want ladder fallback", value, fallback)
		}
	}
}

func TestRetryPolicyNeverExceedsHorizon(t *testing.T) {
	t.Parallel()

	policy, err := NewRetryPolicy(8, 7*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		status *int
		value  string
	}{
		{status: statusPointer(503)},
		{status: statusPointer(429), value: "45"},
		{status: statusPointer(429), value: time.Now().UTC().Add(time.Hour).Format(http.TimeFormat)},
	} {
		decision := policy.Decision("delivery-a", 1, tt.status, tt.value)
		if !decision.Retry || decision.Backoff != 7*time.Second {
			t.Errorf("decision = %+v, want retry capped at 7s", decision)
		}
	}
}

func TestDecryptFailurePacing(t *testing.T) {
	t.Parallel()

	const base = 30 * time.Second
	if got := DecryptFailurePacing("delivery-a", base); got != 32845050131 {
		t.Fatalf("pacing = %dns, want deterministic golden 32845050131ns", got)
	}
	policy, err := NewRetryPolicy(8, 12*time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	if got := policy.DecryptFailurePacing("delivery-a", base); got != DecryptFailurePacing("delivery-a", base) {
		t.Fatalf("policy pacing = %s, standalone pacing = %s", got, DecryptFailurePacing("delivery-a", base))
	}
	for _, deliveryID := range []string{"delivery-a", "delivery-b", "", "delivery-with-a-long-id"} {
		first := DecryptFailurePacing(deliveryID, base)
		second := DecryptFailurePacing(deliveryID, base)
		if first != second {
			t.Fatalf("pacing for %q is not deterministic: %s != %s", deliveryID, first, second)
		}
		if first < base || first > base+base/5 {
			t.Fatalf("pacing for %q = %s, want [%s, %s]", deliveryID, first, base, base+base/5)
		}
	}
	for _, invalid := range []time.Duration{0, -time.Nanosecond, -time.Second} {
		if got := DecryptFailurePacing("delivery-a", invalid); got != 0 {
			t.Errorf("pacing with base %s = %s, want zero", invalid, got)
		}
	}
}

func TestDecryptFailurePacingOverflowSafe(t *testing.T) {
	t.Parallel()

	got := DecryptFailurePacing("delivery-a", time.Duration(math.MaxInt64))
	if got <= 0 || got < time.Duration(math.MaxInt64) {
		t.Fatalf("large-base pacing = %s, want positive duration without wraparound", got)
	}
}
