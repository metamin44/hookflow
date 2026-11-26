//go:build integration

package integration_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/modules/organization"
	"github.com/metamin44/hookflow/internal/modules/subscription"
)

type lockedRequest struct {
	reply mgmtReply
	err   error
}

func (f *mgmtFixture) asyncRequest(method, path, token, body string, headers ...string) <-chan lockedRequest {
	done := make(chan lockedRequest, 1)
	go func() {
		reply, err := f.roundTrip(method, path, token, body, headers...)
		done <- lockedRequest{reply: reply, err: err}
	}()
	return done
}

func (f *mgmtFixture) awaitRequest(t *testing.T, done <-chan lockedRequest, description string) mgmtReply {
	t.Helper()
	select {
	case finished := <-done:
		if finished.err != nil {
			t.Fatalf("%s: %v", description, finished.err)
		}
		return finished.reply
	case <-time.After(10 * time.Second):
		t.Fatalf("%s did not finish within 10s", description)
		return mgmtReply{}
	}
}

func (f *mgmtFixture) assertRequestPending(t *testing.T, done <-chan lockedRequest, description string) {
	t.Helper()
	select {
	case finished := <-done:
		if finished.err != nil {
			t.Fatalf("%s finished early with error: %v", description, finished.err)
		}
		t.Fatalf("%s finished while the lock holder was still active: status=%d", description, finished.reply.status)
	default:
	}
}

// waitForUngrantedLock observes PostgreSQL's own lock wait state, so tests can
// prove a request is waiting without sleeping for a fixed delay. Row-level
// lock waiters wait on the holder's transaction ID, which pg_locks reports as
// a transactionid waiter rather than a relation lock; the blocked statement in
// pg_stat_activity identifies which relation the request is locking.
func (f *mgmtFixture) waitForUngrantedLock(t *testing.T, relation string, minimum int) {
	t.Helper()
	eventually(t, 8*time.Second, "management requests to queue on "+relation, func() error {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		var count int
		if err := f.pool.QueryRow(ctx, `
			SELECT count(*)
			  FROM pg_stat_activity a
			 WHERE a.datname = current_database()
			   AND a.pid <> pg_backend_pid()
			   AND a.wait_event_type = 'Lock'
			   AND a.query LIKE '%' || $1 || '%'`, relation).Scan(&count); err != nil {
			return err
		}
		if count < minimum {
			return fmt.Errorf("backends waiting on %s = %d, want at least %d", relation, count, minimum)
		}
		return nil
	})
}

func (f *mgmtFixture) beginLockTx(t *testing.T) (context.Context, context.CancelFunc, pgx.Tx) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		cancel()
		t.Fatalf("begin lock transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return ctx, cancel, tx
}

func lockOrganizationAndEndpoint(t *testing.T, ctx context.Context, tx pgx.Tx, organizationID, endpointID string) {
	t.Helper()
	if _, err := organization.Lock(ctx, tx, organizationID); err != nil {
		t.Fatalf("lock organization: %v", err)
	}
	if _, err := endpoint.Lock(ctx, tx, organizationID, endpointID); err != nil {
		t.Fatalf("lock endpoint: %v", err)
	}
}

func seedManagementSubscription(t *testing.T, f *mgmtFixture, endpointID, eventType string) string {
	t.Helper()
	token, _ := f.createKey("seed", "endpoints:write")
	reply := f.do(http.MethodPost, f.endpoint(endpointID)+"/subscriptions", token,
		fmt.Sprintf(`{"event_type":%q}`, eventType))
	wantStatus(t, reply, http.StatusCreated, "seed subscription")
	return subscriptionIDFromReply(t, reply)
}

func deactivateManagementSubscription(t *testing.T, f *mgmtFixture, endpointID, subscriptionID string) {
	t.Helper()
	token, _ := f.createKey("deactivate", "endpoints:write")
	wantStatus(t, f.do(http.MethodDelete,
		f.endpoint(endpointID)+"/subscriptions/"+subscriptionID, token, ""),
		http.StatusOK, "deactivate seed subscription")
}

