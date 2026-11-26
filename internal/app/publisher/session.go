package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

// PublishResult is a completed broker decision. Only ResultConfirmed may lead
// to a successful T2 update.
type PublishResult string

// Publish results distinguish confirmed, returned, nacked and unknown outcomes.
const (
	ResultConfirmed PublishResult = "confirmed"
	ResultReturned  PublishResult = "returned"
	ResultNacked    PublishResult = "nacked"
	ResultUnknown   PublishResult = "unknown"
)

// ErrUnknownOutcome means the broker may have accepted the publish. The whole
// channel generation must be discarded and T2 success must not be attempted.
var ErrUnknownOutcome = errors.New("publisher outcome unknown")

// Session owns one application-managed connection and one bounded confirm-mode
// channel generation at a time. Publish is serialized; N=1 is the V1 reference.
type Session struct {
	cfg         config.RabbitMQ
	confirm     time.Duration
	logger      *slog.Logger
	metrics     *observability.Metrics
	mu          sync.Mutex
	conn        *amqp.Connection
	generation  *channelGeneration
	onPublished func()
}

type channelGeneration struct {
	channel         *amqp.Channel
	returns         chan amqp.Return
	confirmations   chan amqp.Confirmation
	channelClose    chan *amqp.Error
	connectionClose chan *amqp.Error
	decision        *publishDecision
}

