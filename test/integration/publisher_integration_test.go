//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/app/publisher"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

func newPublisherSession(t *testing.T, f *fixture) *publisher.Session {
	t.Helper()
	return newPublisherSessionWithTimeout(t, f, 500*time.Millisecond)
}

func newPublisherSessionWithTimeout(t *testing.T, f *fixture, confirmTimeout time.Duration) *publisher.Session {
	t.Helper()
	f.rabbit.ConnectionName = "hookflow-integration-publisher-" + mustID(t)
	session, err := publisher.NewSession(f.rabbit, confirmTimeout, f.logger, f.metrics)
	if err != nil {
		t.Fatalf("create publisher session: %v", err)
	}
	t.Cleanup(session.Close)
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	if err := session.Ensure(ctx); err != nil {
		t.Fatalf("ensure publisher session: %v", err)
	}
	return session
}

// dispatchQueueObservation pairs a live queue.declare-ok snapshot with the
// management API's sampled counters. Only the live values are authoritative:
// management counters are collected asynchronously and can stay stale for up to
// one collection interval, which is longer than this assertion's bounded window.
type dispatchQueueObservation struct {
	ready            int
	consumers        int
	sampledReady     int
	sampledUnacked   int
	sampledAvailable bool
}

func (o dispatchQueueObservation) empty() bool {
	return o.ready == 0 && o.consumers == 0
}

type dispatchQueueStateError struct {
	observation dispatchQueueObservation
}

func (e *dispatchQueueStateError) Error() string {
	sampled := "sampled counters unavailable"
	if e.observation.sampledAvailable {
		sampled = fmt.Sprintf("sampled ready=%d unacked=%d (may lag)",
			e.observation.sampledReady, e.observation.sampledUnacked)
	}
	return fmt.Sprintf("dispatch queue still contains work: live ready=%d consumers=%d; %s",
		e.observation.ready, e.observation.consumers, sampled)
}

func observeDispatchQueue(f *fixture) (dispatchQueueObservation, error) {
	queue, err := f.inspectCh.QueueDeclarePassive(messaging.DispatchQueue, false, false, false, false, nil)
	if err != nil {
		return dispatchQueueObservation{}, fmt.Errorf("observe dispatch queue state: %w", err)
	}
	return dispatchQueueObservation{ready: queue.Messages, consumers: queue.Consumers}, nil
}

// sampleManagement only enriches a failure diagnostic. Sampled management
// values must never decide whether the queue is drained.
func (o *dispatchQueueObservation) sampleManagement(ctx context.Context, management *managementClient) {
	info, err := management.queue(ctx, messaging.DispatchQueue)
	if err != nil {
		return
	}
	o.sampledReady = info.MessagesReady
	o.sampledUnacked = info.MessagesUnacknowledged
	o.sampledAvailable = true
}

func requireDispatchQueueEmpty(t *testing.T, f *fixture) {
	t.Helper()
	eventually(t, 4*time.Second, "dispatch queue to become empty", func() error {
		ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
		defer cancel()
		observation, err := observeDispatchQueue(f)
		if err != nil {
			return err
		}
		if !observation.empty() {
			observation.sampleManagement(ctx, f.management)
			return &dispatchQueueStateError{observation: observation}
		}
		return nil
	})
}

