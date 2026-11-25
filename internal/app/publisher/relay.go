package publisher

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
	"sync"
	"time"

	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/observability"
)

// Store is the T1/T2/T3 database surface.
type Store interface {
	Claim(context.Context, outbox.ClaimOptions) ([]outbox.Claim, error)
	MarkPublished(context.Context, outbox.Claim) (bool, error)
	MarkFailed(context.Context, outbox.Failure) (outbox.FailureResult, error)
	ReapExpired(context.Context, outbox.ReapOptions) (outbox.ReapResult, error)
}

// Broker is the bounded channel-generation boundary.
type Broker interface {
	Ensure(context.Context) error
	Publish(context.Context, outbox.Claim) (PublishResult, error)
	Close()
}

// Options are approved D3 runtime values plus explicit dependencies.
type Options struct {
	Config    config.Publisher
	RabbitMQ  config.RabbitMQ
	ProcessID string
	Store     Store
	Broker    Broker
	NewToken  outbox.TokenGenerator
	Logger    *slog.Logger
	Metrics   *observability.Metrics
}

// Relay is the single-owner publisher loop. It never runs claim rounds or
// publishes concurrently with itself.
type Relay struct {
	cfg       config.Publisher
	rabbit    config.RabbitMQ
	broker    Broker
	store     Store
	process   string
	newToken  outbox.TokenGenerator
	logger    *slog.Logger
	metrics   *observability.Metrics
	reconnect int
}

// NewRelay validates all dependencies and approved numeric invariants.
func NewRelay(opts Options) (*Relay, error) {
	if err := opts.Config.Validate(); err != nil {
		return nil, err
	}
	if err := opts.RabbitMQ.Validate(); err != nil {
		return nil, err
	}
	if opts.ProcessID == "" || opts.Store == nil || opts.NewToken == nil || opts.Broker == nil {
		return nil, errors.New("publisher process id, store, token generator and broker are required")
	}
	logger := opts.Logger
	if logger == nil {
		logger = slog.Default()
	}
	metrics := opts.Metrics
	if metrics == nil {
		metrics = observability.New()
	}
	for _, name := range []string{
		"hookflow_publisher_ready", "hookflow_publisher_in_flight",
		"hookflow_publisher_claimed_total", "hookflow_publisher_confirmed_total",
		"hookflow_publisher_returned_total", "hookflow_publisher_nacked_total",
		"hookflow_publisher_unknown_total", "hookflow_publisher_reaped_total",
		"hookflow_publisher_reconnect_total",
	} {
		metrics.Set(name, 0)
	}
	return &Relay{cfg: opts.Config, rabbit: opts.RabbitMQ, broker: opts.Broker, store: opts.Store, process: opts.ProcessID, newToken: opts.NewToken, logger: logger, metrics: metrics}, nil
}

// Close releases the owned broker session.
func (r *Relay) Close() {
	r.broker.Close()
}

// Start performs the bounded initial connection/topology check. Failure is
// returned to the command instead of marking a partially initialized relay ready.
func (r *Relay) Start(ctx context.Context) error {
	if err := r.broker.Ensure(ctx); err != nil {
		return fmt.Errorf("initialize publisher broker: %w", err)
	}
	return nil
}

// Run executes fixed-delay polling, at most four consecutive full claim rounds
// per poll, and one bounded T3 batch per reap interval. T3 has its own goroutine
// so a slow publish batch cannot suspend expired-lease cleanup indefinitely.
func (r *Relay) Run(ctx context.Context) error {
	var reaper sync.WaitGroup
	reaper.Add(1)
	go func() {
		defer reaper.Done()
		reap := time.NewTicker(r.cfg.ReapInterval)
		defer reap.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-reap.C:
				r.reap(ctx)
			}
		}
	}()
	defer reaper.Wait()

	poll := time.NewTimer(0)
	defer poll.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-poll.C:
			full, unknown := r.tick(ctx)
			delay := r.cfg.PollInterval
			if unknown {
				delay = r.reconnectDelay()
			} else if full {
				delay = 0
			}
			poll.Reset(delay)
		}
	}
}

