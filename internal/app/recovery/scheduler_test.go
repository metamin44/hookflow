package recovery

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRunner struct {
	calls atomic.Int64
	run   func(context.Context) (Report, error)
}

func (r *fakeRunner) Run(ctx context.Context) (Report, error) {
	r.calls.Add(1)
	if r.run != nil {
		return r.run(ctx)
	}
	return Report{}, nil
}

func TestSchedulerRunsImmediatelyAndStops(t *testing.T) {
	runner := &fakeRunner{}
	scheduler, err := NewScheduler(runner, time.Hour, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	deadline := time.After(time.Second)
	for runner.calls.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("scheduler did not run immediately")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run() error: %v", err)
	}
}

func TestSchedulerContinuesAfterFailure(t *testing.T) {
	var runner *fakeRunner
	runner = &fakeRunner{run: func(context.Context) (Report, error) {
		if runner.calls.Load() == 1 {
			return Report{}, errors.New("temporary database failure")
		}
		return Report{}, nil
	}}
	scheduler, err := NewScheduler(runner, 5*time.Millisecond, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := scheduler.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if runner.calls.Load() < 2 {
		t.Fatal("scheduler stopped after a recoverable error")
	}
}

func TestSchedulerRejectsInvalidOptions(t *testing.T) {
	if _, err := NewScheduler(nil, time.Second, nil); err == nil {
		t.Fatal("nil runner accepted")
	}
	if _, err := NewScheduler(&fakeRunner{}, 0, nil); err == nil {
		t.Fatal("zero cadence accepted")
	}
}
