//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	eventapp "github.com/metamin44/hookflow/internal/app/event"
	"github.com/metamin44/hookflow/internal/modules/event"
)

// stage2WaitingPID waits for exactly one other backend to block on a lock and
// returns its pid. The Stage 2 fixtures run without a broker or competing
// consumers, so the only lock waiter is the statement under test.
func stage2WaitingPID(t *testing.T, pool *pgxpool.Pool) int32 {
	t.Helper()
	var pid int32
	eventually(t, 5*time.Second, "one backend waiting on a lock", func() error {
		err := pool.QueryRow(context.Background(), `
			SELECT pid
			  FROM pg_stat_activity
			 WHERE datname = current_database()
			   AND pid <> pg_backend_pid()
			   AND wait_event_type = 'Lock'
			   AND state = 'active'`).Scan(&pid)
		if errors.Is(err, pgx.ErrNoRows) {
			return errors.New("no waiting backend yet")
		}
		return err
	})
	return pid
}

// stage2RelationLock reports whether pid holds any lock on the named relation
// and whether one of those locks is granted.
func stage2RelationLock(t *testing.T, pool *pgxpool.Pool, pid int32, relation string) (held, granted bool) {
	t.Helper()
	if err := pool.QueryRow(context.Background(), `
		SELECT count(*) > 0, COALESCE(bool_or(l.granted), false)
		  FROM pg_locks l
		  JOIN pg_class c ON c.oid = l.relation
		 WHERE l.pid = $1 AND c.relname = $2`, pid, relation).Scan(&held, &granted); err != nil {
		t.Fatalf("inspect %s locks for pid %d: %v", relation, pid, err)
	}
	return held, granted
}

// TestIngestionOrganizationLockPrecedesClaimIntegration holds the organization
// row with the management lock mode and proves the acceptance backend blocks on
// that organization lock without ever touching idempotency_records. If the claim
// ran before the lock, the blocked transaction would hold a relation lock on
// idempotency_records; it must not.
func TestIngestionOrganizationLockPrecedesClaimIntegration(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "lock-order", "active", `{}`)
	f.subscribe(org, endpoint, "lock.order", true)

	ctx := context.Background()
	blocker, err := f.pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin blocker transaction: %v", err)
	}
	defer func() { _ = blocker.Rollback(ctx) }()
	var lockedID string
	if err := blocker.QueryRow(ctx,
		`SELECT id::text FROM organizations WHERE id = $1::uuid FOR NO KEY UPDATE`, org,
	).Scan(&lockedID); err != nil {
		t.Fatalf("take organization lock: %v", err)
	}

	done := make(chan error, 1)
	go func() {
		_, acceptErr := f.accept(org, "lock-order-key", ingestBody("lock.order", `{"n":1}`))
		done <- acceptErr
	}()

	pid := stage2WaitingPID(t, f.pool)
	if held, granted := stage2RelationLock(t, f.pool, pid, "organizations"); !held || !granted {
		t.Fatalf("acceptance backend does not hold the organization lock (held=%v granted=%v)", held, granted)
	}
	if held, _ := stage2RelationLock(t, f.pool, pid, "idempotency_records"); held {
		t.Fatal("acceptance backend reached the idempotency claim before acquiring the organization lock")
	}
	t.Logf("acceptance backend pid %d waits holding a granted organizations lock and no idempotency_records lock", pid)
	select {
	case acceptErr := <-done:
		t.Fatalf("AcceptEvent returned while the organization row was still locked: %v", acceptErr)
	default:
	}

	if err := blocker.Rollback(ctx); err != nil {
		t.Fatalf("release organization lock: %v", err)
	}
	select {
	case acceptErr := <-done:
		if acceptErr != nil {
			t.Fatalf("AcceptEvent after lock release: %v", acceptErr)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("AcceptEvent did not proceed after the organization lock was released")
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
		t.Errorf("events = %d, want 1", events)
	}
	if records := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`, org); records != 1 {
		t.Errorf("idempotency records = %d, want 1", records)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid`, org); deliveries != 1 {
		t.Errorf("deliveries = %d, want 1", deliveries)
	}
	if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != 1 {
		t.Errorf("outbox rows = %d, want 1", outboxRows)
	}
}

