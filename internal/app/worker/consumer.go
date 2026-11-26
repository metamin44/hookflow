// Package worker consumes exact dispatch pointers and applies fenced delivery dispositions.
package worker

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"sync/atomic"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

// Processor establishes a durable DB disposition for one delivery. The consumer
// applies the returned broker action only after Process returns. Implementations
// must honor ctx for every database operation; the production Runtime also uses
// the per-work synchronized settlement gate carried by ctx.
type Processor interface {
	Process(context.Context, *Handle) disposition.Decision
}

type settlementForceContextKey struct{}
type settlementWorkKey struct{}

func settlementForceContext(ctx context.Context) context.Context {
	if ctx == nil {
		return nil
	}
	force, _ := ctx.Value(settlementForceContextKey{}).(context.Context)
	return force
}

func settlementWork(ctx context.Context) *processorWork {
	if ctx == nil {
		return nil
	}
	work, _ := ctx.Value(settlementWorkKey{}).(*processorWork)
	return work
}

type workState uint8

const (
	workActive workState = iota
	workSettlementStarted
	workForced
	workFinished
)

// processorWork is the single synchronization point for settlement admission
// and force-stop. Admission and force-stop cannot both win the state transition.
type processorWork struct {
	mu          sync.Mutex
	state       workState
	cancel      context.CancelFunc
	forceCancel context.CancelFunc
}

func (w *processorWork) admitSettlement() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.state == workForced || w.state == workFinished {
		return false
	}
	w.state = workSettlementStarted
	return true
}

func (w *processorWork) forceStop() {
	w.mu.Lock()
	if w.state != workFinished {
		w.state = workForced
	}
	w.mu.Unlock()
	w.forceCancel()
	w.cancel()
}

func (w *processorWork) finish() {
	w.mu.Lock()
	w.state = workFinished
	w.mu.Unlock()
}

// workTracker owns every processor goroutine that can still begin database or
// broker settlement. Force-stop cancels both the work deadline and the separate
// pre-HTTP settlement signal before Consumer.Run may return.
type workTracker struct {
	mu    sync.Mutex
	works map[*processorWork]struct{}
}

func newWorkTracker() *workTracker {
	return &workTracker{works: make(map[*processorWork]struct{})}
}

func (t *workTracker) add(cancel, forceCancel context.CancelFunc) *processorWork {
	work := &processorWork{cancel: cancel, forceCancel: forceCancel}
	t.mu.Lock()
	t.works[work] = struct{}{}
	t.mu.Unlock()
	return work
}

func (t *workTracker) remove(work *processorWork) {
	t.mu.Lock()
	delete(t.works, work)
	t.mu.Unlock()
}

func (t *workTracker) forceStop() {
	t.mu.Lock()
	works := make([]*processorWork, 0, len(t.works))
	for work := range t.works {
		works = append(works, work)
	}
	t.mu.Unlock()
	for _, work := range works {
		work.forceStop()
	}
}

// ErrShutdownIncomplete reports that force-stop could not drain tracked work
// before the bounded deadline. Callers must not close a database pool underneath
// the remaining processors.
var ErrShutdownIncomplete = errors.New("worker shutdown incomplete: tracked settlement remains active")

// Consumer owns application-managed reconnection. Automatic amqp091-go recovery
// is disabled so no delivery tag can silently migrate to a replacement channel.
type Consumer struct {
	cfg        config.RabbitMQ
	prefetch   int
	workBudget time.Duration
	processor  Processor
	logger     *slog.Logger
	metrics    *observability.Metrics
	attempt    int
	inFlight   atomic.Int64
}

// Quiescent reports whether every tracked processor and admitted settlement has
// returned. It is safe to consult before explicit PostgreSQL pool cleanup.
func (c *Consumer) Quiescent() bool {
	return c != nil && c.inFlight.Load() == 0
}

