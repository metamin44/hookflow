//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/app/publisher"
	"github.com/metamin44/hookflow/internal/app/recovery"
	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

// holdObservingProcessor delegates every decision to the production Worker
// runtime. It only records the first Hold, which is what the real consumer
// returns when the broker delivers a pointer before Publisher T2 commits. The
// consumer then leaves the message unacknowledged, so closing its channel
// requeues the exact pointer for the next generation.
//
// The stop function is installed before the pointer is published, so the
// requeue is deterministic rather than depending on reconnect races.
type holdObservingProcessor struct {
	delegate worker.Processor
	stop     <-chan context.CancelFunc
	held     chan struct{}
}

func (p *holdObservingProcessor) Process(ctx context.Context, handle *worker.Handle) disposition.Decision {
	decision := p.delegate.Process(ctx, handle)
	if decision.Action == disposition.ActionHold {
		select {
		case stop := <-p.stop:
			stop()
		default:
		}
		p.held <- struct{}{}
	}
	return decision
}

// dispatchReadyMessages reads the broker's own ready count. The management API
// samples queue counters, so it can briefly report the pre-consumption state;
// a passive declare is current and is used for the requeue transition.
func dispatchReadyMessages(t *testing.T, f *fixture) int {
	t.Helper()
	queue, err := f.inspectCh.QueueDeclarePassive(messaging.DispatchQueue, false, false, false, false, nil)
	if err != nil {
		t.Fatalf("passive dispatch queue declare: %v", err)
	}
	return queue.Messages
}

