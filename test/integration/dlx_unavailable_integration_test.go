//go:build integration

package integration_test // Bundle 1 DLX failure integration coverage.

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

type rejectOnlyProcessor struct {
	calls   atomic.Int64
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (p *rejectOnlyProcessor) Process(context.Context, *worker.Handle) disposition.Decision {
	p.once.Do(func() { close(p.started) })
	<-p.release
	p.calls.Add(1)
	return disposition.Decision{Action: disposition.ActionRejectRequeue, Reason: "DLX failure injection"}
}

func TestDLXUnavailableDropsPointerAndRecoveryStillCreatesNPlus1Integration(t *testing.T) {
	f := newFixture(t)
	d := f.seedDelivery(t, deliverySeed{})
	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Now().Add(-5*time.Minute))
	f.publishPointer(t, d.id, outboxID, 1)
	dlqBefore := f.queueInfo(t, messaging.DeadLetterQueue)

	processor := &rejectOnlyProcessor{started: make(chan struct{}), release: make(chan struct{})}
	var releaseOnce sync.Once
	closeRelease := func() { releaseOnce.Do(func() { close(processor.release) }) }
	t.Cleanup(closeRelease)
	cfg := testWorkerConfig(1)
	run := startConsumer(t, f.newConsumer(t, cfg, processor))
	select {
	case <-processor.started:
	case <-time.After(3 * time.Second):
		t.Fatal("reject-only consumer did not start")
	}

	channel, err := f.conn.Channel()
	if err != nil {
		t.Fatal(err)
	}
	if err := channel.QueueUnbind(messaging.DeadLetterQueue, messaging.DeadLetterRoutingKey, messaging.DeadLetterExchange, nil); err != nil {
		_ = channel.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := channel.QueueBind(messaging.DeadLetterQueue, messaging.DeadLetterRoutingKey, messaging.DeadLetterExchange, false, nil); err != nil {
			t.Errorf("restore DLQ binding: %v", err)
		}
		_ = channel.Close()
	})
	// The first reject is held until the missing DLX route is established.
	closeRelease()

	eventually(t, 15*time.Second, "repeated rejects to exhaust the delivery limit without a DLX route", func() error {
		if processor.calls.Load() < poisonDeliveryAttempts {
			return &processedCountError{got: processor.calls.Load()}
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)
	if got := processor.calls.Load(); got != poisonDeliveryAttempts {
		t.Fatalf("reject-only calls = %d, want %d", got, poisonDeliveryAttempts)
	}
	if info := f.queueInfo(t, messaging.DeadLetterQueue); info.Messages != dlqBefore.Messages {
		t.Fatalf("unroutable at-most-once dead lettering changed DLQ depth: before=%d after=%d", dlqBefore.Messages, info.Messages)
	}

	report, err := newRecoverySweep(t, f).Run(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if report.Stale.Applied != 1 {
		t.Fatalf("Recovery depended on a DLQ copy: %+v", report)
	}
	rows := f.readOutboxes(t, d.id)
	if len(rows) != 2 || rows[0].id != outboxID || rows[0].status != "published" ||
		rows[1].sequence != 2 || rows[1].status != "pending" {
		t.Fatalf("DLX failure did not produce exactly one database-owned N+1: %+v", rows)
	}
}
