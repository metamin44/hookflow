//go:build integration

package delivery

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestPreHTTPPermanentFailuresTerminalizeIntegration(t *testing.T) {
	permanent := []PreHTTPReason{
		ReasonSecretMissing,
		ReasonSecretInvalid,
		ReasonProviderDisallowed,
		ReasonSigningInternal,
		ReasonDestinationDisallowed,
	}
	for _, reason := range permanent {
		t.Run(string(reason), func(t *testing.T) {
			f := newIntegrationFixture(t)
			store := newTestStore(t, f, nil)
			d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
			claim := acquire(t, store, time.Minute, 3)

			result, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{
				Reason: reason,
				Error:  "pre-HTTP failure Authorization: Bearer super-secret-token",
			})
			if err != nil {
				t.Fatalf("RecordPreHTTPFailure() error: %v", err)
			}
			if !result.Applied || result.Fenced || result.Expired || !result.AckEligible {
				t.Fatalf("unexpected disposition result: %+v", result)
			}
			if result.Reason != reason {
				t.Fatalf("result reason = %s, want %s", result.Reason, reason)
			}

			row := f.readDelivery(t, d.id)
			if row.state != "failed" {
				t.Fatalf("state = %q, want failed", row.state)
			}
			if !row.completedAt.Valid {
				t.Fatal("completed_at was not set")
			}
			if row.nextAttemptAt.Valid {
				t.Fatalf("next_attempt_at = %+v, want NULL", row.nextAttemptAt)
			}
			if row.leaseToken != "" || row.leaseExpiresAt.Valid {
				t.Fatalf("lease was not cleared: %+v", row)
			}
			if row.dispatchSeq != claim.DispatchSeq {
				t.Fatalf("dispatch_seq advanced to %d", row.dispatchSeq)
			}
			if row.attemptCount != claim.AttemptCount {
				t.Fatalf("attempt_count changed to %d", row.attemptCount)
			}
			if row.lastAttemptAt.Valid {
				t.Fatal("permanent pre-HTTP failure set last_attempt_at")
			}
			if !row.lastError.Valid || row.lastError.String == "" {
				t.Fatal("permanent pre-HTTP failure did not record a bounded reason")
			}
			if strings.Contains(row.lastError.String, "super-secret-token") || !strings.Contains(row.lastError.String, "[REDACTED]") {
				t.Fatalf("last_error was not scrubbed: %q", row.lastError.String)
			}
			if n := f.attemptCount(t, d.id); n != 0 {
				t.Fatalf("permanent pre-HTTP failure wrote %d attempt rows", n)
			}
			if rows := f.outboxRows(t, d.id); len(rows) != 0 {
				t.Fatalf("permanent pre-HTTP failure wrote %d outbox rows", len(rows))
			}

			// A repeated disposition is fenced and writes nothing further.
			again, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{
				Reason: reason,
				Error:  "pre-HTTP failure",
			})
			if err != nil {
				t.Fatalf("repeated RecordPreHTTPFailure() error: %v", err)
			}
			if !again.Fenced || again.Applied || !again.AckEligible {
				t.Fatalf("repeated disposition result = %+v", again)
			}
			row = f.readDelivery(t, d.id)
			if row.state != "failed" || row.dispatchSeq != claim.DispatchSeq || row.attemptCount != claim.AttemptCount {
				t.Fatalf("repeated disposition mutated the delivery: %+v", row)
			}
		})
	}
}

func TestPreHTTPPermanentFailureBoundsTheDiagnosticIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	long := strings.Repeat("é", diagnosticLimit+100)
	if _, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{
		Reason: ReasonSecretInvalid,
		Error:  long,
	}); err != nil {
		t.Fatalf("RecordPreHTTPFailure() error: %v", err)
	}
	row := f.readDelivery(t, d.id)
	if !row.lastError.Valid {
		t.Fatal("last_error is NULL")
	}
	if runes := utf8.RuneCountInString(row.lastError.String); runes > diagnosticLimit {
		t.Fatalf("last_error has %d runes, want <= %d", runes, diagnosticLimit)
	}
	if !utf8.ValidString(row.lastError.String) {
		t.Fatal("last_error is not valid UTF-8")
	}
}