// NewSession constructs a session without opening network connections.
func NewSession(cfg config.RabbitMQ, confirmTimeout time.Duration, logger *slog.Logger, metrics *observability.Metrics) (*Session, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if confirmTimeout <= 0 {
		return nil, errors.New("publisher confirm timeout must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = observability.New()
	}
	metrics.Set("hookflow_publisher_ready", 0)
	metrics.Set("hookflow_publisher_in_flight", 0)
	return &Session{cfg: cfg, confirm: confirmTimeout, logger: logger, metrics: metrics}, nil
}

// Ensure opens a connection, verifies topology and installs a fresh generation.
func (s *Session) Ensure(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.ensureLocked(ctx)
}

func (s *Session) ensureLocked(ctx context.Context) error {
	if s.generation != nil && s.conn != nil && !s.conn.IsClosed() {
		return nil
	}
	s.discardLocked()
	conn, err := messaging.ConnectAndEnsure(ctx, s.cfg)
	if err != nil {
		return err
	}
	setupCtx, cancel := context.WithTimeout(ctx, s.cfg.ConnectTimeout)
	defer cancel()
	stopWatch := messaging.WatchConnectionContext(setupCtx, conn)
	defer stopWatch()
	channel, err := conn.Channel()
	if err != nil {
		_ = messaging.CloseChannelAndConnection(conn, nil, s.cfg.ConnectTimeout)
		return fmt.Errorf("open publisher channel: %w", err)
	}
	if err := channel.Confirm(false); err != nil {
		_ = messaging.CloseChannelAndConnection(conn, channel, s.cfg.ConnectTimeout)
		return fmt.Errorf("enable publisher confirms: %w", err)
	}

	generation := &channelGeneration{
		channel:         channel,
		returns:         make(chan amqp.Return, 1),
		confirmations:   make(chan amqp.Confirmation, 1),
		channelClose:    make(chan *amqp.Error, 1),
		connectionClose: make(chan *amqp.Error, 1),
	}
	channel.NotifyReturn(generation.returns)
	channel.NotifyPublish(generation.confirmations)
	channel.NotifyClose(generation.channelClose)
	conn.NotifyClose(generation.connectionClose)
	s.conn = conn
	s.generation = generation
	s.metrics.Set("hookflow_publisher_ready", 1)
	s.logger.Info("publisher channel generation ready")
	return nil
}

// Publish sends one identifiers-only message and waits for a complete
// return-aware confirmation decision. The caller is the single owner goroutine.
func (s *Session) Publish(ctx context.Context, claim outbox.Claim) (PublishResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if err := s.ensureLocked(ctx); err != nil {
		return ResultUnknown, fmt.Errorf("%w: establish generation: %w", ErrUnknownOutcome, err)
	}
	generation := s.generation
	if generation.decision != nil {
		return ResultUnknown, s.failLocked("publish started with an unresolved generation")
	}
	if err := drainReturns(generation); err != nil {
		return ResultUnknown, s.failLocked(err.Error())
	}
	if err := drainConfirms(generation); err != nil {
		return ResultUnknown, s.failLocked(err.Error())
	}

	envelope := messaging.Envelope{
		SchemaVersion:  messaging.SchemaVersion,
		OrganizationID: claim.OrganizationID,
		DeliveryID:     claim.DeliveryID,
		DispatchSeq:    claim.Sequence,
		OutboxID:       claim.MessageID,
	}
	publishing, err := messaging.EncodePublishing(envelope)
	if err != nil {
		return ResultUnknown, s.failLocked("publisher claim has an invalid message contract")
	}
	if claim.Topic != messaging.DispatchRoutingKey || (claim.Kind != "delivery.dispatch" && claim.Kind != "delivery.retry") {
		return ResultUnknown, s.failLocked("publisher claim has an incompatible routing contract")
	}

	decision := &publishDecision{messageID: claim.MessageID, sent: true}
	generation.decision = decision
	waitCtx, cancel := context.WithTimeout(ctx, s.confirm)
	defer cancel()
	stopWatch := messaging.WatchConnectionContext(waitCtx, s.conn)
	defer stopWatch()
	confirmation, err := generation.channel.PublishWithDeferredConfirmWithContext(
		waitCtx, messaging.DispatchExchange, messaging.DispatchRoutingKey, true, false, publishing,
	)
	if err != nil || confirmation == nil {
		return ResultUnknown, s.failLocked("local publish did not produce a deferred confirmation")
	}
	decision.tag = confirmation.DeliveryTag
	s.metrics.Set("hookflow_publisher_in_flight", 1)
	if s.onPublished != nil {
		s.onPublished()
	}

	for {
		select {
		case <-waitCtx.Done():
			return ResultUnknown, s.failLocked("publisher confirmation deadline expired")
		case cause := <-generation.connectionClose:
			_ = cause // The close reason is intentionally not logged; outcome is UNKNOWN.
			return ResultUnknown, s.failLocked("publisher connection closed before decision")
		case cause := <-generation.channelClose:
			_ = cause // The close reason is intentionally not logged; outcome is UNKNOWN.
			return ResultUnknown, s.failLocked("publisher channel closed before decision")
		case value, open := <-generation.returns:
			if !open {
				return ResultUnknown, s.failLocked("publisher return listener closed")
			}
			if err := decision.observeReturn(value); err != nil {
				return ResultUnknown, s.failLocked(err.Error())
			}
		case value, open := <-generation.confirmations:
			if !open {
				return ResultUnknown, s.failLocked("publisher confirmation listener closed")
			}
			// Wire order enqueues a mandatory return before its confirm. Drain
			// before and after applying the confirm so Go select order cannot
			// turn an unroutable publish into a false success.
			if err := drainDecisionReturns(generation, decision); err != nil {
				return ResultUnknown, s.failLocked(err.Error())
			}
			if err := decision.observeConfirmation(value); err != nil {
				return ResultUnknown, s.failLocked(err.Error())
			}
			if err := drainDecisionReturns(generation, decision); err != nil {
				return ResultUnknown, s.failLocked(err.Error())
			}
			if waitCtx.Err() != nil || s.conn.IsClosed() || generation.channel.IsClosed() {
				return ResultUnknown, s.failLocked("publisher generation ended before final decision")
			}
			result, err := decision.result()
			generation.decision = nil
			s.metrics.Set("hookflow_publisher_in_flight", 0)
			if err != nil {
				return ResultUnknown, s.failLocked(err.Error())
			}
			if result == ResultReturned {
				s.metrics.Add("hookflow_publisher_returned_total", 1)
			} else {
				s.metrics.Add("hookflow_publisher_confirmed_total", 1)
			}
			return result, nil
		}
	}
}

func drainDecisionReturns(generation *channelGeneration, decision *publishDecision) error {
	for {
		select {
		case value, open := <-generation.returns:
			if !open {
				return errors.New("return listener closed while draining")
			}
			if err := decision.observeReturn(value); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

func drainReturns(generation *channelGeneration) error {
	select {
	case _, open := <-generation.returns:
		if !open {
			return errors.New("return listener closed while draining")
		}
		return errors.New("stale publisher return before decision")
	default:
		return nil
	}
}

func drainConfirms(generation *channelGeneration) error {
	select {
	case _, open := <-generation.confirmations:
		if !open {
			return errors.New("confirmation listener closed while draining")
		}
		return errors.New("stale publisher confirmation before decision")
	default:
		return nil
	}
}

func (s *Session) failLocked(reason string) error {
	s.metrics.Add("hookflow_publisher_unknown_total", 1)
	s.logger.Warn("publisher generation invalidated", "reason", reason)
	s.discardLocked()
	return fmt.Errorf("%w: %s", ErrUnknownOutcome, reason)
}

func (s *Session) discardLocked() {
	var channel *amqp.Channel
	if s.generation != nil {
		channel = s.generation.channel
	}
	if err := messaging.CloseChannelAndConnection(s.conn, channel, s.cfg.ConnectTimeout); err != nil {
		s.logger.Warn("publisher generation cleanup failed", "error_class", "broker")
	}
	s.generation = nil
	s.conn = nil
	s.metrics.Set("hookflow_publisher_ready", 0)
	s.metrics.Set("hookflow_publisher_in_flight", 0)
}

// Close releases the channel before the connection.
func (s *Session) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.discardLocked()
}
