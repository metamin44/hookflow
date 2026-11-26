//go:build integration

package delivery

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// TestPrepareHTTPCarriesExactEndpointIdentity locks fresh state, returns the
// stored organization/endpoint identity unchanged, and leaves no row lock after
// the preparation transaction commits.
func TestPrepareHTTPCarriesExactEndpointIdentity(t *testing.T) {
	f := newIntegrationFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	store := newTestStore(t, f, nil)
	claim := acquire(t, store, 2*time.Minute, 5)
	if claim.EndpointID != f.endpointID || claim.OrganizationID != f.orgID || claim.DeliveryID != d.id {
		t.Fatalf("claim identity = org %q endpoint %q delivery %q", claim.OrganizationID, claim.EndpointID, claim.DeliveryID)
	}

	preparation, err := store.PrepareHTTP(context.Background(), *claim, time.Second)
	if err != nil {
		t.Fatalf("PrepareHTTP() error: %v", err)
	}
	if preparation.OrganizationID != claim.OrganizationID ||
		preparation.EndpointID != f.endpointID ||
		preparation.DeliveryID != d.id ||
		preparation.EventID != d.eventID {
		t.Fatalf("preparation identity = %+v, want the exact stored tenant and endpoint", preparation)
	}
	if preparation.Remaining <= 0 {
		t.Fatalf("preparation remaining lease = %s", preparation.Remaining)
	}
	assertDeliveryRowUnlocked(t, f, d.id)
	if count := len(f.attempts(t, d.id)); count != 0 {
		t.Fatalf("preparation wrote %d attempt rows", count)
	}
}

// TestPrepareHTTPRejectsStaleEndpointIdentity changes the stored endpoint after
// acquisition and requires a stale refusal before any HTTP could start.
func TestPrepareHTTPRejectsStaleEndpointIdentity(t *testing.T) {
	f := newIntegrationFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	store := newTestStore(t, f, nil)
	claim := acquire(t, store, 2*time.Minute, 5)
	secondEndpoint := insertIdentityEndpoint(t, f)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`UPDATE deliveries SET endpoint_id = $2::uuid
		  WHERE organization_id = $1::uuid AND id = $3::uuid`,
		f.orgID, secondEndpoint, d.id); err != nil {
		t.Fatalf("move delivery endpoint: %v", err)
	}
	before := f.readDelivery(t, d.id)

	if _, err := store.PrepareHTTP(context.Background(), *claim, time.Second); !errors.Is(err, ErrPointerStale) {
		t.Fatalf("PrepareHTTP() error = %v, want ErrPointerStale", err)
	}
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("stale endpoint preparation mutated the delivery: before=%+v after=%+v", before, after)
	}
	if count := len(f.attempts(t, d.id)); count != 0 {
		t.Fatalf("stale endpoint preparation wrote %d attempt rows", count)
	}
	assertDeliveryRowUnlocked(t, f, d.id)
}

// TestPrepareHTTPRejectsClaimWithoutEndpointIdentity proves there is no default
// endpoint identity: an empty or malformed endpoint fails validation before the
// store is touched.
func TestPrepareHTTPRejectsClaimWithoutEndpointIdentity(t *testing.T) {
	f := newIntegrationFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	store := newTestStore(t, f, nil)
	claim := acquire(t, store, 2*time.Minute, 5)
	before := f.readDelivery(t, d.id)

	for _, endpoint := range []string{"", "not-a-uuid", "77777777-7777-4777-8777-77777777777z"} {
		broken := *claim
		broken.EndpointID = endpoint
		_, err := store.PrepareHTTP(context.Background(), broken, time.Second)
		if err == nil || !strings.Contains(err.Error(), "endpoint id") {
			t.Fatalf("PrepareHTTP(endpoint=%q) error = %v, want endpoint validation", endpoint, err)
		}
	}
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("invalid endpoint claim mutated the delivery: before=%+v after=%+v", before, after)
	}
	if count := len(f.attempts(t, d.id)); count != 0 {
		t.Fatalf("invalid endpoint claim wrote %d attempt rows", count)
	}
}

// TestPrepareHTTPInsufficientBudgetLeavesNoMutation proves the pre-HTTP budget
// refusal is read-only: no attempt, no attempt_count, no lease, no generation.
func TestPrepareHTTPInsufficientBudgetLeavesNoMutation(t *testing.T) {
	f := newIntegrationFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	store := newTestStore(t, f, nil)
	claim := acquire(t, store, 500*time.Millisecond, 5)
	before := f.readDelivery(t, d.id)

	if _, err := store.PrepareHTTP(context.Background(), *claim, time.Second); !errors.Is(err, ErrInsufficientLease) {
		t.Fatalf("PrepareHTTP() error = %v, want ErrInsufficientLease", err)
	}
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("budget refusal mutated the delivery: before=%+v after=%+v", before, after)
	}
	if count := len(f.attempts(t, d.id)); count != 0 {
		t.Fatalf("budget refusal wrote %d attempt rows", count)
	}
	assertDeliveryRowUnlocked(t, f, d.id)
}

func insertIdentityEndpoint(t *testing.T, f *integrationFixture) string {
	t.Helper()
	endpointID := testUUID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO endpoints (id, organization_id, name, url, secret_encrypted, status)
		 VALUES ($1::uuid, $2::uuid, $3, 'https://example.test/second', 'test-ciphertext', 'active')`,
		endpointID, f.orgID, "endpoint-"+endpointID,
	); err != nil {
		t.Fatalf("insert identity endpoint: %v", err)
	}
	return endpointID
}

func assertDeliveryRowUnlocked(t *testing.T, f *integrationFixture, deliveryID string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin lock probe: %v", err)
	}
	var one int
	probeErr := tx.QueryRow(ctx,
		`SELECT 1 FROM deliveries WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE NOWAIT`,
		f.orgID, deliveryID).Scan(&one)
	_ = tx.Rollback(ctx)
	if probeErr != nil {
		t.Fatalf("delivery row remained locked after preparation: %v", probeErr)
	}
}
