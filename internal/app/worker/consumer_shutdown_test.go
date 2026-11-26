package worker

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

type blockingProcessor struct {
	started chan struct{}
	release chan struct{}
	done    chan struct{}
}

func (p *blockingProcessor) Process(context.Context, *Handle) disposition.Decision {
	close(p.started)
	<-p.release
	close(p.done)
	return disposition.Decision{Action: disposition.ActionHold}
}

type ackProcessor struct{}

func (ackProcessor) Process(context.Context, *Handle) disposition.Decision {
	return disposition.Decision{Action: disposition.ActionAck}
}

type observingProcessor struct {
	delegate Processor
	done     chan struct{}
}

func (p *observingProcessor) Process(ctx context.Context, handle *Handle) disposition.Decision {
	defer close(p.done)
	return p.delegate.Process(ctx, handle)
}

type blockingUnwrapProvider struct {
	base    kek.Provider
	started chan struct{}
	release chan struct{}
}

func (p *blockingUnwrapProvider) Wrap(ctx context.Context, dek []byte, keyContext kek.Context) (kek.Wrapped, error) {
	return p.base.Wrap(ctx, dek, keyContext)
}

func (p *blockingUnwrapProvider) Unwrap(context.Context, kek.Wrapped, kek.Context) ([]byte, error) {
	close(p.started)
	<-p.release
	return nil, errors.New("controlled permanent provider failure")
}

func (p *blockingUnwrapProvider) CurrentKID() string { return p.base.CurrentKID() }

type lateSettlementStore struct {
	*fakeStore
	closed *atomic.Bool
	late   atomic.Int64
}

func (s *lateSettlementStore) RecordPreHTTPFailure(ctx context.Context, claim delivery.Claim, options delivery.PreHTTPFailureOptions) (delivery.PreHTTPFailureResult, error) {
	if s.closed.Load() {
		s.late.Add(1)
	}
	return s.fakeStore.RecordPreHTTPFailure(ctx, claim, options)
}

type controlledSettlementStore struct {
	*fakeStore
	started chan struct{}
	release chan struct{}
	calls   atomic.Int64
}

func (s *controlledSettlementStore) RecordPreHTTPFailure(ctx context.Context, claim delivery.Claim, options delivery.PreHTTPFailureOptions) (delivery.PreHTTPFailureResult, error) {
	s.calls.Add(1)
	close(s.started)
	<-s.release
	return s.fakeStore.RecordPreHTTPFailure(ctx, claim, options)
}

func settlementTestContext(t *testing.T) (context.Context, *processorWork) {
	t.Helper()
	workCtx, cancelWork := context.WithCancel(context.Background())
	forceCtx, forceCancel := context.WithCancel(context.Background())
	work := &processorWork{cancel: cancelWork, forceCancel: forceCancel}
	ctx := context.WithValue(workCtx, settlementForceContextKey{}, forceCtx)
	ctx = context.WithValue(ctx, settlementWorkKey{}, work)
	t.Cleanup(func() {
		cancelWork()
		forceCancel()
	})
	return ctx, work
}

