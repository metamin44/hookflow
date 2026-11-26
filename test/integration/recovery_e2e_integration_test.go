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

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/app/publisher"
	"github.com/metamin44/hookflow/internal/app/recovery"
	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

const poisonDeliveryAttempts = 21

type leaseRejectProcessor struct {
	store   *delivery.Store
	pointer delivery.Pointer
	calls   atomic.Int64
	errors  chan error
}

func (p *leaseRejectProcessor) Process(ctx context.Context, handle *worker.Handle) disposition.Decision {
	var envelope messaging.Envelope
	if err := json.Unmarshal(handle.Body(), &envelope); err != nil {
		p.errors <- fmt.Errorf("decode poison pointer: %w", err)
		return disposition.Decision{Action: disposition.ActionHold}
	}
	if err := messaging.ValidateEnvelope(envelope); err != nil {
		p.errors <- err
		return disposition.Decision{Action: disposition.ActionHold}
	}
	pointer := delivery.Pointer{
		OrganizationID: envelope.OrganizationID, DeliveryID: envelope.DeliveryID,
		OutboxID: envelope.OutboxID, DispatchSeq: envelope.DispatchSeq,
	}
	if pointer != p.pointer {
		p.errors <- fmt.Errorf("consumer acquired pointer %+v, want exact %+v", pointer, p.pointer)
		return disposition.Decision{Action: disposition.ActionHold}
	}
	call := p.calls.Add(1)
	result, err := p.store.AcquirePointer(ctx, pointer, delivery.AcquireOptions{
		LeaseDuration: 2 * time.Second, MaxAttempts: 3, NewToken: delivery.RandomToken,
	})
	if err != nil {
		p.errors <- err
		return disposition.Decision{Action: disposition.ActionHold}
	}
	if result.Status != delivery.PointerAcquired || result.Claim == nil {
		p.errors <- fmt.Errorf("pointer acquisition status %q on attempt %d", result.Status, call)
		return disposition.Decision{Action: disposition.ActionHold}
	}
	if call < poisonDeliveryAttempts {
		released, err := p.store.ReleasePointer(ctx, *result.Claim)
		if err != nil || !released {
			p.errors <- fmt.Errorf("release poison lease: released=%t error=%v", released, err)
			return disposition.Decision{Action: disposition.ActionHold}
		}
	}
	// The final acquisition deliberately retains its short lease. The fixture
	// expires it after the broker has dropped/dead-lettered the poison pointer.
	return disposition.Decision{Action: disposition.ActionRejectRequeue, Reason: "poison delivery limit test"}
}

func newRecoverySweep(t *testing.T, f *fixture) *recovery.Sweep {
	t.Helper()
	sweep, err := recovery.NewSweep(f.pool, recovery.Options{
		Budget: recovery.Budget{
			Total:  2 * time.Second,
			Expire: recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
			Repair: recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
			ReArm:  recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
			Stale:  recovery.PassBudget{MaxCandidates: 20, MaxDuration: 500 * time.Millisecond},
		},
		StalePublishedAfter: time.Millisecond,
		BuildMessage:        recovery.BuildMessage,
		Logger:              f.logger,
	})
	if err != nil {
		t.Fatalf("create Recovery Sweep: %v", err)
	}
	return sweep
}

func queueCounter(info queueInfo, name string) int64 {
	return info.MessageStats[name]
}

