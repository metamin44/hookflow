package operations

import (
	"math"
	"testing"
	"time"
)

func TestWindowPrevious(t *testing.T) {
	to := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	window := Window{From: to.Add(-24 * time.Hour), To: to}
	previous := window.Previous()
	if !previous.To.Equal(window.From) {
		t.Fatalf("previous.To = %s, want %s", previous.To, window.From)
	}
	if !previous.From.Equal(to.Add(-48 * time.Hour)) {
		t.Fatalf("previous.From = %s", previous.From)
	}
	if previous.Duration() != window.Duration() {
		t.Fatalf("previous duration = %s, want %s", previous.Duration(), window.Duration())
	}
}

func TestRatio(t *testing.T) {
	cases := []struct {
		name  string
		part  int64
		whole int64
		want  *float64
	}{
		{name: "zero denominator is unavailable", part: 0, whole: 0},
		{name: "negative denominator is unavailable", part: 1, whole: -1},
		{name: "all succeeded", part: 4, whole: 4, want: floatPtr(1)},
		{name: "none succeeded", part: 0, whole: 5, want: floatPtr(0)},
		{name: "rounded to four places", part: 2, whole: 3, want: floatPtr(0.6667)},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := Ratio(testCase.part, testCase.whole)
			if testCase.want == nil {
				if got != nil {
					t.Fatalf("Ratio() = %v, want nil", *got)
				}
				return
			}
			if got == nil {
				t.Fatalf("Ratio() = nil, want %v", *testCase.want)
			}
			if math.Abs(*got-*testCase.want) > 1e-9 {
				t.Fatalf("Ratio() = %v, want %v", *got, *testCase.want)
			}
		})
	}
}

func TestDeliveryCountsDenominator(t *testing.T) {
	counts := DeliveryCounts{Total: 10, Initial: 8, Retry: 2, Pending: 1, Retrying: 1,
		Succeeded: 6, Failed: 1, Expired: 1}
	if counts.Terminal() != 8 {
		t.Fatalf("Terminal() = %d, want 8", counts.Terminal())
	}
	rate := counts.SuccessRate()
	if rate == nil || math.Abs(*rate-0.75) > 1e-9 {
		t.Fatalf("SuccessRate() = %v, want 0.75", rate)
	}
	empty := DeliveryCounts{Total: 3, Pending: 3}
	if empty.Terminal() != 0 || empty.SuccessRate() != nil {
		t.Fatalf("pending-only cohort must have no success rate")
	}
}

func TestRetryEffectivenessDenominator(t *testing.T) {
	retries := RetryEffectiveness{Generations: 5, Succeeded: 2, Failed: 1, Expired: 1, InProgress: 1}
	if retries.Terminal() != 4 {
		t.Fatalf("Terminal() = %d, want 4", retries.Terminal())
	}
	rate := retries.SuccessRate()
	if rate == nil || math.Abs(*rate-0.5) > 1e-9 {
		t.Fatalf("SuccessRate() = %v, want 0.5", rate)
	}
}

func TestEndpointHealthTerminal(t *testing.T) {
	health := EndpointHealth{Deliveries: 4, Succeeded: 1, Failed: 2, Expired: 1}
	if health.Terminal() != 4 {
		t.Fatalf("Terminal() = %d", health.Terminal())
	}
	rate := health.SuccessRate()
	if rate == nil || math.Abs(*rate-0.25) > 1e-9 {
		t.Fatalf("SuccessRate() = %v, want 0.25", rate)
	}
}

func floatPtr(value float64) *float64 { return &value }