// TestManagementSubscriptionVersusSoftDelete covers both transaction orders
// for create, reactivate and deactivate against endpoint soft-delete. The
// holder transaction follows organizations -> endpoints -> subscriptions.
func TestManagementSubscriptionVersusSoftDelete(t *testing.T) {
	t.Run("create delete-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
		path := f.endpoint(endpointID) + "/subscriptions"

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		lockOrganizationAndEndpoint(t, ctx, tx, f.orgID, endpointID)

		pending := f.asyncRequest(http.MethodPost, path, token, `{"event_type":"order.created"}`)
		f.waitForUngrantedLock(t, "organizations", 1)
		f.assertRequestPending(t, pending, "subscription create")

		if _, err := endpoint.SoftDelete(ctx, tx, f.orgID, endpointID); err != nil {
			t.Fatalf("soft delete under held locks: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit delete-first transaction: %v", err)
		}
		reply := f.awaitRequest(t, pending, "subscription create")
		wantProblem(t, reply, http.StatusNotFound, "not_found", "")
		if got := countMgmtSubscriptions(t, f, endpointID, false); got != 0 {
			t.Fatalf("delete-first subscription create left %d rows", got)
		}
		if got := f.auditCount("subscription.created"); got != 0 {
			t.Fatalf("delete-first subscription create wrote %d audit rows", got)
		}
	})

	t.Run("create subscription-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
		path := f.endpoint(endpointID) + "/subscriptions"

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		lockOrganizationAndEndpoint(t, ctx, tx, f.orgID, endpointID)
		if _, _, err := subscription.CreateOrReactivate(ctx, tx, subscription.Record{
			ID: mustID(t), OrganizationID: f.orgID, EndpointID: endpointID, EventType: "order.created",
		}); err != nil {
			t.Fatalf("create subscription under held locks: %v", err)
		}

		pending := f.asyncRequest(http.MethodDelete, f.endpoint(endpointID), token, "")
		f.waitForUngrantedLock(t, "organizations", 1)
		f.assertRequestPending(t, pending, "endpoint soft delete")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit subscription-first transaction: %v", err)
		}
		wantStatus(t, f.awaitRequest(t, pending, "endpoint soft delete"), http.StatusNoContent, "soft delete")

		if got := countMgmtSubscriptions(t, f, endpointID, true); got != 1 {
			t.Fatalf("soft delete changed subscription rows: active=%d, want 1", got)
		}
		wantProblem(t, f.do(http.MethodPost, path, token, `{"event_type":"order.created"}`),
			http.StatusNotFound, "not_found", "")
		if got := countMgmtSubscriptions(t, f, endpointID, false); got != 1 {
			t.Fatalf("post-delete create changed row count: %d, want 1", got)
		}
		if got := f.auditCount("subscription.created"); got != 0 {
			t.Fatalf("post-delete create wrote %d audit rows", got)
		}
	})

	t.Run("reactivate delete-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
		path := f.endpoint(endpointID) + "/subscriptions"
		subscriptionID := seedManagementSubscription(t, f, endpointID, "order.created")
		deactivateManagementSubscription(t, f, endpointID, subscriptionID)
		audits := f.auditCount("subscription.reactivated")

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		lockOrganizationAndEndpoint(t, ctx, tx, f.orgID, endpointID)

		pending := f.asyncRequest(http.MethodPost, path, token, `{"event_type":"order.created"}`)
		f.waitForUngrantedLock(t, "organizations", 1)
		f.assertRequestPending(t, pending, "subscription reactivation")
		if _, err := endpoint.SoftDelete(ctx, tx, f.orgID, endpointID); err != nil {
			t.Fatalf("soft delete under held locks: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit reactivate delete-first transaction: %v", err)
		}
		reply := f.awaitRequest(t, pending, "subscription reactivation")
		wantProblem(t, reply, http.StatusNotFound, "not_found", "")
		if subscriptionActive(t, f, subscriptionID) {
			t.Fatal("delete-first reactivation mutated the subscription")
		}
		if got := f.auditCount("subscription.reactivated"); got != audits {
			t.Fatalf("delete-first reactivation wrote an audit row (%d -> %d)", audits, got)
		}
	})

	t.Run("reactivate subscription-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
		subscriptionID := seedManagementSubscription(t, f, endpointID, "order.created")
		deactivateManagementSubscription(t, f, endpointID, subscriptionID)
		audits := f.auditCount("subscription.reactivated")

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		lockOrganizationAndEndpoint(t, ctx, tx, f.orgID, endpointID)
		if _, _, err := subscription.CreateOrReactivate(ctx, tx, subscription.Record{
			ID: mustID(t), OrganizationID: f.orgID, EndpointID: endpointID, EventType: "order.created",
		}); err != nil {
			t.Fatalf("reactivate subscription under held locks: %v", err)
		}

		pending := f.asyncRequest(http.MethodDelete, f.endpoint(endpointID), token, "")
		f.waitForUngrantedLock(t, "organizations", 1)
		f.assertRequestPending(t, pending, "endpoint soft delete")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit reactivate subscription-first transaction: %v", err)
		}
		wantStatus(t, f.awaitRequest(t, pending, "endpoint soft delete"), http.StatusNoContent, "soft delete")

		if !subscriptionActive(t, f, subscriptionID) {
			t.Fatal("soft delete mutated the reactivated subscription")
		}
		wantProblem(t, f.do(http.MethodPost, f.endpoint(endpointID)+"/subscriptions", token,
			`{"event_type":"order.created"}`), http.StatusNotFound, "not_found", "")
		if got := f.auditCount("subscription.reactivated"); got != audits {
			t.Fatalf("post-delete reactivation wrote an audit row (%d -> %d)", audits, got)
		}
	})

	t.Run("deactivate delete-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
		subscriptionID := seedManagementSubscription(t, f, endpointID, "order.created")
		audits := f.auditCount("subscription.deactivated")

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		lockOrganizationAndEndpoint(t, ctx, tx, f.orgID, endpointID)

		pending := f.asyncRequest(http.MethodDelete,
			f.endpoint(endpointID)+"/subscriptions/"+subscriptionID, token, "")
		f.waitForUngrantedLock(t, "organizations", 1)
		f.assertRequestPending(t, pending, "subscription deactivation")
		if _, err := endpoint.SoftDelete(ctx, tx, f.orgID, endpointID); err != nil {
			t.Fatalf("soft delete under held locks: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit deactivate delete-first transaction: %v", err)
		}
		reply := f.awaitRequest(t, pending, "subscription deactivation")
		wantProblem(t, reply, http.StatusNotFound, "not_found", "")
		if !subscriptionActive(t, f, subscriptionID) {
			t.Fatal("delete-first deactivation mutated the subscription")
		}
		if got := f.auditCount("subscription.deactivated"); got != audits {
			t.Fatalf("delete-first deactivation wrote an audit row (%d -> %d)", audits, got)
		}
	})

	t.Run("deactivate subscription-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
		subscriptionID := seedManagementSubscription(t, f, endpointID, "order.created")

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		lockOrganizationAndEndpoint(t, ctx, tx, f.orgID, endpointID)
		if _, err := subscription.Lock(ctx, tx, f.orgID, endpointID, subscriptionID); err != nil {
			t.Fatalf("lock subscription: %v", err)
		}
		if _, changed, err := subscription.Deactivate(ctx, tx, f.orgID, endpointID, subscriptionID); err != nil || !changed {
			t.Fatalf("deactivate subscription under held locks: changed=%v error=%v", changed, err)
		}

		pending := f.asyncRequest(http.MethodDelete, f.endpoint(endpointID), token, "")
		f.waitForUngrantedLock(t, "organizations", 1)
		f.assertRequestPending(t, pending, "endpoint soft delete")
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit deactivate subscription-first transaction: %v", err)
		}
		wantStatus(t, f.awaitRequest(t, pending, "endpoint soft delete"), http.StatusNoContent, "soft delete")
		if subscriptionActive(t, f, subscriptionID) {
			t.Fatal("soft delete mutated the deactivated subscription")
		}
		if got := f.auditCount("endpoint.deleted"); got != 1 {
			t.Fatalf("endpoint.deleted audit rows = %d, want 1", got)
		}
	})
}