// TestIngestionSubscriptionRaceIntegration races acceptance against an
// endpoint subscription insert and asserts that every observed outcome matches
// one of the two valid serialization orders. It also pins each order with a
// lock-controlled subtest.
func TestIngestionSubscriptionRaceIntegration(t *testing.T) {
	t.Run("concurrent serialization outcomes", func(t *testing.T) {
		f := newIngestFixture(t)
		bothOrders := map[bool]int{}
		for iteration := 0; iteration < 10; iteration++ {
			org := f.createOrg("active")
			name := fmt.Sprintf("race-%02d", iteration)
			endpoint := f.createEndpoint(org, name, "active", `{"x-race":"1"}`)
			key := fmt.Sprintf("race-key-%02d", iteration)
			subscriptionID := mustID(t)

			start := make(chan struct{})
			var wg sync.WaitGroup
			var subscriptionErr error
			var result eventapp.Result
			var acceptErr error
			wg.Add(2)
			go func() {
				defer wg.Done()
				<-start
				_, subscriptionErr = f.pool.Exec(context.Background(),
					`INSERT INTO endpoint_subscriptions
					     (id, organization_id, endpoint_id, event_type, is_active)
					 VALUES ($1::uuid, $2::uuid, $3::uuid, 'race.test', true)`,
					subscriptionID, org, endpoint)
			}()
			go func() {
				defer wg.Done()
				<-start
				result, acceptErr = f.accept(org, key, ingestBody("race.test", `{"n":1}`))
			}()
			close(start)
			wg.Wait()

			if subscriptionErr != nil {
				t.Fatalf("iteration %d subscription insert: %v", iteration, subscriptionErr)
			}
			if acceptErr != nil {
				t.Fatalf("iteration %d accept: %v", iteration, acceptErr)
			}
			if active := f.count(`SELECT count(*) FROM endpoint_subscriptions
			                       WHERE organization_id = $1::uuid AND is_active`, org); active != 1 {
				t.Fatalf("iteration %d active subscriptions = %d, want 1", iteration, active)
			}
			if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
				t.Fatalf("iteration %d events = %d, want 1", iteration, events)
			}
			if records := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`, org); records != 1 {
				t.Fatalf("iteration %d idempotency records = %d, want 1", iteration, records)
			}
			deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, result.EventID)
			if deliveries != result.DestinationCount {
				t.Fatalf("iteration %d destination_count %d != deliveries %d",
					iteration, result.DestinationCount, deliveries)
			}
			outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org)
			if outboxRows != deliveries {
				t.Fatalf("iteration %d outbox rows = %d, want %d", iteration, outboxRows, deliveries)
			}
			switch result.DestinationCount {
			case 0:
				if deliveries != 0 || outboxRows != 0 {
					t.Fatalf("iteration %d zero-destination accept wrote rows", iteration)
				}
			case 1:
				var (
					endpointID string
					url        string
					secret     string
				)
				if err := f.pool.QueryRow(context.Background(), `
					SELECT endpoint_id::text, url, secret_encrypted
					  FROM deliveries WHERE event_id = $1::uuid`, result.EventID,
				).Scan(&endpointID, &url, &secret); err != nil {
					t.Fatalf("iteration %d read snapshot: %v", iteration, err)
				}
				if endpointID != endpoint || url != "https://example.test/"+name || secret != "cipher-"+name {
					t.Fatalf("iteration %d snapshot = endpoint %q url %q secret %q",
						iteration, endpointID, url, secret)
				}
				if paired := f.count(`SELECT count(*) FROM outbox_messages o
				                       JOIN deliveries d ON d.id = o.delivery_id
				                      WHERE d.event_id = $1::uuid`, result.EventID); paired != 1 {
					t.Fatalf("iteration %d outbox/delivery pairs = %d, want 1", iteration, paired)
				}
			default:
				t.Fatalf("iteration %d destination count = %d, want 0 or 1", iteration, result.DestinationCount)
			}
			bothOrders[result.DestinationCount == 1]++
		}
		t.Logf("serialization outcomes over 10 iterations: subscription-first=%d acceptance-first=%d",
			bothOrders[true], bothOrders[false])
	})

	t.Run("management mutation commits first", func(t *testing.T) {
		f := newIngestFixture(t)
		org := f.createOrg("active")
		endpoint := f.createEndpoint(org, "mutation-first", "active", `{}`)

		ctx := context.Background()
		blocker, err := f.pool.Begin(ctx)
		if err != nil {
			t.Fatalf("begin blocker transaction: %v", err)
		}
		defer func() { _ = blocker.Rollback(ctx) }()
		var lockedID string
		if err := blocker.QueryRow(ctx,
			`SELECT id::text FROM organizations WHERE id = $1::uuid FOR NO KEY UPDATE`, org,
		).Scan(&lockedID); err != nil {
			t.Fatalf("take organization lock: %v", err)
		}

		done := make(chan eventapp.Result, 1)
		failures := make(chan error, 1)
		go func() {
			result, acceptErr := f.accept(org, "mutation-first-key", ingestBody("mutation.first", `{"n":1}`))
			if acceptErr != nil {
				failures <- acceptErr
				return
			}
			done <- result
		}()
		stage2WaitingPID(t, f.pool)

		subscriptionID := mustID(t)
		if _, err := blocker.Exec(ctx,
			`INSERT INTO endpoint_subscriptions
			     (id, organization_id, endpoint_id, event_type, is_active)
			 VALUES ($1::uuid, $2::uuid, $3::uuid, 'mutation.first', true)`,
			subscriptionID, org, endpoint); err != nil {
			t.Fatalf("insert subscription under lock: %v", err)
		}
		if err := blocker.Commit(ctx); err != nil {
			t.Fatalf("commit subscription: %v", err)
		}

		select {
		case acceptErr := <-failures:
			t.Fatalf("accept failed: %v", acceptErr)
		case result := <-done:
			if result.DestinationCount != 1 {
				t.Fatalf("destination count = %d, want 1 (subscription committed first)", result.DestinationCount)
			}
			if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, result.EventID); deliveries != 1 {
				t.Errorf("deliveries = %d, want 1", deliveries)
			}
			if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != 1 {
				t.Errorf("outbox rows = %d, want 1", outboxRows)
			}
		case <-time.After(10 * time.Second):
			t.Fatal("accept did not resume after the management mutation committed")
		}
	})

	t.Run("acceptance commits first", func(t *testing.T) {
		f := newIngestFixture(t)
		org := f.createOrg("active")
		endpoint := f.createEndpoint(org, "acceptance-first", "active", `{}`)

		first := f.mustAccept(org, "acceptance-first-key", ingestBody("acceptance.first", `{"n":1}`))
		if first.DestinationCount != 0 {
			t.Fatalf("pre-subscription destination count = %d, want 0", first.DestinationCount)
		}
		f.subscribe(org, endpoint, "acceptance.first", true)
		second := f.mustAccept(org, "acceptance-second-key", ingestBody("acceptance.first", `{"n":2}`))
		if second.DestinationCount != 1 {
			t.Fatalf("post-subscription destination count = %d, want 1", second.DestinationCount)
		}
		if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, first.EventID); deliveries != 0 {
			t.Errorf("first event deliveries = %d, want 0", deliveries)
		}
		if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, second.EventID); deliveries != 1 {
			t.Errorf("second event deliveries = %d, want 1", deliveries)
		}
	})
}

// TestIngestionConcurrentMixedHashIntegration races contenders that share one
// organization and idempotency key but carry two different payloads. Exactly
// one hash wins; same-hash losers replay, different-hash losers conflict.
func TestIngestionConcurrentMixedHashIntegration(t *testing.T) {
	f := newIngestFixture(t)
	conflicts, replays := 0, 0
	winnerHashes := map[int]int{}
	for iteration := 0; iteration < 5; iteration++ {
		org := f.createOrg("active")
		endpoint := f.createEndpoint(org, fmt.Sprintf("mixed-%02d", iteration), "active", `{}`)
		f.subscribe(org, endpoint, "mixed.hash", true)
		key := fmt.Sprintf("mixed-key-%02d", iteration)
		bodies := [][]byte{
			ingestBody("mixed.hash", `{"n":1}`),
			ingestBody("mixed.hash", `{"n":2}`),
		}

		const contenders = 8
		results := make([]eventapp.Result, contenders)
		errs := make([]error, contenders)
		start := make(chan struct{})
		var wg sync.WaitGroup
		for i := 0; i < contenders; i++ {
			wg.Add(1)
			go func(index int) {
				defer wg.Done()
				<-start
				results[index], errs[index] = f.accept(org, key, bodies[index%2])
			}(i)
		}
		close(start)
		wg.Wait()

		winner := -1
		for i := 0; i < contenders; i++ {
			if errs[i] == nil && !results[i].Replayed {
				if winner != -1 {
					t.Fatalf("iteration %d: contenders %d and %d both own the key", iteration, winner, i)
				}
				winner = i
			}
		}
		if winner == -1 {
			t.Fatalf("iteration %d: no contender owned the key", iteration)
		}
		winnerBody := bodies[winner%2]
		winnerID := results[winner].EventID
		winnerHashes[winner%2]++
		for i := 0; i < contenders; i++ {
			switch {
			case errs[i] != nil:
				if !errors.Is(errs[i], eventapp.ErrIdempotencyConflict) {
					t.Fatalf("iteration %d contender %d error = %v, want conflict", iteration, i, errs[i])
				}
				if bytes.Equal(bodies[i%2], winnerBody) {
					t.Fatalf("iteration %d contender %d with the winning hash conflicted", iteration, i)
				}
				conflicts++
			case results[i].Replayed:
				if results[i].EventID != winnerID {
					t.Fatalf("iteration %d contender %d replayed event %s, want %s",
						iteration, i, results[i].EventID, winnerID)
				}
				if !bytes.Equal(bodies[i%2], winnerBody) {
					t.Fatalf("iteration %d contender %d replayed with a different hash", iteration, i)
				}
				replays++
			default:
				if i != winner {
					t.Fatalf("iteration %d contender %d unexpectedly owned the key", iteration, i)
				}
			}
		}

		if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
			t.Fatalf("iteration %d events = %d, want 1", iteration, events)
		}
		var storedHash []byte
		var linkedEvent string
		if err := f.pool.QueryRow(context.Background(), `
			SELECT request_hash, COALESCE(event_id::text, '')
			  FROM idempotency_records
			 WHERE organization_id = $1::uuid AND idempotency_key = $2`, org, key,
		).Scan(&storedHash, &linkedEvent); err != nil {
			t.Fatalf("iteration %d read idempotency record: %v", iteration, err)
		}
		document, err := event.Parse(winnerBody)
		if err != nil {
			t.Fatalf("iteration %d parse winning body: %v", iteration, err)
		}
		fingerprint, err := document.Fingerprint()
		if err != nil {
			t.Fatalf("iteration %d fingerprint winning body: %v", iteration, err)
		}
		if !bytes.Equal(storedHash, fingerprint[:]) {
			t.Fatalf("iteration %d stored hash does not match the winning payload", iteration)
		}
		if linkedEvent != winnerID {
			t.Fatalf("iteration %d link = %s, want %s", iteration, linkedEvent, winnerID)
		}
		if records := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`, org); records != 1 {
			t.Fatalf("iteration %d idempotency records = %d, want 1", iteration, records)
		}
		deliveries := f.count(`SELECT count(*) FROM deliveries WHERE event_id = $1::uuid`, winnerID)
		if deliveries != results[winner].DestinationCount {
			t.Fatalf("iteration %d destination_count %d != deliveries %d",
				iteration, results[winner].DestinationCount, deliveries)
		}
		outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org)
		if outboxRows != deliveries {
			t.Fatalf("iteration %d outbox rows = %d, want %d", iteration, outboxRows, deliveries)
		}
		if stray := f.count(`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid AND event_id <> $2::uuid`,
			org, winnerID); stray != 0 {
			t.Fatalf("iteration %d stray deliveries = %d", iteration, stray)
		}
	}
	t.Logf("8 contenders x 5 iterations: conflicts=%d replays=%d winning payload n=1:%d n=2:%d",
		conflicts, replays, winnerHashes[0], winnerHashes[1])
}

