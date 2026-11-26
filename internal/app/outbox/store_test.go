package outbox

import (
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewJSONHandler(io.Discard, nil))
}

func TestClaimRejectsIncompleteOptions(t *testing.T) {
	store := NewStore(nil, testLogger())
	valid := ClaimOptions{
		BatchSize:     1,
		MaxClaims:     2,
		LeaseDuration: time.Minute,
		ProcessID:     "publisher-test",
		NewToken:      func() (string, error) { return "00000000-0000-4000-8000-000000000001", nil },
	}

	tests := []struct {
		name   string
		mutate func(*ClaimOptions)
	}{
		{name: "batch", mutate: func(o *ClaimOptions) { o.BatchSize = 0 }},
		{name: "max claims", mutate: func(o *ClaimOptions) { o.MaxClaims = 0 }},
		{name: "lease", mutate: func(o *ClaimOptions) { o.LeaseDuration = 0 }},
		{name: "process", mutate: func(o *ClaimOptions) { o.ProcessID = "  " }},
		{name: "token generator", mutate: func(o *ClaimOptions) { o.NewToken = nil }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := valid
			tt.mutate(&opts)
			if _, err := store.Claim(context.Background(), opts); err == nil {
				t.Fatal("Claim() accepted incomplete options")
			}
		})
	}
}

func TestMarkFailedRejectsInvalidInputBeforeDatabaseUse(t *testing.T) {
	store := NewStore(nil, testLogger())
	claim := Claim{
		MessageID:  "00000000-0000-4000-8000-000000000001",
		Owner:      "publisher-test",
		MaxClaims:  2,
		ClaimCount: 1,
		LeaseToken: "00000000-0000-4000-8000-000000000002",
		ClaimedAt:  time.Now(),
	}

	if _, err := store.MarkFailed(context.Background(), Failure{Claim: claim, Backoff: 0, Error: "failure"}); err == nil {
		t.Fatal("MarkFailed() accepted a non-positive backoff")
	}
	if _, err := store.MarkFailed(context.Background(), Failure{Claim: claim, Backoff: time.Second, Error: " \n\t "}); err == nil {
		t.Fatal("MarkFailed() accepted an empty diagnostic")
	}
}

func TestBoundedDiagnostic(t *testing.T) {
	got, err := boundedDiagnostic(" connect\tfailed\nwith\r\ncontrol ")
	if err != nil {
		t.Fatalf("boundedDiagnostic() error: %v", err)
	}
	if got != "connect failed with control" {
		t.Fatalf("boundedDiagnostic() = %q", got)
	}

	long := strings.Repeat("x", 700)
	got, err = boundedDiagnostic(long)
	if err != nil {
		t.Fatalf("boundedDiagnostic(long) error: %v", err)
	}
	if len([]rune(got)) != 500 {
		t.Fatalf("bounded diagnostic length = %d, want 500", len([]rune(got)))
	}
}

func TestRandomTokenShapeAndUniqueness(t *testing.T) {
	seen := make(map[string]struct{}, 128)
	for range 128 {
		token, err := RandomToken()
		if err != nil {
			t.Fatalf("RandomToken() error: %v", err)
		}
		if !validUUID(token) {
			t.Fatalf("RandomToken() returned an invalid UUID: %q", token)
		}
		if token[14] != '4' {
			t.Fatalf("RandomToken() version nibble = %c, want 4", token[14])
		}
		switch token[19] {
		case '8', '9', 'a', 'b':
		default:
			t.Fatalf("RandomToken() variant nibble = %c, want 8, 9, a or b", token[19])
		}
		if _, exists := seen[token]; exists {
			t.Fatalf("RandomToken() repeated a token")
		}
		seen[token] = struct{}{}
	}
}

func TestValidUUIDRejectsMalformedValues(t *testing.T) {
	for _, value := range []string{
		"",
		"not-a-uuid",
		"00000000-0000-4000-8000-00000000000",
		"00000000-0000-4000-8000-0000000000000",
		"00000000_0000-4000-8000-000000000000",
		"gggggggg-0000-4000-8000-000000000000",
	} {
		if validUUID(value) {
			t.Fatalf("validUUID(%q) = true", value)
		}
	}
	if !validUUID("00000000-0000-4000-8000-000000000000") {
		t.Fatal("validUUID() rejected a canonical UUID")
	}
}