func TestManagementSubscriptionLockSerialization(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	path := f.endpoint(endpointID) + "/subscriptions"

	ctx, cancel, tx := f.beginLockTx(t)
	defer cancel()
	if _, err := endpoint.Lock(ctx, tx, f.orgID, endpointID); err != nil {
		t.Fatalf("lock endpoint only: %v", err)
	}

	first := f.asyncRequest(http.MethodPost, path, token, `{"event_type":"order.created"}`)
	second := f.asyncRequest(http.MethodPost, path, token, `{"event_type":"order.updated"}`)
	// The holder owns only the endpoint row. Whichever request reaches the
	// endpoint lock first waits there while holding the organization row; the
	// other then waits on the organization lock, so both lock-order levels show
	// a waiter and neither request can have finished.
	f.waitForUngrantedLock(t, "endpoints", 1)
	f.waitForUngrantedLock(t, "organizations", 1)
	f.assertRequestPending(t, first, "first subscription write")
	f.assertRequestPending(t, second, "second subscription write")
	if err := tx.Commit(ctx); err != nil {
		t.Fatalf("release endpoint lock: %v", err)
	}

	replies := []mgmtReply{
		f.awaitRequest(t, first, "first subscription write"),
		f.awaitRequest(t, second, "second subscription write"),
	}
	for _, reply := range replies {
		wantStatus(t, reply, http.StatusCreated, "serialized subscription write")
	}
	if got := countMgmtSubscriptions(t, f, endpointID, true); got != 2 {
		t.Fatalf("active subscriptions = %d, want 2", got)
	}
	if got := f.auditCount("subscription.created"); got != 2 {
		t.Fatalf("subscription.created audit rows = %d, want 2", got)
	}
}

