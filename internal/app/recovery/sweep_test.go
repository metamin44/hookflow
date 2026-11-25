package recovery

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/metamin44/hookflow/internal/platform/config"
)

func testOptions() Options {
	return Options{
		Budget: Budget{
			Total:  time.Second,
			Expire: PassBudget{MaxCandidates: 1, MaxDuration: 250 * time.Millisecond},
			Repair: PassBudget{MaxCandidates: 1, MaxDuration: 250 * time.Millisecond},
			ReArm:  PassBudget{MaxCandidates: 1, MaxDuration: 250 * time.Millisecond},
			Stale:  PassBudget{MaxCandidates: 1, MaxDuration: 250 * time.Millisecond},
		},
		StalePublishedAfter: time.Minute,
		BuildMessage: func(d Delivery) (Message, error) {
			return Message{
				ID:       "018f0000-0000-7000-8000-000000000001",
				Sequence: d.DispatchSeq,
				Kind:     "delivery.dispatch",
				Topic:    "hookflow.deliveries",
				Payload:  `{"delivery_id":"placeholder"}`,
			}, nil
		},
		Logger: slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
}

func TestBudgetValidate(t *testing.T) {
	if err := testOptions().Budget.Validate(); err != nil {
		t.Fatalf("valid budget rejected: %v", err)
	}

	cases := []struct {
		name   string
		mutate func(*Budget)
	}{
		{"zero total", func(b *Budget) { b.Total = 0 }},
		{"zero candidates", func(b *Budget) { b.Repair.MaxCandidates = 0 }},
		{"zero duration", func(b *Budget) { b.Stale.MaxDuration = 0 }},
		{"sum exceeds total", func(b *Budget) { b.Total = 500 * time.Millisecond }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b := testOptions().Budget
			tc.mutate(&b)
			if err := b.Validate(); err == nil {
				t.Fatal("invalid budget accepted")
			}
		})
	}
}

func TestBudgetValidateMatchesConfigValidation(t *testing.T) {
	cases := []struct {
		name  string
		total time.Duration
		pass  time.Duration
		valid bool
	}{
		{"below total", 5 * time.Second, time.Second, true},
		{"equal to total", 4 * time.Second, time.Second, true},
		{"above total", 3 * time.Second, time.Second, false},
		{"overflow", time.Duration(1<<63 - 1), time.Duration(1 << 62), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			budget := Budget{
				Total:  tc.total,
				Expire: PassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				Repair: PassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				ReArm:  PassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				Stale:  PassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
			}
			recoveryConfig := config.Recovery{
				Cadence:             tc.total + time.Second,
				Total:               tc.total,
				Expire:              config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				Repair:              config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				ReArm:               config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				Stale:               config.RecoveryPassBudget{MaxCandidates: 1, MaxDuration: tc.pass},
				StalePublishedAfter: time.Minute,
			}
			budgetErr := budget.Validate()
			configErr := recoveryConfig.Validate()
			if (budgetErr == nil) != tc.valid {
				t.Fatalf("Budget.Validate() error = %v, want valid=%t", budgetErr, tc.valid)
			}
			if (configErr == nil) != tc.valid {
				t.Fatalf("Recovery.Validate() error = %v, want valid=%t", configErr, tc.valid)
			}
		})
	}
}

type stubBeginner struct {
	begins int
}

func (s *stubBeginner) Begin(context.Context) (pgx.Tx, error) {
	s.begins++
	return nil, errors.New("unexpected begin")
}

func TestNewSweepRequiresExplicitInputs(t *testing.T) {
	db := &stubBeginner{}

	if _, err := NewSweep(nil, testOptions()); err == nil {
		t.Fatal("nil database accepted")
	}

	opts := testOptions()
	opts.StalePublishedAfter = 0
	if _, err := NewSweep(db, opts); err == nil {
		t.Fatal("zero stale threshold accepted")
	}

	opts = testOptions()
	opts.BuildMessage = nil
	if _, err := NewSweep(db, opts); err == nil {
		t.Fatal("nil message builder accepted")
	}

	opts = testOptions()
	opts.Budget.Total = 0
	if _, err := NewSweep(db, opts); err == nil {
		t.Fatal("invalid budget accepted")
	}

	if _, err := NewSweep(db, testOptions()); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
}