func TestEndToEndDLQRecoveryRedispatchIntegration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	sequences := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		sequences <- r.Header.Get("X-HookFlow-Dispatch-Sequence")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	oldOutbox := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
	oldClaim := f.claimOne(t)
	if row := f.readOutboxes(t, d.id); row[0].status != "publishing" {
		t.Fatalf("N was not committed as publishing before Session.Publish: %+v", row[0])
	}
	session := newPublisherSession(t, f)
	result, err := session.Publish(context.Background(), oldClaim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("publish N = %q, %v; want confirmed", result, err)
	}
	applied, err := outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), oldClaim)
	if err != nil || !applied {
		t.Fatalf("mark N published = %t, %v; want applied", applied, err)
	}
	dlqBefore := f.queueInfo(t, messaging.DeadLetterQueue)

	store, err := delivery.NewStore(f.pool, delivery.Options{
		NewID: id.NewV7, BuildRetryMessage: delivery.BuildRetryMessage, Logger: f.logger,
	})
	if err != nil {
		t.Fatalf("create delivery store for poison acquisitions: %v", err)
	}
	pointer := delivery.Pointer{
		OrganizationID: f.orgID, DeliveryID: d.id, OutboxID: oldOutbox, DispatchSeq: 1,
	}
	processor := &leaseRejectProcessor{
		store: store, pointer: pointer, errors: make(chan error, 1),
	}
	cfg := testWorkerConfig(1)
	run := startConsumer(t, f.newConsumer(t, cfg, processor))
	eventually(t, 15*time.Second, "21 exact pointer acquisitions followed by broker loss", func() error {
		select {
		case err := <-processor.errors:
			return err
		default:
		}
		if processor.calls.Load() < poisonDeliveryAttempts {
			return fmt.Errorf("pointer acquisitions=%d, want %d", processor.calls.Load(), poisonDeliveryAttempts)
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)
	if got := processor.calls.Load(); got != poisonDeliveryAttempts {
		t.Fatalf("poison pointer acquisitions = %d, want exactly %d", got, poisonDeliveryAttempts)
	}
	if requests.Load() != 0 || f.attemptCount(t, d.id) != 0 {
		t.Fatalf("poison loop performed HTTP or wrote attempts: requests=%d attempts=%d", requests.Load(), f.attemptCount(t, d.id))
	}
	oldRow := f.readOutboxes(t, d.id)
	if len(oldRow) != 1 || oldRow[0].id != oldOutbox || oldRow[0].status != "published" {
		t.Fatalf("old N did not remain published after broker loss: %+v", oldRow)
	}
	leased := f.readDelivery(t, d.id)
	if leased.leaseToken == "" || !leased.leaseExpiresAt.Valid || leased.attemptCount != 0 {
		t.Fatalf("final real acquisition did not leave an expiring lease without attempts: %+v", leased)
	}

	dlqAfter := f.queueInfo(t, messaging.DeadLetterQueue)
	telemetryIncreased := dlqAfter.Messages > dlqBefore.Messages ||
		queueCounter(dlqAfter, "publish") > queueCounter(dlqBefore, "publish") ||
		queueCounter(dlqAfter, "deliver") > queueCounter(dlqBefore, "deliver")
	workAfter := f.queueInfo(t, messaging.DispatchQueue)
	explicitLoss := workAfter.MessagesReady == 0 && workAfter.MessagesUnacknowledged == 0
	if !telemetryIncreased && !explicitLoss {
		t.Fatal("poison pointer has neither DLQ telemetry nor explicit broker loss")
	}
	t.Logf("poison pointer loss evidence: dlq_telemetry=%t explicit_loss=%t", telemetryIncreased, explicitLoss)

	dead, ok, err := f.inspectCh.Get(messaging.DeadLetterQueue, false)
	if err != nil || !ok {
		t.Fatalf("read real DLQ pointer: ok=%t error=%v", ok, err)
	}
	var deadEnvelope messaging.Envelope
	if err := json.Unmarshal(dead.Body, &deadEnvelope); err != nil || messaging.ValidateEnvelope(deadEnvelope) != nil {
		t.Fatalf("DLQ pointer body is invalid: envelope=%+v error=%v", deadEnvelope, err)
	}
	deadPointer := delivery.Pointer{
		OrganizationID: deadEnvelope.OrganizationID, DeliveryID: deadEnvelope.DeliveryID,
		OutboxID: deadEnvelope.OutboxID, DispatchSeq: deadEnvelope.DispatchSeq,
	}
	if deadPointer != pointer || dead.MessageId != oldOutbox || dead.Exchange != messaging.DeadLetterExchange ||
		dead.RoutingKey != messaging.DeadLetterRoutingKey {
		t.Fatalf("DLQ contained the wrong pointer: pointer=%+v message_id=%s exchange=%s routing_key=%s",
			deadPointer, dead.MessageId, dead.Exchange, dead.RoutingKey)
	}
	if _, exists := dead.Headers["x-death"]; !exists {
		t.Fatal("DLQ pointer lacks RabbitMQ x-death evidence")
	}
	if err := f.inspectCh.Ack(dead.DeliveryTag, false); err != nil {
		t.Fatalf("acknowledge inspected DLQ pointer: %v", err)
	}

	// Recovery is database-driven and must not depend on a DLQ consumer/copy.
	f.expireWorkerLease(t, d.id)
	sweep := newRecoverySweep(t, f)
	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Recovery Sweep after lease expiry: %v", err)
	}
	if report.Stale.Applied != 1 || report.Expire.Applied != 0 || report.Repair.Applied != 0 || report.ReArm.Applied != 0 {
		t.Fatalf("unexpected Recovery report: %+v", report)
	}
	rows := f.readOutboxes(t, d.id)
	if len(rows) != 2 || rows[0].id != oldOutbox || rows[0].status != "published" ||
		rows[1].sequence != 2 || rows[1].status != "pending" || rows[1].attempts != 0 {
		t.Fatalf("Recovery did not create exactly one pending N+1 while preserving N: %+v", rows)
	}
	if deliveryRow := f.readDelivery(t, d.id); deliveryRow.dispatchSeq != 2 || deliveryRow.leaseToken != "" || deliveryRow.attemptCount != 0 {
		t.Fatalf("Recovery did not advance/clear the stale lease: %+v", deliveryRow)
	}
	second, err := sweep.Run(context.Background())
	if err != nil || second.Stale.Applied != 0 || len(f.readOutboxes(t, d.id)) != 2 {
		t.Fatalf("duplicate Recovery sweep: report=%+v error=%v rows=%+v", second, err, f.readOutboxes(t, d.id))
	}

	newClaim := f.claimOne(t)
	if newClaim.Sequence != 2 || newClaim.ClaimCount != 1 {
		t.Fatalf("publisher did not claim N+1: %+v", newClaim)
	}
	result, err = session.Publish(context.Background(), newClaim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("publish N+1 = %q, %v; want confirmed", result, err)
	}
	applied, err = outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), newClaim)
	if err != nil || !applied {
		t.Fatalf("mark N+1 published = %t, %v; want applied", applied, err)
	}

	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	finalRun := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 6*time.Second, "N+1 HTTP completion and ACK", func() error {
		if row := f.readDelivery(t, d.id); row.state != "succeeded" || row.dispatchSeq != 2 || row.attemptCount != 1 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded at N+1"}
		}
		if info := f.queueInfo(t, messaging.DispatchQueue); info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	finalRun.stop(t)
	if requests.Load() != 1 || f.attemptCount(t, d.id) != 1 || f.attemptOutcome(t, d.id) != "success" {
		t.Fatalf("final delivery was not exactly one successful HTTP attempt: requests=%d attempts=%d outcome=%q",
			requests.Load(), f.attemptCount(t, d.id), f.attemptOutcome(t, d.id))
	}
	if sequence := <-sequences; sequence != "2" {
		t.Fatalf("HTTP dispatch sequence = %q, want 2", sequence)
	}
	finalRows := f.readOutboxes(t, d.id)
	if len(finalRows) != 2 || finalRows[0].id != oldOutbox || finalRows[0].status != "published" ||
		finalRows[1].sequence != 2 || finalRows[1].status != "published" {
		t.Fatalf("N was not preserved through N+1 completion: %+v", finalRows)
	}
}