func TestManagementPausedAndDeletedSubscriptionPolicy(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	wantStatus(t, f.do(http.MethodPatch, f.endpoint(endpointID), token, `{"status":"paused"}`, "If-Match", "*"),
		http.StatusOK, "pause endpoint")

	path := f.endpoint(endpointID) + "/subscriptions"
	created := f.do(http.MethodPost, path, token, `{"event_type":"order.created"}`)
	wantStatus(t, created, http.StatusCreated, "paused subscription create")
	subscriptionID := subscriptionIDFromReply(t, created)
	wantStatus(t, f.do(http.MethodDelete, path+"/"+subscriptionID, token, ""),
		http.StatusOK, "paused subscription deactivate")

	if got := f.auditCount("subscription.created"); got != 1 {
		t.Fatalf("subscription.created audit rows = %d, want 1", got)
	}
	if got := f.auditCount("subscription.deactivated"); got != 1 {
		t.Fatalf("subscription.deactivated audit rows = %d, want 1", got)
	}

	wantStatus(t, f.do(http.MethodDelete, f.endpoint(endpointID), token, ""), http.StatusNoContent, "soft delete")
	rows := countMgmtSubscriptions(t, f, endpointID, false)
	createdAudits := f.auditCount("subscription.created")
	deactivatedAudits := f.auditCount("subscription.deactivated")
	wantProblem(t, f.do(http.MethodPost, path, token, `{"event_type":"order.updated"}`),
		http.StatusNotFound, "not_found", "")
	wantProblem(t, f.do(http.MethodDelete, path+"/"+subscriptionID, token, ""),
		http.StatusNotFound, "not_found", "")
	if got := countMgmtSubscriptions(t, f, endpointID, false); got != rows {
		t.Fatalf("post-delete subscription writes changed row count (%d -> %d)", rows, got)
	}
	if got := f.auditCount("subscription.created"); got != createdAudits {
		t.Fatalf("post-delete create wrote an audit row (%d -> %d)", createdAudits, got)
	}
	if got := f.auditCount("subscription.deactivated"); got != deactivatedAudits {
		t.Fatalf("post-delete deactivate wrote an audit row (%d -> %d)", deactivatedAudits, got)
	}
}

func TestManagementDeliveryKeyShareCompatibility(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)

	ctx, cancel, tx := f.beginLockTx(t)
	defer cancel()
	var id string
	if err := tx.QueryRow(ctx, `SELECT id::text FROM organizations WHERE id = $1::uuid FOR KEY SHARE`, f.orgID).Scan(&id); err != nil {
		t.Fatalf("take organization key share: %v", err)
	}
	if err := tx.QueryRow(ctx, `SELECT id::text FROM endpoints WHERE id = $1::uuid FOR KEY SHARE`, endpointID).Scan(&id); err != nil {
		t.Fatalf("take endpoint key share: %v", err)
	}

	path := f.endpoint(endpointID) + "/subscriptions"
	created := f.asyncRequest(http.MethodPost, path, token, `{"event_type":"order.created"}`)
	wantStatus(t, f.awaitRequest(t, created, "subscription write under key share"),
		http.StatusCreated, "subscription write under key share")

	deleted := f.asyncRequest(http.MethodDelete, f.endpoint(endpointID), token, "")
	wantStatus(t, f.awaitRequest(t, deleted, "soft delete under key share"),
		http.StatusNoContent, "soft delete under key share")
	if _, err := tx.Exec(ctx, `SELECT 1`); err != nil {
		t.Fatalf("key-share transaction interrupted: %v", err)
	}
}