// TestPublisherWorkerLifecycleAfterPublisherLeaseExpiryIntegration exercises the
// composite Publisher/Worker lifecycle after a publication outcome was never
// recorded and the Publisher lease had to be reaped.
//
// Ordering is the subject of the test:
//  1. T1 must commit the claim tuple before the pointer is published.
//  2. A pointer can reach the Worker while the outbox row is still publishing;
//     the Worker must hold it without HTTP or an attempt.
//  3. T2 and Recovery must be allowed to finish before the next generation is
//     published, otherwise a premature pointer could race the current one.
//  4. Only the current published generation may execute HTTP. Older pointers
//     are ACKed from durable state, and redelivery after completion must not
//     execute a second HTTP request.
//
// The scenario deliberately produces exactly one attempt row: the failed
// publication outcome and the stale/exhausted pointers are broker dispositions,
// not HTTP attempts, and none of them writes deliveries.attempt_count.
func TestPublisherWorkerLifecycleAfterPublisherLeaseExpiryIntegration(t *testing.T) {
	f := newFixture(t)

	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	firstOutboxID := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
	outboxStore := outbox.NewStore(f.pool, f.logger)

	// Step 1: Publisher T1 commits a visible claim before any publish.
	firstClaim := f.claimOne(t)
	if firstClaim.MessageID != firstOutboxID || firstClaim.Sequence != 1 || firstClaim.ClaimCount != 1 {
		t.Fatalf("first claim = %+v, want outbox %s sequence 1 count 1", firstClaim, firstOutboxID)
	}
	session := newPublisherSession(t, f)
	result, err := session.Publish(context.Background(), firstClaim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("publish first pointer = %q, %v; want confirmed", result, err)
	}
	rows := f.readOutboxes(t, d.id)
	if len(rows) != 1 || rows[0].status != "publishing" || rows[0].attempts != 1 {
		t.Fatalf("T1 was not visible as publishing before publish: %+v", rows)
	}

	// Step 2: Establish a deterministic T2 failure by letting the Publisher lease
	// expire. T3 returns the row to pending with the persisted attempt count; it
	// never records publication success.
	f.agePublisherLease(t, firstOutboxID)
	reap, err := outboxStore.ReapExpired(context.Background(), outbox.ReapOptions{
		BatchSize:     10,
		MaxClaims:     3,
		LeaseDuration: time.Millisecond,
		Backoff:       func(int) (time.Duration, error) { return time.Nanosecond, nil },
	})
	if err != nil {
		t.Fatalf("reap expired Publisher lease: %v", err)
	}
	if len(reap.Reaped) != 1 || reap.Reaped[0].Status != "pending" || reap.Reaped[0].Attempts != 1 {
		t.Fatalf("reaped lease = %+v, want one pending row at attempt 1", reap.Reaped)
	}
	rows = f.readOutboxes(t, d.id)
	if len(rows) != 1 || rows[0].status != "pending" || rows[0].attempts != 1 || rows[0].publishedAt.Valid {
		t.Fatalf("reaped outbox row did not record a failed publication: %+v", rows)
	}

	// Step 3: The already-published pointer is premature relative to durable
	// state (the outbox row is pending, not published). A real Worker consumer
	// acquires it, classifies it stale, and ACKs without HTTP or an attempt.
	cfg := testWorkerConfig(1)
	workerRuntime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	staleRun := startConsumer(t, f.newConsumer(t, cfg, workerRuntime))
	eventually(t, 6*time.Second, "premature pointer to be ACKed from durable state", func() error {
		if got := f.metricValue(t, "hookflow_worker_stale_total"); got != 1 {
			return fmt.Errorf("stale metric = %d, want 1", got)
		}
		if got := f.metricValue(t, "hookflow_worker_acked_total"); got != 1 {
			return fmt.Errorf("ACK metric = %d, want 1", got)
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	staleRun.stop(t)
	if requests.Load() != 0 || f.attemptCount(t, d.id) != 0 {
		t.Fatalf("premature pointer reached HTTP or wrote an attempt: requests=%d attempts=%d",
			requests.Load(), f.attemptCount(t, d.id))
	}
	if row := f.readDelivery(t, d.id); row.state != "pending" || row.dispatchSeq != 1 ||
		row.attemptCount != 0 || row.leaseToken != "" || !row.nextAttemptAt.Valid {
		t.Fatalf("premature pointer mutated live delivery state: %+v", row)
	}

	// Step 4: Publisher re-claims the re-armed row. The Worker is started before
	// publish so the pointer cannot race a reconnect: it must hold the pointer
	// while the outbox row is publishing and leave it requeued.
	secondClaim := f.claimOne(t)
	if secondClaim.MessageID != firstOutboxID || secondClaim.Sequence != 1 || secondClaim.ClaimCount != 2 {
		t.Fatalf("second claim = %+v, want the re-armed row at count 2", secondClaim)
	}
	holdStop := make(chan context.CancelFunc, 1)
	holding := &holdObservingProcessor{
		delegate: workerRuntime,
		stop:     holdStop,
		held:     make(chan struct{}, 1),
	}
	holdRun := startConsumer(t, f.newConsumer(t, cfg, holding))
	holdStop <- holdRun.cancel
	result, err = session.Publish(context.Background(), secondClaim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("publish held pointer = %q, %v; want confirmed", result, err)
	}
	select {
	case <-holding.held:
	case <-time.After(6 * time.Second):
		t.Fatal("worker did not hold a pointer published before T2")
	}
	eventually(t, 4*time.Second, "held pointer to be requeued by channel close", func() error {
		if ready := dispatchReadyMessages(t, f); ready != 1 {
			return fmt.Errorf("dispatch queue ready = %d, want the requeued pointer", ready)
		}
		return nil
	})
	holdRun.stop(t)
	rows = f.readOutboxes(t, d.id)
	if len(rows) != 1 || rows[0].status != "publishing" || rows[0].attempts != 2 {
		t.Fatalf("hold mutated the publishing row: %+v", rows)
	}
	if requests.Load() != 0 || f.attemptCount(t, d.id) != 0 {
		t.Fatalf("held pointer reached HTTP or wrote an attempt: requests=%d attempts=%d",
			requests.Load(), f.attemptCount(t, d.id))
	}
	if got := f.metricValue(t, "hookflow_worker_publication_pending_total"); got != 1 {
		t.Fatalf("publication_pending metric = %d, want 1", got)
	}

	applied, err := outboxStore.MarkPublished(context.Background(), secondClaim)
	if err != nil || !applied {
		t.Fatalf("mark requeued generation published = %t, %v; want applied", applied, err)
	}
	rows = f.readOutboxes(t, d.id)
	if len(rows) != 1 || rows[0].status != "published" || rows[0].attempts != 2 || !rows[0].publishedAt.Valid {
		t.Fatalf("T2 did not publish the requeued generation: %+v", rows)
	}

	// Step 5: Recovery advances the stale published generation. This is the
	// operation that makes the next dispatch generation visible to T1.
	sweep, err := recovery.NewSweep(f.pool, recovery.Options{
		Budget: recovery.Budget{
			Total:  2 * time.Second,
			Expire: recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
			Repair: recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
			ReArm:  recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
			Stale:  recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
		},
		StalePublishedAfter: time.Nanosecond,
		BuildMessage:        recovery.BuildMessage,
		Logger:              f.logger,
	})
	if err != nil {
		t.Fatalf("create Recovery Sweep: %v", err)
	}
	var report recovery.Report
	eventually(t, 2*time.Second, "Recovery to advance the stale published generation", func() error {
		report, err = sweep.Run(context.Background())
		if err != nil {
			return err
		}
		if report.Stale.Applied != 1 {
			return fmt.Errorf("stale applied = %d, want 1", report.Stale.Applied)
		}
		return nil
	})
	if report.Expire.Applied != 0 || report.Repair.Applied != 0 || report.ReArm.Applied != 0 {
		t.Fatalf("unexpected Recovery work beyond the stale pass: %+v", report)
	}
	rows = f.readOutboxes(t, d.id)
	if len(rows) != 2 || rows[0].id != firstOutboxID || rows[0].status != "published" ||
		rows[1].sequence != 2 || rows[1].status != "pending" || rows[1].attempts != 0 {
		t.Fatalf("Recovery did not create one pending generation 2: %+v", rows)
	}
	if row := f.readDelivery(t, d.id); row.state != "pending" || row.dispatchSeq != 2 ||
		row.attemptCount != 0 || row.leaseToken != "" {
		t.Fatalf("Recovery did not advance the live generation safely: %+v", row)
	}
	secondOutboxID := rows[1].id

	// Step 6: Publisher publishes generation 2 and records T2. The requeued
	// generation 1 pointer is still in the queue and must become stale.
	thirdClaim := f.claimOne(t)
	if thirdClaim.MessageID != secondOutboxID || thirdClaim.Sequence != 2 || thirdClaim.ClaimCount != 1 {
		t.Fatalf("third claim = %+v, want generation 2 count 1", thirdClaim)
	}
	result, err = session.Publish(context.Background(), thirdClaim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("publish generation 2 = %q, %v; want confirmed", result, err)
	}
	applied, err = outboxStore.MarkPublished(context.Background(), thirdClaim)
	if err != nil || !applied {
		t.Fatalf("mark generation 2 published = %t, %v; want applied", applied, err)
	}
	eventually(t, 4*time.Second, "both live queue pointers to be ready", func() error {
		if ready := dispatchReadyMessages(t, f); ready != 2 {
			return fmt.Errorf("dispatch queue ready = %d, want the requeued generation 1 and generation 2", ready)
		}
		return nil
	})

	// Step 7: The real Worker consumes both pointers. Only the current published
	// generation may execute HTTP and write an attempt. The same runtime is reused
	// so the runtime-scoped metrics remain cumulative across the lifecycle.
	inspector := &inspectingProcessor{delegate: workerRuntime, bodies: make(chan []byte, 4)}
	finalConsumer := f.newConsumer(t, cfg, inspector)
	ackedBeforeFinal := f.metricValue(t, "hookflow_worker_acked_total")
	finalRun := startConsumer(t, finalConsumer)
	eventually(t, 10*time.Second, "generation 2 to complete with one HTTP request", func() error {
		row := f.readDelivery(t, d.id)
		if row.state != "succeeded" || row.dispatchSeq != 2 || row.attemptCount != 1 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded at generation 2"}
		}
		if got := f.metricValue(t, "hookflow_worker_acked_total"); got != ackedBeforeFinal+2 {
			return fmt.Errorf("final ACK metric = %d, want %d", got, ackedBeforeFinal+2)
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		if requests.Load() != 1 {
			return fmt.Errorf("HTTP requests = %d, want 1", requests.Load())
		}
		return nil
	})
	finalRun.stop(t)

	seen := make(map[int]bool, 2)
	for range len(inspector.bodies) {
		var envelope messaging.Envelope
		if err := json.Unmarshal(<-inspector.bodies, &envelope); err != nil {
			t.Fatalf("decode consumed pointer: %v", err)
		}
		seen[envelope.DispatchSeq] = true
	}
	if len(seen) != 2 || !seen[1] || !seen[2] {
		t.Fatalf("consumer processed generations %v, want the stale generation 1 and current generation 2", seen)
	}

	final := f.readDelivery(t, d.id)
	if final.state != "succeeded" {
		// The implemented terminal success state is "succeeded"; this test must
		// assert the real durable state rather than relabeling it "delivered".
		t.Fatalf("delivery state = %q, want the implemented success state %q", final.state, "succeeded")
	}
	if final.dispatchSeq != 2 || final.attemptCount != 1 || final.nextAttemptAt.Valid ||
		final.leaseToken != "" || final.leaseExpiresAt.Valid || !final.completedAt.Valid ||
		!final.lastResponseStatus.Valid || int(final.lastResponseStatus.Int32) != http.StatusNoContent {
		t.Fatalf("completed delivery did not clear retry/lease state: %+v", final)
	}
	if requests.Load() != 1 || f.attemptCount(t, d.id) != 1 || f.attemptOutcome(t, d.id) != "success" {
		t.Fatalf("not exactly one successful HTTP attempt: requests=%d attempts=%d outcome=%q",
			requests.Load(), f.attemptCount(t, d.id), f.attemptOutcome(t, d.id))
	}
	rows = f.readOutboxes(t, d.id)
	if len(rows) != 2 || rows[0].id != firstOutboxID || rows[0].status != "published" || rows[0].attempts != 2 ||
		rows[1].id != secondOutboxID || rows[1].status != "published" || rows[1].attempts != 1 {
		t.Fatalf("completed delivery left unintended outbox work: %+v", rows)
	}
	if got := f.metricValue(t, "hookflow_worker_acked_total"); got != ackedBeforeFinal+2 {
		t.Fatalf("final ACK metric = %d, want %d (stale generation 1 and generation 2); stale=%d duplicate=%d stale_handle=%d processed=%d",
			got, ackedBeforeFinal+2, f.metricValue(t, "hookflow_worker_stale_total"),
			f.metricValue(t, "hookflow_worker_duplicate_total"),
			f.metricValue(t, "hookflow_worker_stale_handle_total"),
			f.metricValue(t, "hookflow_worker_processed_total"))
	}
	if got := f.metricValue(t, "hookflow_worker_processed_total"); got != 4 {
		t.Fatalf("processed metric = %d, want 4 (premature, held, stale generation 1, generation 2)", got)
	}
	if got := f.metricValue(t, "hookflow_worker_stale_total"); got != 2 {
		t.Fatalf("stale metric = %d, want 2 (premature and stale generation 1)", got)
	}
	if got := f.metricValue(t, "hookflow_worker_publication_pending_total"); got != 1 {
		t.Fatalf("publication_pending metric = %d, want 1", got)
	}
	if got := f.metricValue(t, "hookflow_worker_succeeded_total"); got != 1 {
		t.Fatalf("succeeded metric = %d, want 1", got)
	}

	// Step 8: Redeliver the completed generation. The exact pointer must be
	// ACKed from terminal state without a second HTTP request or any mutation.
	before := f.readDelivery(t, d.id)
	beforeOutboxes := f.readOutboxes(t, d.id)
	f.publishPointer(t, d.id, secondOutboxID, 2)
	duplicateConsumer := f.newConsumer(t, cfg, workerRuntime)
	ackedBeforeDuplicate := f.metricValue(t, "hookflow_worker_acked_total")
	duplicateRun := startConsumer(t, duplicateConsumer)
	eventually(t, 10*time.Second, "duplicate pointer after completion to be ACKed", func() error {
		if got := f.metricValue(t, "hookflow_worker_acked_total"); got != ackedBeforeDuplicate+1 {
			return fmt.Errorf("ACK metric = %d, want %d", got, ackedBeforeDuplicate+1)
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	duplicateRun.stop(t)
	if requests.Load() != 1 || f.attemptCount(t, d.id) != 1 {
		t.Fatalf("duplicate redelivery executed HTTP or wrote an attempt: requests=%d attempts=%d",
			requests.Load(), f.attemptCount(t, d.id))
	}
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("duplicate redelivery mutated the completed delivery: before=%+v after=%+v", before, after)
	}
	afterOutboxes := f.readOutboxes(t, d.id)
	if len(afterOutboxes) != len(beforeOutboxes) {
		t.Fatalf("duplicate redelivery changed outbox rows: before=%+v after=%+v", beforeOutboxes, afterOutboxes)
	}
	for i := range beforeOutboxes {
		if beforeOutboxes[i] != afterOutboxes[i] {
			t.Fatalf("duplicate redelivery mutated outbox row %d: before=%+v after=%+v", i, beforeOutboxes[i], afterOutboxes[i])
		}
	}
}