// TestIngestionAcceptCommitFailureIntegration installs a test-owned deferred
// constraint trigger on events that fails at commit for one event type. The
// trigger and its function exist only for this test and are dropped in cleanup.
func TestIngestionAcceptCommitFailureIntegration(t *testing.T) {
	f := newIngestFixture(t)
	org := f.createOrg("active")
	endpoint := f.createEndpoint(org, "commit-failure", "active", `{}`)
	f.subscribe(org, endpoint, "commit.ok", true)

	suffix := strings.ReplaceAll(mustID(t), "-", "")
	functionName := "hookflow_test_commit_fail_" + suffix
	triggerName := functionName + "_trg"
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		if _, err := f.pool.Exec(ctx, `DROP TRIGGER IF EXISTS `+triggerName+` ON events`); err != nil {
			t.Errorf("drop test-owned trigger: %v", err)
		}
		if _, err := f.pool.Exec(ctx, `DROP FUNCTION IF EXISTS `+functionName+`()`); err != nil {
			t.Errorf("drop test-owned function: %v", err)
		}
	})
	f.exec(`CREATE FUNCTION ` + functionName + `() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.type = 'commit.fail.test' THEN
        RAISE EXCEPTION 'test-owned deferred commit failure';
    END IF;
    RETURN NEW;
END $$`)
	f.exec(`CREATE CONSTRAINT TRIGGER ` + triggerName + `
	    AFTER INSERT ON events
	    DEFERRABLE INITIALLY DEFERRED
	    FOR EACH ROW EXECUTE FUNCTION ` + functionName + `()`)

	result, err := f.accept(org, "commit-failure-key", ingestBody("commit.fail.test", `{"n":1}`))
	if err == nil {
		t.Fatal("AcceptEvent succeeded despite the deferred commit failure")
	}
	t.Logf("AcceptEvent surfaced the deferred commit failure: %v", err)
	if result.EventID != "" || result.DestinationCount != 0 {
		t.Fatalf("failed acceptance exposed a result: %+v", result)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid AND type = 'commit.fail.test'`, org); events != 0 {
		t.Errorf("failed commit left %d events", events)
	}
	if records := f.count(`SELECT count(*) FROM idempotency_records WHERE organization_id = $1::uuid`, org); records != 0 {
		t.Errorf("failed commit left %d idempotency rows", records)
	}
	if deliveries := f.count(`SELECT count(*) FROM deliveries WHERE organization_id = $1::uuid`, org); deliveries != 0 {
		t.Errorf("failed commit left %d deliveries", deliveries)
	}
	if outboxRows := f.count(`SELECT count(*) FROM outbox_messages WHERE organization_id = $1::uuid`, org); outboxRows != 0 {
		t.Errorf("failed commit left %d outbox rows", outboxRows)
	}
	if attempts := f.count(`SELECT count(*) FROM delivery_attempts WHERE organization_id = $1::uuid`, org); attempts != 0 {
		t.Errorf("failed commit left %d attempt rows", attempts)
	}

	// The connection must recover: a normal acceptance still commits.
	ok := f.mustAccept(org, "commit-ok-key", ingestBody("commit.ok", `{"n":2}`))
	if ok.DestinationCount != 1 {
		t.Fatalf("recovery destination count = %d, want 1", ok.DestinationCount)
	}
	if events := f.count(`SELECT count(*) FROM events WHERE organization_id = $1::uuid`, org); events != 1 {
		t.Errorf("events after recovery = %d, want 1", events)
	}
}
