//go:build integration

package delivery

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

func pointerAcquireOptions(lease time.Duration) AcquireOptions {
	return AcquireOptions{
		LeaseDuration: lease,
		MaxAttempts:   3,
		NewToken:      RandomToken,
	}
}

func seedPublishedPointer(t *testing.T, f *integrationFixture, spec deliverySeed) (seededDelivery, Pointer) {
	t.Helper()
	if spec.dispatchSeq == 0 {
		spec.dispatchSeq = 1
	}
	d := f.seedDelivery(t, spec)
	outboxID := f.insertOutbox(t, d.id, spec.dispatchSeq, "delivery.dispatch", time.Now().Add(-time.Minute))
	f.markOutboxPublished(t, outboxID)
	return d, Pointer{
		OrganizationID: f.orgID,
		DeliveryID:     d.id,
		OutboxID:       outboxID,
		DispatchSeq:    spec.dispatchSeq,
	}
}

func (f *integrationFixture) markOutboxPublished(t *testing.T, outboxID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`UPDATE outbox_messages SET status = 'published', published_at = clock_timestamp(), updated_at = clock_timestamp()
		  WHERE id = $1::uuid`, outboxID,
	); err != nil {
		t.Fatalf("publish outbox row: %v", err)
	}
}

func acquireExactPointer(t *testing.T, store *Store, pointer Pointer, lease time.Duration) *Claim {
	t.Helper()
	result, err := store.AcquirePointer(context.Background(), pointer, pointerAcquireOptions(lease))
	if err != nil {
		t.Fatalf("AcquirePointer() error: %v", err)
	}
	if result.Status != PointerAcquired || result.Claim == nil {
		t.Fatalf("AcquirePointer() = %+v, want acquired claim", result)
	}
	return result.Claim
}

func requirePointerUnchanged(t *testing.T, f *integrationFixture, d seededDelivery, before deliveryRow, beforeOutbox []outboxRow) {
	t.Helper()
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("delivery changed:\nbefore=%+v\nafter=%+v", before, after)
	}
	afterOutbox := f.outboxRows(t, d.id)
	if len(afterOutbox) != len(beforeOutbox) {
		t.Fatalf("outbox row count changed: before=%d after=%d", len(beforeOutbox), len(afterOutbox))
	}
	for i := range beforeOutbox {
		if afterOutbox[i] != beforeOutbox[i] {
			t.Fatalf("outbox row %d changed:\nbefore=%+v\nafter=%+v", i, beforeOutbox[i], afterOutbox[i])
		}
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("pointer operation wrote %d attempt rows", n)
	}
}

func (f *integrationFixture) deliveryUpdatedAt(t *testing.T, deliveryID string) time.Time {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	var updatedAt time.Time
	if err := f.pool.QueryRow(ctx,
		`SELECT updated_at FROM deliveries WHERE id = $1::uuid`, deliveryID,
	).Scan(&updatedAt); err != nil {
		t.Fatalf("read delivery updated_at: %v", err)
	}
	return updatedAt
}

func TestAcquirePointerExactBrokerPointerIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	now := time.Now()

	older, _ := seedPublishedPointer(t, f, deliverySeed{nextAttemptAt: now.Add(-2 * time.Minute)})
	exact, pointer := seedPublishedPointer(t, f, deliverySeed{
		nextAttemptAt: now.Add(-time.Minute),
		url:           "https://example.test/exact",
		headers:       `{"x-test":"exact"}`,
		secret:        "exact-ciphertext",
		payload:       `{"source":"exact"}`,
		eventType:     "test.exact",
	})

	claim := acquireExactPointer(t, store, pointer, time.Minute)
	if claim.OrganizationID != pointer.OrganizationID || claim.DeliveryID != pointer.DeliveryID ||
		claim.EventID != exact.eventID || claim.DispatchSeq != pointer.DispatchSeq {
		t.Fatalf("acquired the wrong pointer: %+v", claim)
	}
	if claim.RequestURL != "https://example.test/exact" || claim.RequestHeaders != `{"x-test": "exact"}` ||
		claim.SecretCiphertext != "exact-ciphertext" || claim.EventType != "test.exact" ||
		!strings.Contains(claim.EventPayload, "exact") {
		t.Fatalf("unexpected request snapshot: %+v", claim)
	}
	if !validUUID(claim.LeaseToken) || claim.LeaseExpiresAt.Sub(claim.ClaimedAt) != time.Minute {
		t.Fatalf("invalid fixed lease: %+v", claim)
	}
	if row := f.readDelivery(t, older.id); row.leaseToken != "" || row.leaseExpiresAt.Valid {
		t.Fatalf("eligible older delivery was leased: %+v", row)
	}
	row := f.readDelivery(t, exact.id)
	if row.state != "pending" || row.dispatchSeq != 1 || row.attemptCount != 0 || row.leaseToken != claim.LeaseToken {
		t.Fatalf("unexpected exact delivery state: %+v", row)
	}
	requireTimestamptzNear(t, "pointer lease expiry", row.leaseExpiresAt, claim.LeaseExpiresAt, 2*time.Millisecond)
	if n := f.attemptCount(t, exact.id); n != 0 {
		t.Fatalf("acquisition wrote %d attempts", n)
	}
	if rows := f.outboxRows(t, exact.id); len(rows) != 1 || rows[0].id != pointer.OutboxID || rows[0].status != "published" {
		t.Fatalf("acquisition changed the pointer outbox row: %+v", rows)
	}

	duplicate, err := store.AcquirePointer(context.Background(), pointer, pointerAcquireOptions(time.Minute))
	if err != nil {
		t.Fatalf("duplicate AcquirePointer() error: %v", err)
	}
	if duplicate.Status != PointerDuplicate || duplicate.Claim != nil {
		t.Fatalf("live lease result = %+v", duplicate)
	}
	if after := f.readDelivery(t, exact.id); after != row {
		t.Fatalf("duplicate acquisition changed the lease: before=%+v after=%+v", row, after)
	}

	// The legacy global API remains oldest-first and can still claim delivery A.
	global := acquire(t, store, time.Minute, 3)
	if global.DeliveryID != older.id {
		t.Fatalf("legacy Acquire() selected %s, want older delivery %s", global.DeliveryID, older.id)
	}
}

func TestAcquirePointerStatusesIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	now := time.Now()

	assertStatus := func(t *testing.T, d seededDelivery, pointer Pointer, want PointerStatus) {
		t.Helper()
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		result, err := store.AcquirePointer(context.Background(), pointer, pointerAcquireOptions(time.Minute))
		if err != nil {
			t.Fatalf("AcquirePointer() error: %v", err)
		}
		if result.Status != want || result.Claim != nil {
			t.Fatalf("AcquirePointer() = %+v, want %q without claim", result, want)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	}

	t.Run("missing delivery", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		pointer.DeliveryID = testUUID(t)
		assertStatus(t, d, pointer, PointerInvalid)
	})
	t.Run("wrong organization", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		pointer.OrganizationID = testUUID(t)
		assertStatus(t, d, pointer, PointerInvalid)
	})
	t.Run("missing outbox", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		pointer.OutboxID = testUUID(t)
		assertStatus(t, d, pointer, PointerInvalid)
	})
	t.Run("outbox belongs to another delivery", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		_, other := seedPublishedPointer(t, f, deliverySeed{})
		pointer.OutboxID = other.OutboxID
		assertStatus(t, d, pointer, PointerInvalid)
	})
	t.Run("stale sequence", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		pointer.DispatchSeq++
		assertStatus(t, d, pointer, PointerStale)
	})
	t.Run("older published pointer", func(t *testing.T) {
		d := f.seedDelivery(t, deliverySeed{dispatchSeq: 2})
		oldOutbox := f.insertOutbox(t, d.id, 1, "delivery.dispatch", now.Add(-2*time.Minute))
		f.markOutboxPublished(t, oldOutbox)
		currentOutbox := f.insertOutbox(t, d.id, 2, "delivery.retry", now.Add(-time.Minute))
		f.markOutboxPublished(t, currentOutbox)
		assertStatus(t, d, Pointer{
			OrganizationID: f.orgID,
			DeliveryID:     d.id,
			OutboxID:       oldOutbox,
			DispatchSeq:    1,
		}, PointerStale)
	})
	t.Run("mismatched current outbox", func(t *testing.T) {
		d := f.seedDelivery(t, deliverySeed{dispatchSeq: 2})
		oldOutbox := f.insertOutbox(t, d.id, 1, "delivery.dispatch", now.Add(-2*time.Minute))
		f.markOutboxPublished(t, oldOutbox)
		currentOutbox := f.insertOutbox(t, d.id, 2, "delivery.retry", now.Add(-time.Minute))
		f.markOutboxPublished(t, currentOutbox)
		assertStatus(t, d, Pointer{
			OrganizationID: f.orgID,
			DeliveryID:     d.id,
			OutboxID:       oldOutbox,
			DispatchSeq:    2,
		}, PointerStale)
	})
	t.Run("unpublished outbox", func(t *testing.T) {
		d := f.seedDelivery(t, deliverySeed{})
		outboxID := f.insertOutbox(t, d.id, 1, "delivery.dispatch", now.Add(-time.Minute))
		assertStatus(t, d, Pointer{
			OrganizationID: f.orgID,
			DeliveryID:     d.id,
			OutboxID:       outboxID,
			DispatchSeq:    1,
		}, PointerStale)
	})
	t.Run("terminal", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{state: "succeeded"})
		assertStatus(t, d, pointer, PointerObsolete)
	})
	t.Run("expired event", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{
			eventReceived:  now.Add(-2 * time.Hour),
			eventExpiresAt: now.Add(-time.Hour),
		})
		assertStatus(t, d, pointer, PointerExpired)
	})
	t.Run("attempts exhausted", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{attemptCount: 3})
		assertStatus(t, d, pointer, PointerObsolete)
	})
	t.Run("duplicate live lease", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{
			leaseToken:     testUUID(t),
			leaseExpiresAt: now.Add(time.Hour),
		})
		assertStatus(t, d, pointer, PointerDuplicate)
	})
	t.Run("not due", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{nextAttemptAt: now.Add(time.Hour)})
		assertStatus(t, d, pointer, PointerNotDue)
	})
}

func TestAcquirePointerReplacesExpiredLeaseIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)
	oldToken := testUUID(t)
	d, pointer := seedPublishedPointer(t, f, deliverySeed{
		leaseToken:     oldToken,
		leaseExpiresAt: time.Now().Add(-time.Minute),
	})

	claim := acquireExactPointer(t, store, pointer, time.Minute)
	if claim.LeaseToken == oldToken || !validUUID(claim.LeaseToken) {
		t.Fatalf("expired lease was not replaced: %+v", claim)
	}
	row := f.readDelivery(t, d.id)
	if row.leaseToken != claim.LeaseToken || row.state != "pending" || row.attemptCount != 0 || row.dispatchSeq != 1 {
		t.Fatalf("unexpected replacement state: %+v", row)
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("replacement wrote %d attempts", n)
	}
}

func TestExpirePointerIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	t.Run("expired event", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		beforeOutbox := f.outboxRows(t, d.id)
		beforeUpdatedAt := f.deliveryUpdatedAt(t, d.id)
		f.expireEvent(t, d.eventID)

		changed, err := store.ExpirePointer(context.Background(), pointer)
		if err != nil {
			t.Fatalf("ExpirePointer() error: %v", err)
		}
		if !changed {
			t.Fatal("ExpirePointer() did not expire the exact delivery")
		}
		row := f.readDelivery(t, d.id)
		if row.state != "expired" || !row.completedAt.Valid || row.nextAttemptAt.Valid ||
			row.leaseToken != "" || row.leaseExpiresAt.Valid {
			t.Fatalf("unexpected expired state: %+v", row)
		}
		if row.dispatchSeq != claim.DispatchSeq || row.attemptCount != claim.AttemptCount {
			t.Fatalf("expiry changed accounting: %+v", row)
		}
		requireTimeNear(t, "completed_at", row.completedAt.Time, time.Now(), 5*time.Second)
		afterUpdatedAt := f.deliveryUpdatedAt(t, d.id)
		requireTimeNear(t, "updated_at", afterUpdatedAt, row.completedAt.Time, 2*time.Millisecond)
		if afterUpdatedAt.Before(beforeUpdatedAt) {
			t.Fatalf("updated_at moved backward: before=%s after=%s", beforeUpdatedAt, afterUpdatedAt)
		}
		if afterOutbox := f.outboxRows(t, d.id); len(afterOutbox) != len(beforeOutbox) || afterOutbox[0] != beforeOutbox[0] {
			t.Fatalf("expiry changed outbox rows: before=%+v after=%+v", beforeOutbox, afterOutbox)
		}
		if n := f.attemptCount(t, d.id); n != 0 {
			t.Fatalf("expiry wrote %d attempts", n)
		}
		if again, err := store.ExpirePointer(context.Background(), pointer); err != nil || again {
			t.Fatalf("terminal ExpirePointer() = %t, %v", again, err)
		}
	})

	fencingCases := []struct {
		name   string
		mutate func(*Pointer)
	}{
		{"wrong organization", func(p *Pointer) { p.OrganizationID = testUUID(t) }},
		{"wrong delivery", func(p *Pointer) { p.DeliveryID = testUUID(t) }},
		{"wrong sequence", func(p *Pointer) { p.DispatchSeq++ }},
	}
	for _, tc := range fencingCases {
		t.Run(tc.name, func(t *testing.T) {
			d, pointer := seedPublishedPointer(t, f, deliverySeed{})
			acquireExactPointer(t, store, pointer, time.Minute)
			f.expireEvent(t, d.eventID)
			before := f.readDelivery(t, d.id)
			beforeOutbox := f.outboxRows(t, d.id)
			tc.mutate(&pointer)
			if changed, err := store.ExpirePointer(context.Background(), pointer); err != nil || changed {
				t.Fatalf("fenced ExpirePointer() = %t, %v", changed, err)
			}
			requirePointerUnchanged(t, f, d, before, beforeOutbox)
		})
	}

	t.Run("unexpired event", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		acquireExactPointer(t, store, pointer, time.Minute)
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if changed, err := store.ExpirePointer(context.Background(), pointer); err != nil || changed {
			t.Fatalf("unexpired ExpirePointer() = %t, %v", changed, err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})

	t.Run("already terminal", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{
			state:          "succeeded",
			eventReceived:  time.Now().Add(-2 * time.Hour),
			eventExpiresAt: time.Now().Add(-time.Hour),
		})
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if changed, err := store.ExpirePointer(context.Background(), pointer); err != nil || changed {
			t.Fatalf("terminal ExpirePointer() = %t, %v", changed, err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})
}

func TestReleasePointerIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	t.Run("matching fence", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)

		released, err := store.ReleasePointer(context.Background(), *claim)
		if err != nil {
			t.Fatalf("ReleasePointer() error: %v", err)
		}
		if !released {
			t.Fatal("matching lease was not released")
		}
		after := f.readDelivery(t, d.id)
		if after.leaseToken != "" || after.leaseExpiresAt.Valid {
			t.Fatalf("lease tuple survived release: %+v", after)
		}
		if after.state != before.state || after.dispatchSeq != before.dispatchSeq || after.attemptCount != before.attemptCount ||
			after.nextAttemptAt != before.nextAttemptAt || after.completedAt != before.completedAt || after.lastAttemptAt != before.lastAttemptAt {
			t.Fatalf("release mutated accounting or scheduling: before=%+v after=%+v", before, after)
		}
		if rows := f.outboxRows(t, d.id); len(rows) != len(beforeOutbox) || rows[0] != beforeOutbox[0] {
			t.Fatalf("release changed outbox rows: before=%+v after=%+v", beforeOutbox, rows)
		}
		if n := f.attemptCount(t, d.id); n != 0 {
			t.Fatalf("release wrote %d attempts", n)
		}
		if again, err := store.ReleasePointer(context.Background(), *claim); err != nil || again {
			t.Fatalf("repeated ReleasePointer() = %t, %v", again, err)
		}
	})

	cases := []struct {
		name   string
		mutate func(*Claim)
	}{
		{"wrong organization", func(c *Claim) { c.OrganizationID = testUUID(t) }},
		{"wrong delivery", func(c *Claim) { c.DeliveryID = testUUID(t) }},
		{"wrong sequence", func(c *Claim) { c.DispatchSeq++ }},
		{"wrong token", func(c *Claim) { c.LeaseToken = testUUID(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, pointer := seedPublishedPointer(t, f, deliverySeed{})
			claim := acquireExactPointer(t, store, pointer, time.Minute)
			before := f.readDelivery(t, d.id)
			beforeOutbox := f.outboxRows(t, d.id)
			tampered := *claim
			tc.mutate(&tampered)
			if released, err := store.ReleasePointer(context.Background(), tampered); err != nil || released {
				t.Fatalf("fenced ReleasePointer() = %t, %v", released, err)
			}
			requirePointerUnchanged(t, f, d, before, beforeOutbox)
		})
	}

	t.Run("terminal state", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		ctx := context.Background()
		if _, err := f.pool.Exec(ctx,
			`UPDATE deliveries SET state = 'succeeded', completed_at = clock_timestamp(), next_attempt_at = NULL
			  WHERE id = $1::uuid`, d.id,
		); err != nil {
			t.Fatalf("terminalize delivery: %v", err)
		}
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if released, err := store.ReleasePointer(context.Background(), *claim); err != nil || released {
			t.Fatalf("terminal ReleasePointer() = %t, %v", released, err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})
}

func TestPrepareHTTPIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	t.Run("fresh sufficient budget", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{
			url:       "https://example.test/prepared",
			headers:   `{"content-type":"application/json"}`,
			secret:    "prepared-ciphertext",
			payload:   `{"source":"prepared"}`,
			eventType: "test.prepared",
		})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)

		preparation, err := store.PrepareHTTP(context.Background(), *claim, 5*time.Second)
		if err != nil {
			t.Fatalf("PrepareHTTP() error: %v", err)
		}
		if preparation.OrganizationID != claim.OrganizationID || preparation.DeliveryID != claim.DeliveryID ||
			preparation.EventID != claim.EventID || preparation.DispatchSeq != claim.DispatchSeq {
			t.Fatalf("unexpected preparation identity: %+v", preparation)
		}
		if !preparation.LeaseExpiresAt.Equal(claim.LeaseExpiresAt) || !preparation.EventExpiresAt.Equal(claim.EventExpiresAt) {
			t.Fatalf("unexpected preparation horizons: %+v", preparation)
		}
		if preparation.Remaining != preparation.LeaseExpiresAt.Sub(preparation.DatabaseTime) || preparation.Remaining < 5*time.Second {
			t.Fatalf("unexpected remaining budget: %+v", preparation)
		}
		requireTimeNear(t, "database time", preparation.DatabaseTime, time.Now(), 5*time.Second)
		if preparation.RequestURL != claim.RequestURL || preparation.RequestHeaders != claim.RequestHeaders ||
			preparation.SecretCiphertext != claim.SecretCiphertext || preparation.EventType != claim.EventType ||
			preparation.EventPayload != claim.EventPayload {
			t.Fatalf("unexpected preparation snapshot: %+v", preparation)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})

	t.Run("fresh database lease overrides claim snapshot", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE deliveries SET dispatch_lease_expires_at = clock_timestamp() + interval '2 seconds'
			  WHERE id = $1::uuid`, d.id,
		); err != nil {
			t.Fatalf("shorten database lease: %v", err)
		}
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if _, err := store.PrepareHTTP(context.Background(), *claim, 3*time.Second); !errors.Is(err, ErrInsufficientLease) {
			t.Fatalf("PrepareHTTP() error = %v, want ErrInsufficientLease", err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})

	t.Run("expired lease", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE deliveries SET dispatch_lease_expires_at = clock_timestamp() - interval '1 second'
			  WHERE id = $1::uuid`, d.id,
		); err != nil {
			t.Fatalf("expire database lease: %v", err)
		}
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if _, err := store.PrepareHTTP(context.Background(), *claim, time.Nanosecond); !errors.Is(err, ErrInsufficientLease) {
			t.Fatalf("PrepareHTTP() error = %v, want ErrInsufficientLease", err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})

	t.Run("expired event", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		f.expireEvent(t, d.eventID)
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if _, err := store.PrepareHTTP(context.Background(), *claim, time.Second); !errors.Is(err, ErrEventExpired) {
			t.Fatalf("PrepareHTTP() error = %v, want ErrEventExpired", err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})

	cases := []struct {
		name   string
		mutate func(*Claim)
	}{
		{"wrong organization", func(c *Claim) { c.OrganizationID = testUUID(t) }},
		{"wrong delivery", func(c *Claim) { c.DeliveryID = testUUID(t) }},
		{"wrong event", func(c *Claim) { c.EventID = testUUID(t) }},
		{"wrong sequence", func(c *Claim) { c.DispatchSeq++ }},
		{"wrong token", func(c *Claim) { c.LeaseToken = testUUID(t) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d, pointer := seedPublishedPointer(t, f, deliverySeed{})
			claim := acquireExactPointer(t, store, pointer, time.Minute)
			before := f.readDelivery(t, d.id)
			beforeOutbox := f.outboxRows(t, d.id)
			tampered := *claim
			tc.mutate(&tampered)
			if _, err := store.PrepareHTTP(context.Background(), tampered, time.Second); !errors.Is(err, ErrPointerStale) {
				t.Fatalf("PrepareHTTP() error = %v, want ErrPointerStale", err)
			}
			requirePointerUnchanged(t, f, d, before, beforeOutbox)
		})
	}

	t.Run("terminal state", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE deliveries SET state = 'succeeded', completed_at = clock_timestamp(), next_attempt_at = NULL
			  WHERE id = $1::uuid`, d.id,
		); err != nil {
			t.Fatalf("terminalize delivery: %v", err)
		}
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		if _, err := store.PrepareHTTP(context.Background(), *claim, time.Second); !errors.Is(err, ErrPointerStale) {
			t.Fatalf("PrepareHTTP() error = %v, want ErrPointerStale", err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})
}

func TestAttemptRequestHeadersSanitizationIntegration(t *testing.T) {
	f := newIntegrationFixture(t)
	store := newTestStore(t, f, nil)

	t.Run("credentials redacted and ordinary headers preserved", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{
			headers: `{
				"Authorization":"Bearer authorization-secret",
				"X-API-Key":"api-key-secret",
				"Cookie":"session=cookie-secret",
				"Content-Type":"application/json",
				"X-Trace-ID":"trace-123"
			}`,
		})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		if _, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{}); err != nil {
			t.Fatalf("Complete() error: %v", err)
		}
		attempts := f.attempts(t, d.id)
		if len(attempts) != 1 {
			t.Fatalf("attempt rows = %d, want 1", len(attempts))
		}
		persisted := attempts[0].requestHeaders
		for _, secret := range []string{"authorization-secret", "api-key-secret", "cookie-secret"} {
			if strings.Contains(persisted, secret) {
				t.Fatalf("credential %q was persisted: %s", secret, persisted)
			}
		}
		var headers map[string]string
		if err := json.Unmarshal([]byte(persisted), &headers); err != nil {
			t.Fatalf("persisted headers are not JSON: %v", err)
		}
		for _, name := range []string{"Authorization", "X-API-Key", "Cookie"} {
			if headers[name] != "[REDACTED]" {
				t.Errorf("%s = %q, want redacted", name, headers[name])
			}
		}
		if headers["Content-Type"] != "application/json" || headers["X-Trace-ID"] != "trace-123" {
			t.Fatalf("ordinary headers changed: %v", headers)
		}
	})

	t.Run("non-object headers fail closed", func(t *testing.T) {
		d, pointer := seedPublishedPointer(t, f, deliverySeed{headers: `"authorization=raw-secret"`})
		claim := acquireExactPointer(t, store, pointer, time.Minute)
		before := f.readDelivery(t, d.id)
		beforeOutbox := f.outboxRows(t, d.id)
		_, err := store.Complete(context.Background(), *claim, successOutcome(), RetryDecision{})
		if err == nil {
			t.Fatal("Complete() persisted non-object request headers")
		}
		if strings.Contains(err.Error(), "raw-secret") {
			t.Fatalf("sanitization error leaked raw headers: %v", err)
		}
		requirePointerUnchanged(t, f, d, before, beforeOutbox)
	})
}