// NewConsumer validates the broker, prefetch and processor.
func NewConsumer(cfg config.RabbitMQ, prefetch int, workBudget time.Duration, processor Processor, logger *slog.Logger, metrics *observability.Metrics) (*Consumer, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if prefetch <= 0 || prefetch > 65535 {
		return nil, errors.New("worker prefetch must be between 1 and 65535")
	}
	if workBudget <= 0 || processor == nil {
		return nil, errors.New("worker budget and processor are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = observability.New()
	}
	metrics.Set("hookflow_worker_ready", 0)
	metrics.Set("hookflow_worker_in_flight", 0)
	metrics.Set("hookflow_worker_consumed_total", 0)
	metrics.Set("hookflow_worker_acked_total", 0)
	metrics.Set("hookflow_worker_rejected_total", 0)
	metrics.Set("hookflow_worker_stale_handle_total", 0)
	metrics.Set("hookflow_worker_reconnect_total", 0)
	return &Consumer{cfg: cfg, prefetch: prefetch, workBudget: workBudget, processor: processor, logger: logger, metrics: metrics}, nil
}

// Start performs a fail-closed initial topology, QoS, and consumer check.
func (c *Consumer) Start(ctx context.Context) error {
	conn, generation, err := c.openGeneration(ctx)
	if err != nil {
		return err
	}
	defer generation.close()
	setupCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	stopWatch := messaging.WatchConnectionContext(setupCtx, conn)
	defer stopWatch()
	defer c.metrics.Set("hookflow_worker_ready", 0)
	consumerTag, err := id.NewV7()
	if err != nil {
		return fmt.Errorf("generate worker consumer tag: %w", err)
	}
	if _, err := generation.channel.Consume(messaging.DispatchQueue, consumerTag, false, false, false, false, nil); err != nil {
		return fmt.Errorf("verify worker consumer: %w", err)
	}
	c.metrics.Set("hookflow_worker_ready", 1)
	return nil
}

func (c *Consumer) openGeneration(ctx context.Context) (*amqp.Connection, *channelGeneration, error) {
	setupCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	conn, err := messaging.ConnectAndEnsure(setupCtx, c.cfg)
	if err != nil {
		return nil, nil, err
	}
	stopWatch := messaging.WatchConnectionContext(setupCtx, conn)
	defer stopWatch()
	channel, err := conn.Channel()
	if err != nil {
		_ = messaging.CloseChannelAndConnection(conn, nil, c.cfg.ConnectTimeout)
		return nil, nil, fmt.Errorf("open worker channel: %w", err)
	}
	if err := channel.Qos(c.prefetch, 0, false); err != nil {
		_ = messaging.CloseChannelAndConnection(conn, channel, c.cfg.ConnectTimeout)
		return nil, nil, fmt.Errorf("configure worker QoS: %w", err)
	}
	generation := &channelGeneration{channel: channel, conn: conn, closeBudget: c.cfg.ConnectTimeout}
	generation.closeChannel = func() error {
		return messaging.CloseChannelAndConnection(conn, channel, c.cfg.ConnectTimeout)
	}
	c.logger.Info("worker channel generation configured", "prefetch", c.prefetch)
	return conn, generation, nil
}

// Run consumes until shutdown, recreating the entire connection/channel after
// failure. It waits for in-flight DB dispositions before reconnecting.
func (c *Consumer) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // Context cancellation is normal consumer shutdown.
		}
		conn, generation, deliveries, err := c.consumeGeneration(ctx)
		if err != nil {
			c.metrics.Set("hookflow_worker_ready", 0)
			if ctx.Err() != nil {
				return nil //nolint:nilerr // Shutdown cancellation is not a runtime failure.
			}
			c.logger.Error("worker broker generation failed", "error_class", "broker")
			if err := c.waitReconnect(ctx); err != nil {
				return nil //nolint:nilerr // Reconnect backoff ends only on shutdown.
			}
			continue
		}

		graceful := c.runGeneration(ctx, generation, deliveries)
		generation.close()
		_ = conn.Close()
		c.metrics.Set("hookflow_worker_ready", 0)
		if !c.Quiescent() {
			return fmt.Errorf("%w: force-stop drain exhausted", ErrShutdownIncomplete)
		}
		if graceful || ctx.Err() != nil {
			return nil //nolint:nilerr // A closed generation during shutdown is expected.
		}
		c.logger.Warn("worker reconnecting after channel generation loss")
		if err := c.waitReconnect(ctx); err != nil {
			return nil //nolint:nilerr // Reconnect backoff ends only on shutdown.
		}
	}
}