func TestPreHTTPPermanentFailureEventExpiryPrecedenceIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)
	f.expireEvent(t, d.eventID)

	result, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{
		Reason: ReasonSecretMissing,
		Error:  "stored webhook secret is empty",
	})
	if err != nil {
		t.Fatalf("RecordPreHTTPFailure() error: %v", err)
	}
	if !result.Applied || !result.Expired || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}
	row := f.readDelivery(t, d.id)
	if row.state != "expired" {
		t.Fatalf("state = %q, want expired", row.state)
	}
	if !row.completedAt.Valid || row.nextAttemptAt.Valid {
		t.Fatalf("expiry did not terminalize: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("expiry did not clear the lease: %+v", row)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("expiry wrote %d attempt rows", n)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("expiry wrote %d outbox rows", len(rows))
	}
}

func TestPreHTTPTransientFailureUsesPacedRetryIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	pacing := 30 * time.Second
	result, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{
		Reason: ReasonSecretUnavailable,
		Pacing: pacing,
		Error:  "webhook secret provider is temporarily unavailable",
	})
	if err != nil {
		t.Fatalf("RecordPreHTTPFailure() error: %v", err)
	}
	if !result.Applied || result.Fenced || result.Expired || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}
	if result.RetrySequence != claim.DispatchSeq+1 {
		t.Fatalf("retry sequence = %d, want %d", result.RetrySequence, claim.DispatchSeq+1)
	}
	requireTimeNear(t, "disposition gate", result.NextAttemptAt, time.Now().Add(pacing), 5*time.Second)

	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.dispatchSeq != claim.DispatchSeq+1 || row.attemptCount != claim.AttemptCount {
		t.Fatalf("transient disposition changed accounting: %+v", row)
	}
	if row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("transient disposition did not clear the lease: %+v", row)
	}
	if row.lastAttemptAt.Valid {
		t.Fatal("transient disposition set last_attempt_at")
	}
	if !row.lastError.Valid || !strings.Contains(row.lastError.String, "temporarily unavailable") {
		t.Fatalf("transient disposition last_error = %+v", row.lastError)
	}
	requireTimestamptzNear(t, "delivery gate", row.nextAttemptAt, result.NextAttemptAt, 2*time.Millisecond)
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("transient disposition wrote %d attempt rows", n)
	}
	rows := f.outboxRows(t, d.id)
	if len(rows) != 1 {
		t.Fatalf("transient disposition outbox rows = %d, want 1", len(rows))
	}
	outbox := rows[0]
	if outbox.sequence != claim.DispatchSeq+1 || outbox.kind != "delivery.retry" || outbox.status != "pending" {
		t.Fatalf("transient retry outbox = %+v", outbox)
	}
	requireTimeNear(t, "outbox gate", outbox.availableAt, result.NextAttemptAt, 2*time.Millisecond)
}

func TestPreHTTPTransientFailureNearExpiryCreatesNoDispatchIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)

	f.setEventExpiresAt(t, d.eventID, time.Now().Add(2*time.Second))
	eventExpiry := f.eventExpiresAt(t, d.eventID)

	result, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{
		Reason: ReasonSecretUnavailable,
		Pacing: 10 * time.Second,
		Error:  "webhook secret provider is temporarily unavailable",
	})
	if err != nil {
		t.Fatalf("RecordPreHTTPFailure() error: %v", err)
	}
	if !result.Applied || result.Expired || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}
	if result.RetrySequence != claim.DispatchSeq {
		t.Fatalf("retry sequence advanced to %d", result.RetrySequence)
	}
	requireTimeNear(t, "disposition gate", result.NextAttemptAt, eventExpiry, 2*time.Millisecond)
	row := f.readDelivery(t, d.id)
	if row.state != "pending" || row.dispatchSeq != claim.DispatchSeq {
		t.Fatalf("near-expiry disposition mutated the delivery: %+v", row)
	}
	requireTimestamptzNear(t, "delivery gate", row.nextAttemptAt, eventExpiry, 2*time.Millisecond)
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("near-expiry disposition created %d outbox rows", len(rows))
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("near-expiry disposition wrote %d attempt rows", n)
	}
}