func TestManagementSoftDeletePreservesDeliverySnapshot(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{})
	token, _ := f.createKey("full", "endpoints:write")
	endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)

	eventID, deliveryID := mustID(t), mustID(t)
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO events (id, organization_id, type, payload, destination_count, received_at, expires_at)
		 VALUES ($1::uuid, $2::uuid, 'order.created', '{"source":"management"}'::jsonb, 1, now(), now() + interval '1 hour')`,
		eventID, f.orgID); err != nil {
		t.Fatalf("seed delivery event: %v", err)
	}
	if _, err := f.pool.Exec(ctx,
		`INSERT INTO deliveries
		     (id, organization_id, event_id, endpoint_id, url, http_headers, secret_encrypted,
		      state, dispatch_seq, attempt_count, next_attempt_at, created_at, updated_at)
		 VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, 'https://example.test/hooks',
		         '{"x-snapshot":"accepted"}'::jsonb, $5, 'pending', 1, 0, now() - interval '1 minute', now(), now())`,
		deliveryID, f.orgID, eventID, endpointID, f.storedSecret(endpointID)); err != nil {
		t.Fatalf("seed delivery snapshot: %v", err)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cleanupCancel()
		if _, err := f.pool.Exec(cleanupCtx, `DELETE FROM deliveries WHERE id = $1::uuid`, deliveryID); err != nil {
			t.Errorf("cleanup delivery snapshot: %v", err)
		}
		if _, err := f.pool.Exec(cleanupCtx, `DELETE FROM events WHERE id = $1::uuid`, eventID); err != nil {
			t.Errorf("cleanup delivery event: %v", err)
		}
	})

	readSnapshot := func() string {
		var snapshot string
		if err := f.pool.QueryRow(context.Background(),
			`SELECT to_jsonb(d)::text FROM deliveries d WHERE d.id = $1::uuid`, deliveryID).Scan(&snapshot); err != nil {
			t.Fatalf("read delivery snapshot: %v", err)
		}
		return snapshot
	}
	before := readSnapshot()
	wantStatus(t, f.do(http.MethodDelete, f.endpoint(endpointID), token, ""), http.StatusNoContent, "soft delete")
	if after := readSnapshot(); after != before {
		t.Fatal("endpoint soft delete changed the accepted delivery snapshot")
	}
}

func TestManagementEndpointCreationVersusOrganizationTransition(t *testing.T) {
	for _, status := range []string{"suspended", "archived"} {
		t.Run(status+" status-first", func(t *testing.T) {
			f := newMgmtFixture(t, mgmtOptions{})
			token, _ := f.createKey("full", "endpoints:write")

			ctx, cancel, tx := f.beginLockTx(t)
			defer cancel()
			if _, err := organization.Lock(ctx, tx, f.orgID); err != nil {
				t.Fatalf("lock organization: %v", err)
			}
			if _, err := tx.Exec(ctx, `UPDATE organizations SET status = $2 WHERE id = $1::uuid`, f.orgID, status); err != nil {
				t.Fatalf("transition organization: %v", err)
			}
			pending := f.asyncRequest(http.MethodPost, f.endpoints(), token,
				`{"name":"late-endpoint","url":"https://example.test/late"}`)
			f.waitForUngrantedLock(t, "organizations", 1)
			f.assertRequestPending(t, pending, "endpoint creation")

			if err := tx.Commit(ctx); err != nil {
				t.Fatalf("commit status transition: %v", err)
			}
			reply := f.awaitRequest(t, pending, "endpoint creation")
			wantProblem(t, reply, http.StatusForbidden, "organization_"+status, "")
			var count int
			if err := f.pool.QueryRow(context.Background(),
				`SELECT count(*) FROM endpoints WHERE organization_id = $1::uuid`, f.orgID).Scan(&count); err != nil {
				t.Fatalf("count endpoints after rejected creation: %v", err)
			}
			if count != 0 {
				t.Fatalf("status-first creation left %d endpoints", count)
			}
			if got := f.auditCount("endpoint.created"); got != 0 {
				t.Fatalf("status-first creation wrote %d audit rows", got)
			}
		})
	}

	t.Run("creation-first", func(t *testing.T) {
		f := newMgmtFixture(t, mgmtOptions{})
		token, _ := f.createKey("full", "endpoints:write")
		endpointID := mustID(t)

		ctx, cancel, tx := f.beginLockTx(t)
		defer cancel()
		if _, err := organization.Lock(ctx, tx, f.orgID); err != nil {
			t.Fatalf("lock organization for creation: %v", err)
		}
		if _, err := endpoint.Insert(ctx, tx, endpoint.Record{
			ID: endpointID, OrganizationID: f.orgID, Name: "created-first",
			URL: "https://example.test/first", HTTPHeaders: map[string]string{},
		}); err != nil {
			t.Fatalf("insert endpoint under organization lock: %v", err)
		}

		probeCtx, probeCancel := context.WithTimeout(context.Background(), queryTimeout)
		defer probeCancel()
		probeTx, err := f.pool.Begin(probeCtx)
		if err != nil {
			t.Fatalf("begin transition probe: %v", err)
		}
		defer func() { _ = probeTx.Rollback(probeCtx) }()
		var ignored string
		err = probeTx.QueryRow(probeCtx,
			`SELECT status FROM organizations WHERE id = $1::uuid FOR NO KEY UPDATE NOWAIT`, f.orgID).Scan(&ignored)
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "55P03" {
			t.Fatalf("status transition did not wait behind endpoint creation: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatalf("commit endpoint creation: %v", err)
		}
		_ = probeTx.Rollback(probeCtx)
		if _, err := f.pool.Exec(context.Background(),
			`UPDATE organizations SET status = 'archived' WHERE id = $1::uuid`, f.orgID); err != nil {
			t.Fatalf("transition after committed creation: %v", err)
		}

		var live int
		if err := f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM endpoints WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL`,
			f.orgID, endpointID).Scan(&live); err != nil {
			t.Fatalf("read endpoint created first: %v", err)
		}
		if live != 1 {
			t.Fatal("creation-first endpoint is missing after the status transition")
		}

		wantProblem(t, f.do(http.MethodPost, f.endpoints(), token,
			`{"name":"after-archive","url":"https://example.test/after"}`),
			http.StatusForbidden, "organization_archived", "")
		var total int
		if err := f.pool.QueryRow(context.Background(),
			`SELECT count(*) FROM endpoints WHERE organization_id = $1::uuid`, f.orgID).Scan(&total); err != nil {
			t.Fatalf("count endpoints after archived rejection: %v", err)
		}
		if total != 1 {
			t.Fatalf("archived creation inserted a second endpoint (count=%d)", total)
		}
	})
}

