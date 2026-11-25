package recovery

import (
	"context"
	"errors"
	"log/slog"
	"time"
)

// Runner is one bounded Recovery invocation.
type Runner interface {
	Run(context.Context) (Report, error)
}

// Scheduler runs fixed-delay Recovery invocations. A single goroutine means
// invocations never overlap; the next cadence starts after the prior run ends.
type Scheduler struct {
	runner  Runner
	cadence time.Duration
	logger  *slog.Logger
}

// NewScheduler validates the cadence and runner.
func NewScheduler(runner Runner, cadence time.Duration, logger *slog.Logger) (*Scheduler, error) {
	if runner == nil {
		return nil, errors.New("recovery runner is required")
	}
	if cadence <= 0 {
		return nil, errors.New("recovery cadence must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Scheduler{runner: runner, cadence: cadence, logger: logger}, nil
}

// Run executes immediately, then waits a full cadence after each completion.
// Errors are observable and never permanently stop future invocations.
func (s *Scheduler) Run(ctx context.Context) error {
	for {
		if ctx.Err() != nil {
			return nil //nolint:nilerr // Context cancellation is normal scheduler shutdown.
		}
		started := time.Now()
		report, err := s.runner.Run(ctx)
		if err != nil && ctx.Err() == nil {
			s.logger.Error("recovery invocation failed", "duration", time.Since(started).String(), "error_class", "database")
		}
		if err == nil {
			s.logger.Info("recovery scheduler invocation completed",
				"duration", report.Duration.String(),
				"expire_applied", report.Expire.Applied,
				"repair_applied", report.Repair.Applied,
				"rearm_applied", report.ReArm.Applied,
				"stale_applied", report.Stale.Applied,
			)
		}

		timer := time.NewTimer(s.cadence)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return nil
		case <-timer.C:
		}
	}
}