func (c *Consumer) consumeGeneration(ctx context.Context) (*amqp.Connection, *channelGeneration, <-chan amqp.Delivery, error) {
	conn, generation, err := c.openGeneration(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	setupCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	stopWatch := messaging.WatchConnectionContext(setupCtx, conn)
	defer stopWatch()
	consumerTag, err := id.NewV7()
	if err != nil {
		generation.close()
		_ = conn.Close()
		return nil, nil, nil, fmt.Errorf("generate worker consumer tag: %w", err)
	}
	deliveries, err := generation.channel.Consume(messaging.DispatchQueue, consumerTag, false, false, false, false, nil)
	if err != nil {
		generation.close()
		_ = conn.Close()
		return nil, nil, nil, fmt.Errorf("start worker consumer: %w", err)
	}
	c.metrics.Set("hookflow_worker_ready", 1)
	return conn, generation, deliveries, nil
}

func (c *Consumer) runGeneration(ctx context.Context, generation *channelGeneration, deliveries <-chan amqp.Delivery) bool {
	semaphore := make(chan struct{}, c.prefetch)
	var wait sync.WaitGroup
	tracker := newWorkTracker()
	stop := make(chan struct{})
	var stopOnce sync.Once

	for {
		select {
		case <-stop:
			c.waitInFlight(&wait, tracker, "generation stop")
			return false
		default:
		}
		select {
		case <-ctx.Done():
			stopOnce.Do(func() { close(stop) })
			c.waitInFlight(&wait, tracker, "shutdown")
			return true
		case <-stop:
			c.waitInFlight(&wait, tracker, "generation stop")
			return false
		case delivery, open := <-deliveries:
			if !open {
				stopOnce.Do(func() { close(stop) })
				c.waitInFlight(&wait, tracker, "channel close")
				return false
			}
			select {
			case semaphore <- struct{}{}:
			case <-stop:
				c.waitInFlight(&wait, tracker, "generation stop")
				return false
			case <-ctx.Done():
				stopOnce.Do(func() { close(stop) })
				c.waitInFlight(&wait, tracker, "shutdown")
				return true
			}
			select {
			case <-stop:
				<-semaphore
				c.waitInFlight(&wait, tracker, "generation stop")
				return false
			case <-ctx.Done():
				<-semaphore
				stopOnce.Do(func() { close(stop) })
				c.waitInFlight(&wait, tracker, "shutdown")
				return true
			default:
			}
			wait.Add(1)
			// Register work before launching its goroutine so force-stop cannot
			// miss a delivery whose processor has not yet been scheduled.
			workCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.workBudget)
			forceCtx, forceCancel := context.WithCancel(context.WithoutCancel(ctx))
			workCtx = context.WithValue(workCtx, settlementForceContextKey{}, forceCtx)
			work := tracker.add(cancel, forceCancel)
			workCtx = context.WithValue(workCtx, settlementWorkKey{}, work)
			c.inFlight.Add(1)
			c.metrics.Add("hookflow_worker_in_flight", 1)
			c.metrics.Add("hookflow_worker_consumed_total", 1)
			go func() {
				defer wait.Done()
				defer c.inFlight.Add(-1)
				defer work.finish()
				defer tracker.remove(work)
				defer cancel()
				defer forceCancel()
				defer func() { <-semaphore }()
				defer c.metrics.Add("hookflow_worker_in_flight", -1)
				stopWatch := messaging.WatchConnectionContext(workCtx, generation.conn)
				defer stopWatch()
				handle := newHandle(generation, delivery)
				decision := c.processor.Process(workCtx, handle)
				if !work.admitSettlement() {
					return // Force-stop leaves the broker delivery unsettled.
				}
				settlementFailed := c.apply(decision, handle)
				if decision.Action == disposition.ActionHold || settlementFailed {
					stopOnce.Do(func() { close(stop) })
				}
			}()
		}
	}
}

func (c *Consumer) waitInFlight(wait *sync.WaitGroup, tracker *workTracker, reason string) bool {
	finished := make(chan struct{})
	go func() {
		wait.Wait()
		close(finished)
	}()
	timer := time.NewTimer(c.workBudget)
	defer timer.Stop()
	select {
	case <-finished:
		return true
	case <-timer.C:
		tracker.forceStop()
		c.logger.Warn("worker in-flight budget exhausted; forcing cancellation before channel requeue", "reason", reason)
	}

	// Force-stop atomically closes settlement admission. This second bounded
	// window lets admitted work actually return before the pool may be closed.
	forceTimer := time.NewTimer(c.forceDrainBudget())
	defer forceTimer.Stop()
	select {
	case <-finished:
		return true
	case <-forceTimer.C:
		c.logger.Error("worker processor force-stop drain exhausted; unsettled messages remain unacknowledged", "reason", reason)
		return false
	}
}

func (c *Consumer) forceDrainBudget() time.Duration {
	if c.workBudget < preHTTPSettlementTimeout {
		return c.workBudget
	}
	return preHTTPSettlementTimeout
}

func (c *Consumer) apply(decision disposition.Decision, handle *Handle) bool {
	var err error
	switch decision.Action {
	case disposition.ActionAck:
		err = handle.Ack()
		if err == nil {
			c.metrics.Add("hookflow_worker_acked_total", 1)
		}
	case disposition.ActionRejectRequeue:
		err = handle.RejectRequeue()
		if err == nil {
			c.metrics.Add("hookflow_worker_rejected_total", 1)
		}
	case disposition.ActionHold:
		return false
	default:
		err = errors.New("unknown disposition action")
	}
	if err != nil && !errors.Is(err, ErrStaleHandle) {
		c.logger.Error("worker broker settlement failed", "error_class", "broker")
	}
	if errors.Is(err, ErrStaleHandle) {
		c.metrics.Add("hookflow_worker_stale_handle_total", 1)
	}
	return err != nil
}

func (c *Consumer) waitReconnect(ctx context.Context) error {
	c.attempt++
	if c.attempt > 30 {
		c.attempt = 30
	}
	bound := c.cfg.ReconnectMinBackoff
	for i := 1; i < c.attempt && bound < c.cfg.ReconnectMaxBackoff; i++ {
		if bound > c.cfg.ReconnectMaxBackoff/2 {
			bound = c.cfg.ReconnectMaxBackoff
			break
		}
		bound *= 2
	}
	if bound > c.cfg.ReconnectMaxBackoff {
		bound = c.cfg.ReconnectMaxBackoff
	}
	width := int64(bound-c.cfg.ReconnectMinBackoff) + 1
	offset, err := randInt(new(big.Int).SetInt64(width))
	if err != nil {
		offset = big.NewInt(0)
	}
	c.metrics.Add("hookflow_worker_reconnect_total", 1)
	timer := time.NewTimer(c.cfg.ReconnectMinBackoff + time.Duration(offset.Int64()))
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
