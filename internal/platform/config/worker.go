package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Environment variable names approved for the U43 idempotency sweep.
const (
	EnvIdempotencySweepBatchSize         = "HOOKFLOW_IDEMPOTENCY_SWEEP_BATCH_SIZE"
	EnvIdempotencySweepInterval          = "HOOKFLOW_IDEMPOTENCY_SWEEP_INTERVAL"
	EnvIdempotencySweepMaxBatchesPerTick = "HOOKFLOW_IDEMPOTENCY_SWEEP_MAX_BATCHES_PER_TICK"
)

// Approved U43 defaults. They are operational defaults, not architecture values
// invented here: database-schema.md §4.13.4 fixes batch 500, interval 1 minute
// and at most 5 batches per tick.
const (
	DefaultIdempotencySweepBatchSize         = 500
	DefaultIdempotencySweepInterval          = time.Minute
	DefaultIdempotencySweepMaxBatchesPerTick = 5
)

// Worker contains configuration for the worker process.
//
// Only the approved U43 idempotency sweep is represented here. Delivery claim,
// lease and retry values are declared separately as DeliveryWorker in
// runtime.go, where they are required explicit inputs with no defaults, so they
// cannot be activated from unresolved operational values.
type Worker struct {
	IdempotencySweep IdempotencySweep
}

// IdempotencySweep configures the worker-hosted expired-record sweep.
type IdempotencySweep struct {
	BatchSize         int
	Interval          time.Duration
	MaxBatchesPerTick int
}

// LoadWorker reads and validates worker configuration from the process
// environment. Unset or empty values use the approved U43 defaults.
func LoadWorker() (Worker, error) {
	return loadWorker(os.LookupEnv)
}

func loadWorker(getenv func(string) (string, bool)) (Worker, error) {
	batchSize, err := positiveInt(getenv, EnvIdempotencySweepBatchSize, DefaultIdempotencySweepBatchSize)
	if err != nil {
		return Worker{}, err
	}

	interval, err := positiveDuration(getenv, EnvIdempotencySweepInterval, DefaultIdempotencySweepInterval)
	if err != nil {
		return Worker{}, err
	}

	maxBatches, err := positiveInt(getenv, EnvIdempotencySweepMaxBatchesPerTick, DefaultIdempotencySweepMaxBatchesPerTick)
	if err != nil {
		return Worker{}, err
	}

	return Worker{
		IdempotencySweep: IdempotencySweep{
			BatchSize:         batchSize,
			Interval:          interval,
			MaxBatchesPerTick: maxBatches,
		},
	}, nil
}

func positiveInt(getenv func(string) (string, bool), key string, fallback int) (int, error) {
	raw, _ := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}

	parsed, err := strconv.Atoi(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func positiveDuration(getenv func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	raw, _ := getenv(key)
	value := strings.TrimSpace(raw)
	if value == "" {
		return fallback, nil
	}

	parsed, err := time.ParseDuration(value)
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration such as 1m", key)
	}
	return parsed, nil
}