func TestPreHTTPTransientFailureWithExpiredLeaseExpiresIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	now := time.Now()
	token := testUUID(t)
	leaseExpiry := now.Add(-time.Minute)
	d := f.seedDelivery(t, deliverySeed{
		nextAttemptAt:  now.Add(-time.Minute),
		leaseToken:     token,
		leaseExpiresAt: leaseExpiry,
		eventReceived:  now.Add(-2 * time.Hour),
		eventExpiresAt: now.Add(time.Hour),
	})
	claim := Claim{
		OrganizationID: f.orgID,
		DeliveryID:     d.id,
		EventID:        d.eventID,
		EndpointID:     f.endpointID,
		DispatchSeq:    1,
		LeaseToken:     token,
		LeaseExpiresAt: leaseExpiry,
	}

	result, err := store.RecordPreHTTPFailure(context.Background(), claim, PreHTTPFailureOptions{
		Reason: ReasonSecretUnavailable,
		Pacing: 30 * time.Second,
		Error:  "webhook secret provider is temporarily unavailable",
	})
	if err != nil {
		t.Fatalf("RecordPreHTTPFailure() error: %v", err)
	}
	if !result.Applied || !result.Expired || result.Fenced || !result.AckEligible {
		t.Fatalf("unexpected disposition result: %+v", result)
	}
	row := f.readDelivery(t, d.id)
	if row.state != "expired" || !row.completedAt.Valid || row.nextAttemptAt.Valid {
		t.Fatalf("expired lease did not terminalize: %+v", row)
	}
	if row.dispatchSeq != 1 || row.attemptCount != 0 {
		t.Fatalf("expired lease changed accounting: %+v", row)
	}
	if rows := f.outboxRows(t, d.id); len(rows) != 0 {
		t.Fatalf("expired lease wrote %d outbox rows", len(rows))
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("expired lease wrote %d attempt rows", n)
	}
}

func TestPreHTTPFailureFencingIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	cases := []struct {
		name   string
		tamper func(*Claim)
	}{
		{"wrong token", func(c *Claim) { c.LeaseToken = testUUID(t) }},
		{"wrong generation", func(c *Claim) { c.DispatchSeq++ }},
		{"wrong organization", func(c *Claim) { c.OrganizationID = testUUID(t) }},
		{"wrong event", func(c *Claim) { c.EventID = testUUID(t) }},
		{"missing delivery", func(c *Claim) { c.DeliveryID = testUUID(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
			claim := acquire(t, store, time.Minute, 3)
			tampered := *claim
			tc.tamper(&tampered)

			result, err := store.RecordPreHTTPFailure(context.Background(), tampered, PreHTTPFailureOptions{
				Reason: ReasonSecretInvalid,
				Error:  "stored webhook secret envelope is invalid",
			})
			if err != nil {
				t.Fatalf("RecordPreHTTPFailure() error: %v", err)
			}
			if !result.Fenced || result.Applied || !result.AckEligible {
				t.Fatalf("fenced disposition result = %+v", result)
			}
			row := f.readDelivery(t, d.id)
			if row.state != "pending" || row.dispatchSeq != 1 || row.attemptCount != 0 {
				t.Fatalf("fenced disposition mutated the delivery: %+v", row)
			}
			if row.leaseToken != claim.LeaseToken {
				t.Fatalf("fenced disposition changed the lease: %+v", row)
			}
			if row.lastError.Valid {
				t.Fatalf("fenced disposition wrote last_error = %q", row.lastError.String)
			}
			if n := f.attemptCount(t, d.id); n != 0 {
				t.Fatalf("fenced disposition wrote %d attempt rows", n)
			}
			if rows := f.outboxRows(t, d.id); len(rows) != 0 {
				t.Fatalf("fenced disposition wrote %d outbox rows", len(rows))
			}
		})
	}
}

func TestPreHTTPFailureRejectsUnapprovedInputIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	d := f.seedDelivery(t, deliverySeed{nextAttemptAt: time.Now().Add(-time.Minute)})
	claim := acquire(t, store, time.Minute, 3)
	before := f.readDelivery(t, d.id)

	if _, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{Reason: "unapproved"}); err == nil {
		t.Fatal("unapproved reason accepted")
	}
	if _, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{Reason: ReasonSecretUnavailable}); err == nil {
		t.Fatal("transient reason without pacing accepted")
	}
	if _, err := store.RecordPreHTTPFailure(context.Background(), *claim, PreHTTPFailureOptions{Reason: ReasonSecretInvalid, Pacing: time.Second}); err != nil {
		t.Fatalf("permanent reason with pacing rejected: %v", err)
	}
	after := f.readDelivery(t, d.id)
	if after.state != "failed" || after.dispatchSeq != before.dispatchSeq || after.attemptCount != before.attemptCount {
		t.Fatalf("terminal disposition after rejected inputs = %+v", after)
	}
}