func (r *Relay) tick(ctx context.Context) (full bool, unknown bool) {
	for round := 0; round < 4; round++ {
		if ctx.Err() != nil {
			return false, false
		}
		claims, err := r.store.Claim(ctx, outbox.ClaimOptions{
			BatchSize: r.cfg.BatchSize, MaxClaims: r.cfg.MaxClaims,
			LeaseDuration: r.cfg.LeaseDuration, ProcessID: r.process, NewToken: r.newToken,
		})
		if err != nil {
			if ctx.Err() == nil {
				r.logger.Error("publisher claim failed", "error_class", "database")
			}
			return false, false
		}
		if len(claims) == 0 {
			return false, false
		}
		r.metrics.Add("hookflow_publisher_claimed_total", int64(len(claims)))
		if len(claims) < r.cfg.BatchSize {
			full = false
		} else {
			full = true
		}
		for index, claim := range claims {
			if ctx.Err() != nil {
				r.failRemaining(ctx, claims[index:], "publisher stopped before publish")
				return full, false
			}
			result, publishErr := r.broker.Publish(ctx, claim)
			if publishErr != nil || result != ResultConfirmed {
				r.failRemaining(ctx, claims[index:], "publish outcome was not confirmed")
				return full, true
			}
			applied, err := r.store.MarkPublished(ctx, claim)
			if err != nil {
				r.logger.Error("publisher success commit failed", "error_class", "database")
				r.failRemaining(ctx, claims[index:], "publisher success commit outcome unknown")
				return full, true
			}
			if !applied {
				r.logger.Info("publisher success completion fenced")
			}
			r.reconnect = 0
		}
		if !full {
			return false, false
		}
	}
	// Four full rounds are the hard per-tick cap (800 rows at D3 defaults).
	return false, false
}

func (r *Relay) failRemaining(ctx context.Context, claims []outbox.Claim, diagnostic string) {
	for _, claim := range claims {
		backoff, err := Backoff(claim.ClaimCount, r.cfg.BackoffBase, r.cfg.BackoffCap, r.cfg.BackoffMin, cryptoRandomInt)
		if err != nil {
			r.logger.Error("publisher backoff derivation failed", "error_class", "internal")
			continue
		}
		if _, err := r.store.MarkFailed(ctx, outbox.Failure{Claim: claim, Backoff: backoff, Error: diagnostic}); err != nil && ctx.Err() == nil {
			// T3 remains the fail-closed cleanup path if T2 failure cannot commit.
			r.logger.Error("publisher failure accounting failed", "error_class", "database")
		}
	}
}

func (r *Relay) reap(ctx context.Context) {
	result, err := r.store.ReapExpired(ctx, outbox.ReapOptions{
		BatchSize: r.cfg.BatchSize, MaxClaims: r.cfg.MaxClaims,
		LeaseDuration: r.cfg.LeaseDuration,
		Backoff: func(count int) (time.Duration, error) {
			return Backoff(count, r.cfg.BackoffBase, r.cfg.BackoffCap, r.cfg.BackoffMin, cryptoRandomInt)
		},
	})
	if err != nil {
		if ctx.Err() == nil {
			r.logger.Error("publisher lease reap failed", "error_class", "database")
		}
		return
	}
	r.metrics.Add("hookflow_publisher_reaped_total", int64(len(result.Reaped)))
}

func (r *Relay) reconnectDelay() time.Duration {
	r.reconnect++
	if r.reconnect > 30 {
		r.reconnect = 30
	}
	bound := r.rabbit.ReconnectMinBackoff
	for i := 1; i < r.reconnect && bound < r.rabbit.ReconnectMaxBackoff; i++ {
		if bound > r.rabbit.ReconnectMaxBackoff/2 {
			bound = r.rabbit.ReconnectMaxBackoff
			break
		}
		bound *= 2
	}
	if bound > r.rabbit.ReconnectMaxBackoff {
		bound = r.rabbit.ReconnectMaxBackoff
	}
	width := int64(bound-r.rabbit.ReconnectMinBackoff) + 1
	offset, err := cryptoRandomInt(new(big.Int).SetInt64(width))
	if err != nil {
		return bound
	}
	r.metrics.Add("hookflow_publisher_reconnect_total", 1)
	return r.rabbit.ReconnectMinBackoff + time.Duration(offset.Int64())
}
