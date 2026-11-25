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

type channelGeneration struct {
	mu          sync.Mutex
	channel     *amqp.Channel
	conn        *amqp.Connection
	closeBudget time.Duration
	closed      bool
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
	h.generation.mu.Lock()
	defer h.generation.mu.Unlock()
	if h.generation.closed || h.generation.channel == nil || h.generation.channel.IsClosed() {
		return ErrStaleHandle
	}
	return h.generation.channel.Ack(h.tag, false)
}

// RejectRequeue is the sole RB-4 negative settlement. There is deliberately no
// Nack method on Handle.
func (h *Handle) RejectRequeue() error {
	if h == nil || h.generation == nil {
		return ErrStaleHandle
	}
	h.generation.mu.Lock()
	defer h.generation.mu.Unlock()
	if h.generation.closed || h.generation.channel == nil || h.generation.channel.IsClosed() {
		return ErrStaleHandle
	}
	return h.generation.channel.Reject(h.tag, true)
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
	defer g.mu.Unlock()
	if g.closed {
		return
	}
	g.closed = true
	_ = messaging.CloseChannelAndConnection(g.conn, g.channel, g.closeBudget)
}
