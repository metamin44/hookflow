package idempotency

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

type batchResult struct {
	deleted int
	err     error
}

type fakeStore struct {
	results  []batchResult
	calls    int
	requests []int
}

func (f *fakeStore) DeleteExpiredBatch(_ context.Context, limit int) (int, error) {
	f.requests = append(f.requests, limit)
	if f.calls >= len(f.results) {
		return 0, errors.New("unexpected sweep call")
	}
	result := f.results[f.calls]
	f.calls++
	return result.deleted, result.err
}

func newTestLogger(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
}

func defaultOptions() Options {
	return Options{BatchSize: 500, Interval: time.Minute, MaxBatchesPerTick: 5}
}

func TestSweepShortBatchStopsTick(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{results: []batchResult{{deleted: 3}}}
	sweeper := NewSweeper(store, defaultOptions(), newTestLogger(&logs))

	sweeper.tick(context.Background())

	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
	if store.requests[0] != 500 {
		t.Fatalf("batch limit = %d, want 500", store.requests[0])
	}
	if !strings.Contains(logs.String(), `"deleted":3`) {
		t.Fatalf("log does not report deleted rows: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"batches":1`) {
		t.Fatalf("log does not report batches: %s", logs.String())
	}
	if sweeper.fullBatchStreak != 0 {
		t.Fatalf("full batch streak = %d, want 0", sweeper.fullBatchStreak)
	}
}

func TestSweepFullBatchesRunToLimit(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{results: []batchResult{{deleted: 2}, {deleted: 2}, {deleted: 2}}}
	sweeper := NewSweeper(store, Options{BatchSize: 2, Interval: time.Minute, MaxBatchesPerTick: 3}, newTestLogger(&logs))

	sweeper.tick(context.Background())

	if store.calls != 3 {
		t.Fatalf("store calls = %d, want 3", store.calls)
	}
	if sweeper.fullBatchStreak != 1 {
		t.Fatalf("full batch streak = %d, want 1", sweeper.fullBatchStreak)
	}
	if !strings.Contains(logs.String(), `"deleted":6`) {
		t.Fatalf("log does not report total deleted rows: %s", logs.String())
	}
}

func TestSweepFullBatchStreakResetsOnShortBatch(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{results: []batchResult{{deleted: 2}, {deleted: 1}}}
	sweeper := NewSweeper(store, Options{BatchSize: 2, Interval: time.Minute, MaxBatchesPerTick: 5}, newTestLogger(&logs))

	sweeper.tick(context.Background())
	if store.calls != 2 {
		t.Fatalf("store calls = %d, want 2", store.calls)
	}
	if sweeper.fullBatchStreak != 0 {
		t.Fatalf("full batch streak = %d, want 0", sweeper.fullBatchStreak)
	}
}

func TestSweepBatchErrorIsIsolatedAndCounted(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{results: []batchResult{
		{deleted: 2},
		{err: errors.New("database unavailable")},
		{deleted: 1},
	}}
	sweeper := NewSweeper(store, Options{BatchSize: 2, Interval: time.Minute, MaxBatchesPerTick: 5}, newTestLogger(&logs))

	sweeper.tick(context.Background())

	if store.calls != 2 {
		t.Fatalf("store calls = %d, want 2", store.calls)
	}
	if sweeper.sweepErrors != 1 {
		t.Fatalf("sweep errors = %d, want 1", sweeper.sweepErrors)
	}
	if !strings.Contains(logs.String(), "idempotency sweep batch failed") {
		t.Fatalf("log does not report the batch failure: %s", logs.String())
	}
	if !strings.Contains(logs.String(), `"sweep_errors_total":1`) {
		t.Fatalf("log does not report the error counter: %s", logs.String())
	}

	sweeper.tick(context.Background())
	if store.calls != 3 {
		t.Fatalf("store did not resume after the failed tick: calls = %d", store.calls)
	}
	if sweeper.sweepErrors != 1 {
		t.Fatalf("sweep errors = %d after recovery, want 1", sweeper.sweepErrors)
	}
}

func TestSweepContextCancellationStopsRun(t *testing.T) {
	var logs bytes.Buffer
	store := &fakeStore{}
	sweeper := NewSweeper(store, defaultOptions(), newTestLogger(&logs))

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- sweeper.run(ctx, ticks)
	}()

	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not stop after cancellation")
	}
}

type blockingStore struct {
	started chan struct{}
	once    sync.Once
}

func (s *blockingStore) DeleteExpiredBatch(ctx context.Context, _ int) (int, error) {
	s.once.Do(func() { close(s.started) })
	<-ctx.Done()
	return 0, ctx.Err()
}

func TestSweepCancellationMidBatchIsNotLoggedAsFailure(t *testing.T) {
	var logs bytes.Buffer
	store := &blockingStore{started: make(chan struct{})}
	sweeper := NewSweeper(store, defaultOptions(), newTestLogger(&logs))

	ctx, cancel := context.WithCancel(context.Background())
	ticks := make(chan time.Time)
	done := make(chan error, 1)
	go func() {
		done <- sweeper.run(ctx, ticks)
	}()

	ticks <- time.Now()
	<-store.started
	cancel()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("run() error: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("run() did not stop after cancellation")
	}

	if strings.Contains(logs.String(), "idempotency sweep batch failed") {
		t.Fatalf("cancellation was logged as a batch failure: %s", logs.String())
	}
}

func TestSweepRejectsInvalidOptions(t *testing.T) {
	valid := defaultOptions()

	tests := []struct {
		name   string
		store  BatchDeleter
		opts   Options
		errSub string
	}{
		{name: "nil store", store: nil, opts: valid, errSub: "store"},
		{name: "zero batch", store: &fakeStore{}, opts: Options{BatchSize: 0, Interval: time.Minute, MaxBatchesPerTick: 1}, errSub: "batch size"},
		{name: "zero interval", store: &fakeStore{}, opts: Options{BatchSize: 1, Interval: 0, MaxBatchesPerTick: 1}, errSub: "interval"},
		{name: "zero max batches", store: &fakeStore{}, opts: Options{BatchSize: 1, Interval: time.Minute, MaxBatchesPerTick: 0}, errSub: "max batches"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var logs bytes.Buffer
			sweeper := NewSweeper(tt.store, tt.opts, newTestLogger(&logs))
			err := sweeper.Run(context.Background())
			if err == nil {
				t.Fatal("Run() succeeded with invalid options")
			}
			if !strings.Contains(err.Error(), tt.errSub) {
				t.Fatalf("error %q does not contain %q", err, tt.errSub)
			}
		})
	}
}