func TestRunGenerationDrainsInFlightBeforeReturn(t *testing.T) {
	processor := &blockingProcessor{
		started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}),
	}
	consumer := &Consumer{
		prefetch: 1, workBudget: 500 * time.Millisecond, processor: processor,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: observability.New(),
	}
	deliveries := make(chan amqp.Delivery, 1)
	deliveries <- amqp.Delivery{Body: []byte(`{}`)}
	close(deliveries)
	returned := make(chan bool, 1)
	go func() { returned <- consumer.runGeneration(context.Background(), &channelGeneration{}, deliveries) }()

	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}
	select {
	case <-returned:
		t.Fatal("generation returned before in-flight work drained")
	case <-time.After(20 * time.Millisecond):
	}
	close(processor.release)
	select {
	case graceful := <-returned:
		if graceful {
			t.Fatal("channel loss was reported as graceful shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("generation did not return after in-flight work drained")
	}
	if !consumer.Quiescent() {
		t.Fatal("consumer remained non-quiescent after normal drain")
	}
}

func TestRunGenerationForcedShutdownPreventsLatePreHTTPSettlement(t *testing.T) {
	base := testMemoryProvider(t)
	preparation := encryptedPreparation(t, base, testWebhookSecret, secrets.ProviderLocalFile)
	started := make(chan struct{})
	release := make(chan struct{})
	provider := &blockingUnwrapProvider{base: base, started: started, release: release}
	store := acquiredStore(preparation)
	var poolClosed atomic.Bool
	guardedStore := &lateSettlementStore{fakeStore: store, closed: &poolClosed}
	runtime, metrics := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), provider, guardedStore, &fakeExecutor{})
	processor := &observingProcessor{delegate: runtime, done: make(chan struct{})}
	consumer := &Consumer{
		prefetch: 1, workBudget: 20 * time.Millisecond, processor: processor,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: metrics,
	}
	deliveries := make(chan amqp.Delivery, 1)
	deliveries <- testHandle(t).delivery
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan bool, 1)
	go func() { returned <- consumer.runGeneration(ctx, &channelGeneration{}, deliveries) }()

	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("secret resolution did not reach the controlled boundary")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("forced shutdown exceeded its bounded drain")
	}

	// The bounded drain returned while the processor is still alive, so the
	// consumer must remain non-quiescent and lifecycle must leave PostgreSQL open.
	if consumer.Quiescent() {
		t.Fatal("consumer reported quiescence while the processor was still blocked")
	}
	// Consumer.Run has returned incomplete, so cmd/worker leaves PostgreSQL open.
	// Simulate external process teardown here to prove force-stop independently
	// prevents a late pre-HTTP settlement against a closed pool.
	poolClosed.Store(true)
	close(release)
	select {
	case <-processor.done:
	case <-time.After(time.Second):
		t.Fatal("processor did not observe force-stop after release")
	}
	if got := guardedStore.late.Load(); got != 0 {
		t.Fatalf("late pre-HTTP settlements after pool close = %d, want 0", got)
	}
	if store.preHTTPCalls != 0 {
		t.Fatalf("pre-HTTP settlement calls = %d, want 0", store.preHTTPCalls)
	}
	if got := metricValue(t, metrics, "hookflow_worker_acked_total"); got != 0 {
		t.Fatalf("ACK metric = %d, want 0 for unsettled work", got)
	}
	if got := metricValue(t, metrics, "hookflow_worker_rejected_total"); got != 0 {
		t.Fatalf("reject metric = %d, want 0; shutdown must not nack", got)
	}
}

func TestRunGenerationCloseDoesNotWaitForBlockedBrokerSettlement(t *testing.T) {
	channel := &blockingGenerationChannel{started: make(chan struct{}), release: make(chan struct{})}
	defer close(channel.release)
	closeCalled := make(chan struct{})
	generation := &channelGeneration{
		channel: channel, closeBudget: time.Second,
		closeChannel: func() error { close(closeCalled); return nil },
	}
	consumer := &Consumer{
		prefetch: 1, workBudget: 20 * time.Millisecond, processor: ackProcessor{},
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: observability.New(),
	}
	deliveries := make(chan amqp.Delivery)
	delivery := testHandle(t).delivery
	secondReceived := make(chan struct{})
	go func() {
		deliveries <- delivery
		deliveries <- delivery
		close(secondReceived)
	}()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	returned := make(chan bool, 1)
	go func() { returned <- consumer.runGeneration(ctx, generation, deliveries) }()
	select {
	case <-channel.started:
	case <-time.After(time.Second):
		t.Fatal("broker settlement did not start")
	}
	select {
	case <-secondReceived:
	case <-time.After(time.Second):
		t.Fatal("second delivery was not queued behind the full prefetch window")
	}
	cancel()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("generation drain waited for blocked broker settlement")
	}
	if consumer.Quiescent() {
		t.Fatal("consumer reported quiescence while broker settlement was blocked")
	}
	closed := make(chan struct{})
	go func() { generation.close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(time.Second):
		t.Fatal("generation close waited for blocked broker settlement")
	}
	select {
	case <-closeCalled:
	default:
		t.Fatal("generation close did not invoke broker cleanup")
	}
}

