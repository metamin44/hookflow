// Package idempotency implements the approved U43 expired-record sweep.
//
// The sweep is a space optimization, not a correctness prerequisite: a key
// becomes reusable at expires_at whether or not this job runs. Every batch is
// deleted in its own transaction and failures are isolated from ingestion and
// delivery.
package idempotency

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// BatchDeleter removes at most limit expired idempotency records.
type BatchDeleter interface {
	DeleteExpiredBatch(ctx context.Context, limit int) (int, error)
}

// Options are the approved U43 sweep settings.
type Options struct {
	BatchSize         int
	Interval          time.Duration
	MaxBatchesPerTick int
}

// Sweeper periodically deletes expired idempotency records.
type Sweeper struct {
	store  BatchDeleter
	opts   Options
	logger *slog.Logger

	fullBatchStreak int
	sweepErrors     uint64
}

// NewSweeper creates a sweeper. Options are validated by Run so that a bad
// configuration is reported as a startup error instead of a panic.
func NewSweeper(store BatchDeleter, opts Options, logger *slog.Logger) *Sweeper {
	if logger == nil {
		logger = slog.Default()
	}
	return &Sweeper{
		store:  store,
		opts:   opts,
		logger: logger,
	}
}

// Run sweeps until ctx is canceled. It returns an error only for an invalid
// configuration; batch failures are logged, counted and retried on the next
// tick.
func (s *Sweeper) Run(ctx context.Context) error {
	if s.opts.BatchSize <= 0 {
		return errors.New("idempotency sweep batch size must be positive")
	}
	if s.opts.Interval <= 0 {
		return errors.New("idempotency sweep interval must be positive")
	}
	if s.opts.MaxBatchesPerTick <= 0 {
		return errors.New("idempotency sweep max batches per tick must be positive")
	}
	if s.store == nil {
		return errors.New("idempotency sweep store is required")
	}

	ticker := time.NewTicker(s.opts.Interval)
	defer ticker.Stop()

	return s.run(ctx, ticker.C)
}

func (s *Sweeper) run(ctx context.Context, ticks <-chan time.Time) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			s.tick(ctx)
		}
	}
}

func (s *Sweeper) tick(ctx context.Context) {
	start := time.Now()
	deleted := 0
	batches := 0
	lastBatch := 0

	for batches < s.opts.MaxBatchesPerTick {
		if ctx.Err() != nil {
			return
		}

		removed, err := s.store.DeleteExpiredBatch(ctx, s.opts.BatchSize)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.sweepErrors++
			s.fullBatchStreak = 0
			s.logger.Error("idempotency sweep batch failed",
				"deleted", deleted,
				"batches", batches,
				"sweep_errors_total", s.sweepErrors,
				"error", err,
			)
			return
		}

		deleted += removed
		batches++
		lastBatch = removed
		if removed < s.opts.BatchSize {
			break
		}
	}

	if batches == s.opts.MaxBatchesPerTick && lastBatch == s.opts.BatchSize {
		s.fullBatchStreak++
	} else {
		s.fullBatchStreak = 0
	}

	s.logger.Info("idempotency sweep tick completed",
		"deleted", deleted,
		"batches", batches,
		"full_batch_streak", s.fullBatchStreak,
		"sweep_errors_total", s.sweepErrors,
		"duration_ms", time.Since(start).Milliseconds(),
	)
}