func TestManagementConcurrentCreationAtCapMinusOne(t *testing.T) {
	f := newMgmtFixture(t, mgmtOptions{maxActive: 1})
	token, _ := f.createKey("full", "endpoints:write")

	const attempts = 4
	start := make(chan struct{})
	results := make(chan lockedRequest, attempts)
	var group sync.WaitGroup
	for i := 0; i < attempts; i++ {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			<-start
			reply, err := f.roundTrip(http.MethodPost, f.endpoints(), token,
				fmt.Sprintf(`{"name":"cap-%d","url":"https://example.test/cap-%d"}`, index, index))
			results <- lockedRequest{reply: reply, err: err}
		}(i)
	}
	close(start)
	group.Wait()
	close(results)

	statuses := make([]int, 0, attempts)
	for result := range results {
		if result.err != nil {
			t.Fatalf("concurrent creation request: %v", result.err)
		}
		statuses = append(statuses, result.reply.status)
	}
	sort.Ints(statuses)
	if len(statuses) != attempts || statuses[0] != http.StatusCreated ||
		statuses[len(statuses)-1] != http.StatusConflict {
		t.Fatalf("concurrent creation statuses = %v, want one 201 and the rest 409", statuses)
	}
	for _, status := range statuses[1:] {
		if status != http.StatusConflict {
			t.Fatalf("concurrent creation statuses = %v, want one 201 and the rest 409", statuses)
		}
	}

	var total, active int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*), count(*) FILTER (WHERE status = 'active' AND deleted_at IS NULL)
		   FROM endpoints WHERE organization_id = $1::uuid`, f.orgID).Scan(&total, &active); err != nil {
		t.Fatalf("count capped endpoints: %v", err)
	}
	if total != 1 || active != 1 {
		t.Fatalf("capped endpoints total=%d active=%d, want 1/1", total, active)
	}
	if got := f.auditCount("endpoint.created"); got != 1 {
		t.Fatalf("endpoint.created audit rows = %d, want 1", got)
	}
}

func TestManagementLockOrderStress(t *testing.T) {
	const (
		workers    = 4
		iterations = 4
	)
	f := newMgmtFixture(t, mgmtOptions{maxActive: 2})
	token, _ := f.createKey("full", "endpoints:write")
	endpointID := f.createEndpointQuiet(token, `{"name":"orders","url":"https://example.test/hooks"}`)
	path := f.endpoint(endpointID) + "/subscriptions"

	failures := make(chan string, workers*iterations*3)
	start := make(chan struct{})
	var group sync.WaitGroup
	for worker := 0; worker < workers; worker++ {
		group.Add(1)
		go func(worker int) {
			defer group.Done()
			<-start
			for iteration := 0; iteration < iterations; iteration++ {
				eventType := fmt.Sprintf("stress.%d.%d", worker, iteration)
				reply, err := f.roundTrip(http.MethodPost, path, token,
					fmt.Sprintf(`{"event_type":%q}`, eventType))
				if err != nil || reply.status != http.StatusCreated {
					failures <- fmt.Sprintf("create %s status=%d error=%v", eventType, reply.status, err)
					continue
				}
				subscriptionID := subscriptionIDFromReply(t, reply)
				deactivated, err := f.roundTrip(http.MethodDelete, path+"/"+subscriptionID, token, "")
				if err != nil || deactivated.status != http.StatusOK {
					failures <- fmt.Sprintf("deactivate %s status=%d error=%v", eventType, deactivated.status, err)
				}
				patched, err := f.roundTrip(http.MethodPatch, f.endpoint(endpointID), token,
					fmt.Sprintf(`{"description":"stress-%d-%d"}`, worker, iteration), "If-Match", "*")
				if err != nil || patched.status != http.StatusOK {
					failures <- fmt.Sprintf("patch %d/%d status=%d error=%v", worker, iteration, patched.status, err)
				}
			}
		}(worker)
	}
	close(start)
	group.Wait()
	close(failures)
	for failure := range failures {
		t.Errorf("lock-order stress: %s", failure)
	}
	if t.Failed() {
		return
	}

	if got := countMgmtSubscriptions(t, f, endpointID, false); got != workers*iterations {
		t.Fatalf("subscription rows = %d, want %d", got, workers*iterations)
	}
	if got := countMgmtSubscriptions(t, f, endpointID, true); got != 0 {
		t.Fatalf("active subscription rows = %d, want 0", got)
	}
	if got := f.auditCount("subscription.created"); got != workers*iterations {
		t.Fatalf("subscription.created audit rows = %d, want %d", got, workers*iterations)
	}
	if got := f.auditCount("subscription.deactivated"); got != workers*iterations {
		t.Fatalf("subscription.deactivated audit rows = %d, want %d", got, workers*iterations)
	}
	if got := f.auditCount("endpoint.updated"); got != workers*iterations {
		t.Fatalf("endpoint.updated audit rows = %d, want %d", got, workers*iterations)
	}
	var duplicates int
	if err := f.pool.QueryRow(context.Background(),
		`SELECT count(*) FROM (
		   SELECT event_type FROM endpoint_subscriptions
		    WHERE organization_id = $1::uuid AND endpoint_id = $2::uuid
		    GROUP BY event_type HAVING count(*) > 1
		 ) duplicates`, f.orgID, endpointID).Scan(&duplicates); err != nil {
		t.Fatalf("check duplicate subscriptions: %v", err)
	}
	if duplicates != 0 {
		t.Fatalf("duplicate event-type rows = %d, want 0", duplicates)
	}

	wantStatus(t, f.do(http.MethodDelete, f.endpoint(endpointID), token, ""), http.StatusNoContent, "soft delete")
	rows := countMgmtSubscriptions(t, f, endpointID, false)
	createdAudits := f.auditCount("subscription.created")
	for i := 0; i < 4; i++ {
		wantProblem(t, f.do(http.MethodPost, path, token,
			fmt.Sprintf(`{"event_type":"after.delete.%d"}`, i)), http.StatusNotFound, "not_found", "")
	}
	if got := countMgmtSubscriptions(t, f, endpointID, false); got != rows {
		t.Fatalf("post-delete writes changed row count (%d -> %d)", rows, got)
	}
	if got := f.auditCount("subscription.created"); got != createdAudits {
		t.Fatalf("post-delete writes added audit rows (%d -> %d)", createdAudits, got)
	}
}