func TestRunHonorsCanceledContext(t *testing.T) {
	db := &stubBeginner{}
	sweep, err := NewSweep(db, testOptions())
	if err != nil {
		t.Fatalf("NewSweep() error: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = sweep.Run(ctx)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run() error = %v, want context.Canceled", err)
	}
	if db.begins != 0 {
		t.Fatalf("Run() began %d transactions after cancellation", db.begins)
	}
}

func TestValidateMessage(t *testing.T) {
	valid := Message{
		ID:       "018f0000-0000-7000-8000-000000000001",
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
		{"bad id", func(m *Message) { m.ID = "not-a-uuid" }},
		{"sequence mismatch", func(m *Message) { m.Sequence = 3 }},
		{"kind", func(m *Message) { m.Kind = "delivery.cancel" }},
		{"topic", func(m *Message) { m.Topic = "  " }},
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

func TestEligibilityPredicates(t *testing.T) {
	now := time.Date(2026, 11, 23, 12, 0, 0, 0, time.UTC)
	base := deliveryRow{
		state:          "pending",
		dispatchSeq:    1,
		nextAttemptAt:  now.Add(-time.Minute),
		createdAt:      now.Add(-time.Hour),
		eventExpiresAt: now.Add(time.Hour),
	}

	if !repairEligible(base, now) {
		t.Fatal("due, unleased, unexpired delivery was not repair eligible")
	}
	future := base
	future.nextAttemptAt = now.Add(time.Minute)
	if repairEligible(future, now) {
		t.Fatal("future-gated delivery was repair eligible")
	}
	leased := base
	leased.leaseToken = "018f0000-0000-7000-8000-000000000002"
	if repairEligible(leased, now) {
		t.Fatal("leased delivery was repair eligible")
	}
	expired := base
	expired.eventExpiresAt = now
	if repairEligible(expired, now) || rearmEligible(expired, now) {
		t.Fatal("expired event was eligible for repair or re-arm")
	}
	if !rearmEligible(base, now) {
		t.Fatal("due, unleased, unexpired delivery was not re-arm eligible")
	}

	rows := map[int]outboxState{
		1: {
			id:          "018f0000-0000-7000-8000-000000000003",
			sequence:    1,
			status:      "published",
			publishedAt: pgtype.Timestamptz{Time: now.Add(-2 * time.Minute), Valid: true},
		},
	}
	if !staleEligible(base, rows, now, time.Minute) {
		t.Fatal("old published current generation was not stale eligible")
	}
	recent := map[int]outboxState{
		1: {
			status:      "published",
			publishedAt: pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true},
		},
	}
	if staleEligible(base, recent, now, time.Minute) {
		t.Fatal("recent published current generation was stale eligible")
	}
	pending := map[int]outboxState{1: {status: "pending"}}
	if staleEligible(base, pending, now, time.Minute) {
		t.Fatal("pending current generation was stale eligible")
	}
	expiredLease := leased
	expiredLease.leaseExpiresAt = pgtype.Timestamptz{Time: now.Add(-time.Second), Valid: true}
	if !staleEligible(expiredLease, nil, now, time.Minute) {
		t.Fatal("expired worker lease was not stale eligible")
	}
	liveLease := leased
	liveLease.leaseExpiresAt = pgtype.Timestamptz{Time: now.Add(time.Second), Valid: true}
	if staleEligible(liveLease, nil, now, time.Minute) {
		t.Fatal("live worker lease was stale eligible")
	}
}

func TestDeferrableErrors(t *testing.T) {
	for _, code := range []string{"55P03", "40001", "40P01"} {
		err := &pgconn.PgError{Code: code}
		if !isDeferrable(err) {
			t.Fatalf("code %s was not deferrable", code)
		}
		if !isDeferrable(fmt.Errorf("wrapped: %w", err)) {
			t.Fatalf("wrapped code %s was not deferrable", code)
		}
	}
	if isDeferrable(&pgconn.PgError{Code: "23505"}) {
		t.Fatal("unique violation was treated as deferrable contention")
	}
	if !isDeferrable(context.DeadlineExceeded) {
		t.Fatal("deadline exceeded was not deferrable")
	}
}

func TestNullableJSON(t *testing.T) {
	if nullableJSON("") != nil {
		t.Fatal("empty headers were not nil")
	}
	if got := nullableJSON(`{"a":1}`); got != `{"a":1}` {
		t.Fatalf("headers = %v", got)
	}
}