func TestDispatchQueueDrainIgnoresStaleManagementCountersIntegration(t *testing.T) {
	f := newFixture(t)
	stale := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"messages":1,"messages_ready":1,"messages_unacknowledged":1,"consumers":0}`))
	}))
	t.Cleanup(stale.Close)
	original := f.management
	f.management = &managementClient{baseURL: stale.URL, vhost: "/", client: stale.Client()}
	t.Cleanup(func() { f.management = original })

	// Prove the sampled predicate this helper previously used would reject the
	// empty queue before asserting that the live observation accepts it.
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	info, err := f.management.queue(ctx, messaging.DispatchQueue)
	if err != nil || info.MessagesReady == 0 || info.MessagesUnacknowledged == 0 {
		t.Fatalf("stale management fixture = %+v, %v; want nonzero sampled counts", info, err)
	}
	requireDispatchQueueEmpty(t, f)
}

func TestDispatchQueueObservationRejectsLiveWork(t *testing.T) {
	tests := []struct {
		name        string
		observation dispatchQueueObservation
		wantEmpty   bool
	}{
		{name: "empty live queue", observation: dispatchQueueObservation{}, wantEmpty: true},
		{name: "live ready message", observation: dispatchQueueObservation{ready: 1}, wantEmpty: false},
		{name: "live consumer", observation: dispatchQueueObservation{consumers: 1}, wantEmpty: false},
		{name: "sampled counters only", observation: dispatchQueueObservation{sampledReady: 1, sampledUnacked: 1, sampledAvailable: true}, wantEmpty: true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.observation.empty(); got != tc.wantEmpty {
				t.Fatalf("observation.empty() = %t, want %t", got, tc.wantEmpty)
			}
		})
	}
}

func consumeAndAckPointer(t *testing.T, f *fixture) (messaging.Envelope, amqp.Delivery) {
	t.Helper()
	message, ok, err := f.inspectCh.Get(messaging.DispatchQueue, false)
	if err != nil || !ok {
		t.Fatalf("get published pointer: ok=%t error=%v", ok, err)
	}
	envelope, err := messaging.ParseDelivery(message)
	if err != nil {
		t.Fatalf("parse published pointer: %v", err)
	}
	if err := f.inspectCh.Ack(message.DeliveryTag, false); err != nil {
		t.Fatalf("acknowledge inspected pointer: %v", err)
	}
	return envelope, message
}

func TestPublisherSessionCommitBeforePublishIntegration(t *testing.T) {
	f := newFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	outboxID := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
	claim := f.claimOne(t)

	// Claim has returned, so T1 must already be visible from another transaction.
	before := f.readOutboxes(t, d.id)
	if len(before) != 1 || before[0].id != outboxID || before[0].status != "publishing" || before[0].attempts != 1 {
		t.Fatalf("T1 was not committed before publish: %+v", before)
	}

	session := newPublisherSession(t, f)
	result, err := session.Publish(context.Background(), claim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("Session.Publish() = %q, %v; want confirmed", result, err)
	}
	// A broker confirm is not T2. Only the fenced store may publish the row.
	afterConfirm := f.readOutboxes(t, d.id)
	if afterConfirm[0].status != "publishing" || afterConfirm[0].publishedAt.Valid {
		t.Fatalf("Session.Publish mutated T2 state: %+v", afterConfirm[0])
	}

	stale := claim
	stale.LeaseToken = mustID(t)
	applied, err := outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), stale)
	if err != nil || applied {
		t.Fatalf("stale MarkPublished() = %t, %v; want fenced", applied, err)
	}
	if after := f.readOutboxes(t, d.id); after[0] != afterConfirm[0] {
		t.Fatalf("stale T2 mutated the claim: before=%+v after=%+v", afterConfirm[0], after[0])
	}
	applied, err = outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), claim)
	if err != nil || !applied {
		t.Fatalf("matching MarkPublished() = %t, %v; want applied", applied, err)
	}

	envelope, message := consumeAndAckPointer(t, f)
	want := messaging.Envelope{
		SchemaVersion: messaging.SchemaVersion, OrganizationID: f.orgID,
		DeliveryID: d.id, DispatchSeq: 1, OutboxID: outboxID,
	}
	if envelope != want {
		t.Fatalf("published envelope = %+v, want %+v", envelope, want)
	}
	if message.DeliveryMode != amqp.Persistent || message.MessageId != outboxID {
		t.Fatalf("published message lost persistence or identity: %+v", message)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(message.Body, &fields); err != nil {
		t.Fatalf("decode identifiers-only body: %v", err)
	}
	if len(fields) != 5 {
		t.Fatalf("broker envelope has %d fields, want exactly five identifiers", len(fields))
	}
	for _, forbidden := range []string{"payload", "secret", "url", "headers"} {
		if _, exists := fields[forbidden]; exists {
			t.Fatalf("broker envelope contained forbidden field %q", forbidden)
		}
	}
	published := f.readOutboxes(t, d.id)
	if published[0].status != "published" || !published[0].publishedAt.Valid {
		t.Fatalf("fenced T2 did not publish row: %+v", published[0])
	}
	requireDispatchQueueEmpty(t, f)
}

func TestPublisherDuplicatePublishCompletesExactlyOnceIntegration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	outboxID := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
	claim := f.claimOne(t)
	session := newPublisherSession(t, f)
	for i := 0; i < 2; i++ {
		result, err := session.Publish(context.Background(), claim)
		if err != nil || result != publisher.ResultConfirmed {
			t.Fatalf("duplicate publication %d = %q, %v", i+1, result, err)
		}
	}
	queue, err := f.inspectCh.QueueDeclarePassive(messaging.DispatchQueue, false, false, false, false, nil)
	if err != nil || queue.Messages != 2 {
		t.Fatalf("actual duplicate pointer count = %d, error=%v; want 2", queue.Messages, err)
	}
	store := outbox.NewStore(f.pool, f.logger)
	applied, err := store.MarkPublished(context.Background(), claim)
	if err != nil || !applied {
		t.Fatalf("first T2 = %t, %v; want applied", applied, err)
	}
	before := f.readOutboxes(t, d.id)
	applied, err = store.MarkPublished(context.Background(), claim)
	if err != nil || applied {
		t.Fatalf("duplicate T2 = %t, %v; want fenced", applied, err)
	}
	if after := f.readOutboxes(t, d.id); len(after) != 1 || after[0] != before[0] || after[0].id != outboxID {
		t.Fatalf("duplicate T2 mutated durable outbox: before=%+v after=%+v", before, after)
	}
	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 12*time.Second, "both duplicate pointers to ACK after one HTTP completion", func() error {
		row := f.readDelivery(t, d.id)
		if row.state != "succeeded" || row.attemptCount != 1 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded"}
		}
		if got := f.metricValue(t, "hookflow_worker_acked_total"); got != 2 {
			return &processedCountError{got: got}
		}
		return nil
	})
	run.stop(t)
	if requests.Load() != 1 || f.attemptCount(t, d.id) != 1 || len(f.readOutboxes(t, d.id)) != 1 {
		t.Fatalf("duplicate publish corrupted delivery: HTTP=%d attempts=%d", requests.Load(), f.attemptCount(t, d.id))
	}
	requireDispatchQueueEmpty(t, f)
}

func TestWorkerRetainsPointerDeliveredBeforePublisherT2Integration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	outboxID := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
	claim := f.claimOne(t)
	session := newPublisherSession(t, f)
	result, err := session.Publish(context.Background(), claim)
	if err != nil || result != publisher.ResultConfirmed {
		t.Fatalf("publication before T2 = %q, %v", result, err)
	}
	before := f.readDelivery(t, d.id)
	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 8*time.Second, "Worker to retain the exact publishing pointer without ACK", func() error {
		if f.metricValue(t, "hookflow_worker_publication_pending_total") == 0 {
			return errors.New("publication-in-progress pointer has not been checked")
		}
		return nil
	})
	if requests.Load() != 0 || f.metricValue(t, "hookflow_worker_acked_total") != 0 ||
		f.metricValue(t, "hookflow_worker_rejected_total") != 0 || f.attemptCount(t, d.id) != 0 || f.readDelivery(t, d.id) != before {
		t.Fatal("Worker settled or mutated the live pointer before T2 committed")
	}
	applied, err := outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), claim)
	if err != nil || !applied {
		t.Fatalf("matching Publisher T2 = %t, %v", applied, err)
	}
	eventually(t, 12*time.Second, "the same N pointer to complete after Publisher T2", func() error {
		row := f.readDelivery(t, d.id)
		if row.state != "succeeded" || row.dispatchSeq != 1 || row.attemptCount != 1 ||
			f.metricValue(t, "hookflow_worker_acked_total") != 1 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded"}
		}
		return nil
	})
	run.stop(t)
	if requests.Load() != 1 || f.attemptCount(t, d.id) != 1 || len(f.readOutboxes(t, d.id)) != 1 || claim.MessageID != outboxID {
		t.Fatal("pre-T2 redelivery required a new generation or duplicate HTTP completion")
	}
	requireDispatchQueueEmpty(t, f)
}

func TestPublisherSessionUnknownOutcomeIntegration(t *testing.T) {
	for _, mode := range []string{"context cancellation", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			f := newFixture(t)
			d := f.seedDelivery(t, deliverySeed{})
			outboxID := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
			claim := f.claimOne(t)
			var session *publisher.Session
			if mode == "timeout" {
				session = newPublisherSessionWithTimeout(t, f, time.Nanosecond)
			} else {
				session = newPublisherSession(t, f)
			}

			var result publisher.PublishResult
			var err error
			if mode == "context cancellation" {
				ctx, cancel := context.WithCancel(context.Background())
				cancel()
				result, err = session.Publish(ctx, claim)
			} else {
				result, err = session.Publish(context.Background(), claim)
			}
			if !errors.Is(err, publisher.ErrUnknownOutcome) || result != publisher.ResultUnknown {
				t.Fatalf("Publish() = %q, %v; want unknown", result, err)
			}
			unknownRow := f.readOutboxes(t, d.id)
			if unknownRow[0].status != "publishing" || unknownRow[0].publishedAt.Valid {
				t.Fatalf("unknown outcome committed T2: %+v", unknownRow[0])
			}
			f.drainOwnMessages(t)

			f.agePublisherLease(t, outboxID)
			reaped, err := outbox.NewStore(f.pool, f.logger).ReapExpired(context.Background(), outbox.ReapOptions{
				BatchSize: 1, MaxClaims: 3, LeaseDuration: time.Millisecond,
				Backoff: func(int) (time.Duration, error) { return time.Nanosecond, nil },
			})
			if err != nil || len(reaped.Reaped) != 1 {
				t.Fatalf("ReapExpired() = %+v, %v; want one reaped lease", reaped, err)
			}
			gateCtx, gateCancel := context.WithTimeout(context.Background(), queryTimeout)
			_, gateErr := f.pool.Exec(gateCtx,
				`UPDATE outbox_messages SET available_at = clock_timestamp() + interval '1 hour'
				  WHERE id = $1::uuid`, outboxID)
			gateCancel()
			if gateErr != nil {
				t.Fatalf("defer old unknown row: %v", gateErr)
			}
			nextDelivery := f.seedDelivery(t, deliverySeed{})
			nextOutbox := f.seedOutbox(t, nextDelivery.id, 1, "pending", time.Time{}, time.Time{})
			replacement := f.claimOne(t)
			if replacement.MessageID != nextOutbox || replacement.MessageID == outboxID ||
				replacement.LeaseToken == claim.LeaseToken || replacement.ClaimCount != 1 {
				t.Fatalf("replacement claim = %+v, original=%+v, want distinct first claim", replacement, claim)
			}
			// Close discards any unresolved channel-generation mapping; Ensure on
			// the same Session must create a clean generation for a different row.
			session.Close()
			ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
			defer cancel()
			if mode == "timeout" {
				session = newPublisherSession(t, f)
			} else if err := session.Ensure(ctx); err != nil {
				t.Fatalf("reuse Session after unknown outcome: %v", err)
			}
			result, err = session.Publish(ctx, replacement)
			if err != nil || result != publisher.ResultConfirmed {
				t.Fatalf("replacement Publish() = %q, %v; want confirmed", result, err)
			}
			applied, err := outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), claim)
			if err != nil || applied {
				t.Fatalf("old generation MarkPublished() = %t, %v; want fenced", applied, err)
			}
			applied, err = outbox.NewStore(f.pool, f.logger).MarkPublished(context.Background(), replacement)
			if err != nil || !applied {
				t.Fatalf("replacement MarkPublished() = %t, %v; want applied", applied, err)
			}
			envelope, _ := consumeAndAckPointer(t, f)
			if envelope.OutboxID != nextOutbox || envelope.DeliveryID != nextDelivery.id || envelope.DispatchSeq != 1 {
				t.Fatalf("fresh generation inherited an old mapping: %+v", envelope)
			}
			if old := f.readOutboxes(t, d.id); len(old) != 1 || old[0].status != "pending" || old[0].publishedAt.Valid {
				t.Fatalf("old unknown generation was revived: %+v", old)
			}
			requireDispatchQueueEmpty(t, f)
		})
	}
}

func TestPublisherMandatoryReturnWinsConfirmFirstIntegration(t *testing.T) {
	f := newFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	outboxID := f.seedOutbox(t, d.id, 1, "pending", time.Time{}, time.Time{})
	claim := f.claimOne(t)

	// Session deliberately owns the production routing contract. Use a dedicated
	// confirm channel and a missing key to exercise real mandatory-return wire
	// behavior without removing or changing any HookFlow binding.
	channel, err := f.conn.Channel()
	if err != nil {
		t.Fatalf("open mandatory-return test channel: %v", err)
	}
	t.Cleanup(func() { _ = channel.Close() })
	if err := channel.Confirm(false); err != nil {
		t.Fatalf("enable mandatory-return confirms: %v", err)
	}
	returns := make(chan amqp.Return, 1)
	confirmations := make(chan amqp.Confirmation, 1)
	channel.NotifyReturn(returns)
	channel.NotifyPublish(confirmations)
	publishing, err := messaging.EncodePublishing(messaging.Envelope{
		SchemaVersion: messaging.SchemaVersion, OrganizationID: f.orgID,
		DeliveryID: d.id, DispatchSeq: claim.Sequence, OutboxID: claim.MessageID,
	})
	if err != nil {
		t.Fatalf("encode publishing: %v", err)
	}
	missingKey := "hookflow.missing." + mustID(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	deferred, err := channel.PublishWithDeferredConfirmWithContext(
		ctx, messaging.DispatchExchange, missingKey, true, false, publishing,
	)
	if err != nil || deferred == nil {
		t.Fatalf("publish to missing key: deferred=%v error=%v", deferred, err)
	}

	// Intentionally observe the positive confirm first. The decision must remain
	// pending until the mandatory Return is observed; confirm-first is not success.
	eventually(t, 3*time.Second, "broker confirmation for unroutable publish", func() error {
		if len(confirmations) == 0 {
			return errors.New("confirmation not queued")
		}
		return nil
	})
	confirmation := <-confirmations
	if !confirmation.Ack || confirmation.DeliveryTag != deferred.DeliveryTag {
		t.Fatalf("unexpected confirmation: %+v", confirmation)
	}
	var returned amqp.Return
	select {
	case returned = <-returns:
	case <-ctx.Done():
		t.Fatal("mandatory Return did not arrive after the positive confirm")
	}
	if returned.MessageId != outboxID || returned.Exchange != messaging.DispatchExchange || returned.RoutingKey != missingKey {
		t.Fatalf("unexpected mandatory Return: %+v", returned)
	}
	if returned.ReplyCode != 312 {
		t.Fatalf("mandatory Return reply code = %d, want 312 NO_ROUTE", returned.ReplyCode)
	}
	if row := f.readOutboxes(t, d.id); row[0].status != "publishing" || row[0].publishedAt.Valid {
		t.Fatalf("confirm-first unroutable publish was marked successful: %+v", row[0])
	}
	requireDispatchQueueEmpty(t, f)
}