func TestRecoveryLostPointerIndependentIntegration(t *testing.T) {
	f := newFixture(t)
	sweep := newRecoverySweep(t, f)
	now := time.Now()

	lost := f.seedDelivery(t, deliverySeed{nextAttemptAt: now.Add(-time.Minute)})
	lostOutbox := f.seedOutbox(t, lost.id, 1, "published", now.Add(-time.Minute), now.Add(-5*time.Minute))
	active := f.seedDelivery(t, deliverySeed{
		nextAttemptAt: now.Add(-time.Minute), leaseToken: mustID(t), leaseExpiresAt: now.Add(time.Hour),
	})
	activeOutbox := f.seedOutbox(t, active.id, 1, "published", now.Add(-time.Minute), now.Add(-5*time.Minute))
	expired := f.seedDelivery(t, deliverySeed{
		nextAttemptAt: now.Add(-time.Minute), eventReceived: now.Add(-2 * time.Hour), eventExpiresAt: now.Add(-time.Hour),
	})
	expiredOutbox := f.seedOutbox(t, expired.id, 1, "published", now.Add(-time.Minute), now.Add(-5*time.Minute))
	beforeActive := f.readDelivery(t, active.id)

	report, err := sweep.Run(context.Background())
	if err != nil {
		t.Fatalf("Recovery Sweep: %v", err)
	}
	if report.Stale.Applied != 1 || report.Expire.Applied != 1 || report.Repair.Applied != 0 || report.ReArm.Applied != 0 {
		t.Fatalf("unexpected independent Recovery report: %+v", report)
	}
	lostRows := f.readOutboxes(t, lost.id)
	if len(lostRows) != 2 || lostRows[0].id != lostOutbox || lostRows[0].status != "published" ||
		lostRows[1].sequence != 2 || lostRows[1].status != "pending" {
		t.Fatalf("lost pointer did not produce one N+1: %+v", lostRows)
	}
	if row := f.readDelivery(t, lost.id); row.dispatchSeq != 2 || row.state != "pending" || row.leaseToken != "" {
		t.Fatalf("lost pointer delivery was not advanced safely: %+v", row)
	}
	if after := f.readDelivery(t, active.id); after != beforeActive || len(f.readOutboxes(t, active.id)) != 1 ||
		f.readOutboxes(t, active.id)[0].id != activeOutbox || f.readOutboxes(t, active.id)[0].status != "published" {
		t.Fatalf("active lease was redispatched: before=%+v after=%+v outboxes=%+v", beforeActive, after, f.readOutboxes(t, active.id))
	}
	if row := f.readDelivery(t, expired.id); row.state != "expired" || row.dispatchSeq != 1 || row.leaseToken != "" {
		t.Fatalf("event expiry did not take precedence: %+v", row)
	}
	expiredRows := f.readOutboxes(t, expired.id)
	if len(expiredRows) != 1 || expiredRows[0].id != expiredOutbox || expiredRows[0].status != "published" {
		t.Fatalf("event expiry created a redispatch: %+v", expiredRows)
	}

	second, err := sweep.Run(context.Background())
	if err != nil || second.Stale.Applied != 0 || second.Expire.Applied != 0 {
		t.Fatalf("second Recovery sweep was not idempotent: report=%+v error=%v", second, err)
	}
	if rows := f.readOutboxes(t, lost.id); len(rows) != 2 {
		t.Fatalf("second Recovery sweep duplicated N+1: %+v", rows)
	}
}
