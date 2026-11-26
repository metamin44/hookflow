//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

type capturedHandle struct {
	handle  *worker.Handle
	tag     uint64
	call    int64
	release chan struct{}
}

type captureProcessor struct {
	calls chan capturedHandle
	count atomic.Int64
}

func (p *captureProcessor) Process(ctx context.Context, handle *worker.Handle) disposition.Decision {
	call := p.count.Add(1)
	release := make(chan struct{})
	p.calls <- capturedHandle{handle: handle, tag: handleDeliveryTag(handle), call: call, release: release}
	select {
	case <-release:
	case <-ctx.Done():
	}
	return disposition.Decision{Action: disposition.ActionHold, Reason: "test-controlled hold"}
}

// Handle intentionally hides its numeric tag. Reflection is read-only and lets
// this test prove the actual AMQP tag was reused without adding a production API.
func handleDeliveryTag(handle *worker.Handle) uint64 {
	value := reflect.ValueOf(handle)
	if value.Kind() != reflect.Pointer || value.IsNil() {
		return 0
	}
	return value.Elem().FieldByName("tag").Uint()
}

func TestWorkerDeliveryTagGenerationSafetyIntegration(t *testing.T) {
	f := newFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, d.id, outboxID, 1)
	before := f.readDelivery(t, d.id)

	processor := &captureProcessor{calls: make(chan capturedHandle, 2)}
	cfg := testWorkerConfig(1)
	// Use the approved 30s Worker window: management sampling can take 12s.
	// A 2.9s test work context would correctly abort the connection before its
	// identity/counters were sampled, rather than exercising stale-tag safety.
	cfg.LeaseDuration = 30 * time.Second
	consumer := f.newConsumer(t, cfg, processor)
	run := startConsumer(t, consumer)

	var first capturedHandle
	select {
	case first = <-processor.calls:
	case <-time.After(3 * time.Second):
		t.Fatal("generation A did not receive the pointer")
	}
	t.Cleanup(func() {
		run.cancel()
		select {
		case <-first.release:
		default:
			close(first.release)
		}
	})
	connectionName := f.waitOwnConnection(t, f.consumerConnectionName)
	f.closeConnection(t, connectionName)
	close(first.release)
	t.Logf("released generation A call=%d tag=%d", first.call, first.tag)

	var second capturedHandle
	select {
	case second = <-processor.calls:
	case <-time.After(5 * time.Second):
		t.Fatal("generation B did not reconnect and receive the requeued pointer")
	}
	t.Cleanup(func() {
		run.cancel()
		select {
		case <-second.release:
		default:
			close(second.release)
		}
	})
	t.Logf("generation B call=%d tag=%d reconnect=%d", second.call, second.tag, f.metricValue(t, "hookflow_worker_reconnect_total"))
	if first.tag == 0 || first.tag != second.tag {
		t.Fatalf("numeric delivery tag was not reused: A=%d B=%d", first.tag, second.tag)
	}
	if err := first.handle.Ack(); !errors.Is(err, worker.ErrStaleHandle) {
		t.Fatalf("old Handle.Ack() error = %v, want ErrStaleHandle", err)
	}
	if err := first.handle.RejectRequeue(); !errors.Is(err, worker.ErrStaleHandle) {
		t.Fatalf("old Handle.RejectRequeue() error = %v, want ErrStaleHandle", err)
	}

	// The stale handle must not settle B merely because both tags are 1. RabbitMQ
	// management queue counters are sampled, so allow two collection intervals.
	eventually(t, 12*time.Second, "generation B delivery to remain unacknowledged", func() error {
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 1 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.cancel()
	close(second.release)
	run.stop(t)

	eventually(t, 15*time.Second, "generation B delivery to requeue on shutdown", func() error {
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 1 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	message, ok, err := f.inspectCh.Get(messaging.DispatchQueue, false)
	if err != nil || !ok {
		t.Fatalf("get requeued generation B pointer: ok=%t error=%v", ok, err)
	}
	headerNames := make([]string, 0, len(message.Headers))
	for name := range message.Headers {
		headerNames = append(headerNames, name)
	}
	t.Logf("requeued generation B headers=%v", headerNames)
	envelope, err := messaging.ParseDelivery(message)
	if err != nil {
		t.Fatalf("parse requeued pointer: %v", err)
	}
	if !message.Redelivered || envelope.DeliveryID != d.id || envelope.OutboxID != outboxID {
		t.Fatalf("unexpected requeued delivery: redelivered=%t envelope=%+v", message.Redelivered, envelope)
	}
	if err := f.inspectCh.Ack(message.DeliveryTag, false); err != nil {
		t.Fatalf("acknowledge inspected requeued pointer: %v", err)
	}
	if after := f.readDelivery(t, d.id); after != before || f.attemptCount(t, d.id) != 0 {
		t.Fatalf("delivery-tag safety test mutated durable work: before=%+v after=%+v attempts=%d", before, after, f.attemptCount(t, d.id))
	}
	if got := f.metricValue(t, "hookflow_worker_reconnect_total"); got < 1 {
		t.Fatalf("worker reconnect metric = %d, want application-managed reconnect", got)
	}
}

type selectiveProcessor struct {
	delegate worker.Processor
	holdID   string
	held     chan struct{}
	once     sync.Once
}

func (p *selectiveProcessor) Process(ctx context.Context, handle *worker.Handle) disposition.Decision {
	var envelope messaging.Envelope
	if err := json.Unmarshal(handle.Body(), &envelope); err == nil && envelope.DeliveryID == p.holdID {
		p.once.Do(func() { close(p.held) })
		return disposition.Decision{Action: disposition.ActionHold, Reason: "prefetched work remains unsettled during shutdown"}
	}
	return p.delegate.Process(ctx, handle)
}

func TestWorkerGracefulShutdownRequeuesWithoutNACKIntegration(t *testing.T) {
	f := newFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	var startedOnce sync.Once
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(release) }) }
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		startedOnce.Do(func() { close(started) })
		<-release
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	t.Cleanup(closeRelease)

	active := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	remaining := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	activeOutbox := f.seedOutbox(t, active.id, 1, "published", time.Time{}, time.Time{})
	remainingOutbox := f.seedOutbox(t, remaining.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, active.id, activeOutbox, 1)
	f.publishPointer(t, remaining.id, remainingOutbox, 1)
	beforeRemaining := f.readDelivery(t, remaining.id)

	cfg := testWorkerConfig(2)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	processor := &selectiveProcessor{delegate: runtime, holdID: remaining.id, held: make(chan struct{})}
	run := startConsumer(t, f.newConsumer(t, cfg, processor))
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("active HTTP work did not start")
	}
	select {
	case <-processor.held:
	case <-time.After(3 * time.Second):
		t.Fatal("remaining prefetched work was not delivered")
	}

	run.cancel()
	select {
	case <-run.done:
		t.Fatal("consumer exited before active HTTP work could commit")
	default:
	}
	closeRelease()
	run.stop(t)

	eventually(t, 5*time.Second, "committed active work to ACK and held work to requeue", func() error {
		if row := f.readDelivery(t, active.id); row.state != "succeeded" || row.attemptCount != 1 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded"}
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 1 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	if requests.Load() != 1 || f.attemptCount(t, active.id) != 1 {
		t.Fatalf("active HTTP work was not committed exactly once: requests=%d attempts=%d", requests.Load(), f.attemptCount(t, active.id))
	}
	if after := f.readDelivery(t, remaining.id); after != beforeRemaining || f.attemptCount(t, remaining.id) != 0 {
		t.Fatalf("held prefetched work was mutated or attempted: before=%+v after=%+v attempts=%d", beforeRemaining, after, f.attemptCount(t, remaining.id))
	}
	if got := f.metricValue(t, "hookflow_worker_rejected_total"); got != 0 {
		t.Fatalf("worker issued %d negative settlements during graceful shutdown, want 0", got)
	}

	message, ok, err := f.inspectCh.Get(messaging.DispatchQueue, false)
	if err != nil || !ok {
		t.Fatalf("get requeued prefetched work: ok=%t error=%v", ok, err)
	}
	envelope, err := messaging.ParseDelivery(message)
	if err != nil {
		t.Fatalf("parse requeued prefetched work: %v", err)
	}
	if !message.Redelivered || envelope.DeliveryID != remaining.id || envelope.OutboxID != remainingOutbox {
		t.Fatalf("wrong message requeued during shutdown: redelivered=%t envelope=%+v", message.Redelivered, envelope)
	}
	if err := f.inspectCh.Ack(message.DeliveryTag, false); err != nil {
		t.Fatalf("acknowledge inspected shutdown message: %v", err)
	}
}
