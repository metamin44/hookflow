package worker

import (
	"context"
	"errors"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/platform/messaging"
)

// ErrStaleHandle means the delivery's channel generation is no longer usable.
// It must never be retried on a replacement channel, even if the numeric tag is
// reused there.
var ErrStaleHandle = errors.New("delivery handle belongs to a closed channel generation")

type generationChannel interface {
	Ack(uint64, bool) error
	Reject(uint64, bool) error
	IsClosed() bool
	Consume(string, string, bool, bool, bool, bool, amqp.Table) (<-chan amqp.Delivery, error)
}

type channelGeneration struct {
	mu           sync.Mutex
	channel      generationChannel
	conn         *amqp.Connection
	closeBudget  time.Duration
	closeChannel func() error
	closed       bool
}

// Handle is a type-safe broker delivery owned by exactly one channel generation.
type Handle struct {
	generation *channelGeneration
	tag        uint64
	body       []byte
	delivery   amqp.Delivery
}

func newHandle(generation *channelGeneration, delivery amqp.Delivery) *Handle {
	delivery.Body = append([]byte(nil), delivery.Body...)
	return &Handle{generation: generation, tag: delivery.DeliveryTag, body: delivery.Body, delivery: delivery}
}

// Body returns a copy of the identifiers-only broker payload.
func (h *Handle) Body() []byte {
	return append([]byte(nil), h.body...)
}

// Ack acknowledges only on the owning, still-open generation.
func (h *Handle) Ack() error {
	if h == nil || h.generation == nil {
		return ErrStaleHandle
	}
	channel := h.generation.settlementChannel()
	if channel == nil {
		return ErrStaleHandle
	}
	return channel.Ack(h.tag, false)
}

// RejectRequeue is the sole RB-4 negative settlement. There is deliberately no
// Nack method on Handle.
func (h *Handle) RejectRequeue() error {
	if h == nil || h.generation == nil {
		return ErrStaleHandle
	}
	channel := h.generation.settlementChannel()
	if channel == nil {
		return ErrStaleHandle
	}
	return channel.Reject(h.tag, true)
}

func (g *channelGeneration) settlementChannel() generationChannel {
	g.mu.Lock()
	if g.closed || g.channel == nil {
		g.mu.Unlock()
		return nil
	}
	channel := g.channel
	g.mu.Unlock()
	if channel.IsClosed() {
		return nil
	}
	return channel
}

func (g *channelGeneration) close() {
	if g == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), g.closeBudget)
	defer cancel()
	stopWatch := messaging.WatchConnectionContext(ctx, g.conn)
	defer stopWatch()
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return
	}
	g.closed = true
	closeChannel := g.closeChannel
	g.mu.Unlock()
	if closeChannel != nil {
		_ = closeChannel()
	}
}