func TestSettlementAdmissionAndForceStopAreMutuallyExclusive(t *testing.T) {
	t.Run("force-stop wins", func(t *testing.T) {
		ctx, work := settlementTestContext(t)
		work.forceStop()
		if work.admitSettlement() {
			t.Fatal("settlement admitted after force-stop")
		}
		work.forceStop()
		if work.admitSettlement() {
			t.Fatal("idempotent force-stop allowed later settlement")
		}
		_ = ctx
	})

	t.Run("admission wins", func(t *testing.T) {
		_, work := settlementTestContext(t)
		if !work.admitSettlement() {
			t.Fatal("initial settlement admission failed")
		}
		work.forceStop()
		if work.admitSettlement() {
			t.Fatal("new settlement admitted after force-stop")
		}
		work.mu.Lock()
		state := work.state
		work.mu.Unlock()
		if state != workForced {
			t.Fatalf("state = %v, want forced", state)
		}
	})
}

func TestPreHTTPSettlementForceStopWinsBeforeDatabaseCall(t *testing.T) {
	base := testMemoryProvider(t)
	preparation := encryptedPreparation(t, base, testWebhookSecret, secrets.ProviderLocalFile)
	store := &controlledSettlementStore{
		fakeStore: acquiredStore(preparation),
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	runtime, _ := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), base, store, &fakeExecutor{})
	ctx, work := settlementTestContext(t)
	work.forceStop()

	decision := runtime.settlePreHTTP(ctx, testClaim(), preHTTPFailure{
		reason: delivery.ReasonHeadersInvalid, message: "headers are invalid",
	})
	if decision.Action != disposition.ActionHold {
		t.Fatalf("decision = %+v, want hold", decision)
	}
	if got := store.calls.Load(); got != 0 {
		t.Fatalf("database settlement calls = %d, want 0", got)
	}
}

func TestPreHTTPSettlementAdmissionRemainsTrackedUntilReturn(t *testing.T) {
	base := testMemoryProvider(t)
	preparation := encryptedPreparation(t, base, testWebhookSecret, secrets.ProviderLocalFile)
	store := &controlledSettlementStore{
		fakeStore: acquiredStore(preparation),
		started:   make(chan struct{}),
		release:   make(chan struct{}),
	}
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	runtime, _ := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), base, store, &fakeExecutor{})
	ctx, work := settlementTestContext(t)
	returned := make(chan disposition.Decision, 1)
	go func() {
		returned <- runtime.settlePreHTTP(ctx, testClaim(), preHTTPFailure{
			reason: delivery.ReasonHeadersInvalid, message: "headers are invalid",
		})
	}()

	select {
	case <-store.started:
	case <-time.After(time.Second):
		t.Fatal("database settlement did not start")
	}
	work.forceStop()
	select {
	case <-returned:
		t.Fatal("settlement returned while the database test double was still blocked")
	default:
	}
	work.mu.Lock()
	state := work.state
	work.mu.Unlock()
	if state != workForced {
		t.Fatalf("state = %v, want forced while admitted work remains active", state)
	}
	close(store.release)
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("settlement did not return after release")
	}
	if got := store.calls.Load(); got != 1 {
		t.Fatalf("database settlement calls = %d, want 1", got)
	}
}

func TestRunGenerationBoundsInFlightWaitOnChannelClose(t *testing.T) {
	processor := &blockingProcessor{
		started: make(chan struct{}), release: make(chan struct{}), done: make(chan struct{}),
	}
	consumer := &Consumer{
		prefetch: 1, workBudget: 25 * time.Millisecond, processor: processor,
		logger: slog.New(slog.NewTextHandler(io.Discard, nil)), metrics: observability.New(),
	}
	deliveries := make(chan amqp.Delivery, 1)
	deliveries <- amqp.Delivery{Body: []byte(`{}`)}
	close(deliveries)
	generation := &channelGeneration{}
	returned := make(chan bool, 1)
	go func() { returned <- consumer.runGeneration(context.Background(), generation, deliveries) }()

	select {
	case <-processor.started:
	case <-time.After(time.Second):
		t.Fatal("processor did not start")
	}
	select {
	case graceful := <-returned:
		if graceful {
			t.Fatal("channel loss was reported as a graceful shutdown")
		}
	case <-time.After(time.Second):
		t.Fatal("in-flight wait exceeded the configured work budget")
	}
	close(processor.release)
	select {
	case <-processor.done:
	case <-time.After(time.Second):
		t.Fatal("processor did not finish after release")
	}
}
