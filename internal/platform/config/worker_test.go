package config

import (
	"strings"
	"testing"
	"time"
)

func TestLoadWorkerDefaults(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
	}{
		{name: "unset", env: map[string]string{}},
		{
			name: "empty",
			env: map[string]string{
				EnvIdempotencySweepBatchSize:         "",
				EnvIdempotencySweepInterval:          "",
				EnvIdempotencySweepMaxBatchesPerTick: "",
			},
		},
		{
			name: "whitespace",
			env: map[string]string{
				EnvIdempotencySweepBatchSize:         "   ",
				EnvIdempotencySweepInterval:          "\t",
				EnvIdempotencySweepMaxBatchesPerTick: " ",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg, err := loadWorker(lookupFrom(tt.env))
			if err != nil {
				t.Fatalf("loadWorker() error: %v", err)
			}
			if cfg.IdempotencySweep.BatchSize != DefaultIdempotencySweepBatchSize {
				t.Fatalf("batch size = %d, want %d", cfg.IdempotencySweep.BatchSize, DefaultIdempotencySweepBatchSize)
			}
			if cfg.IdempotencySweep.Interval != DefaultIdempotencySweepInterval {
				t.Fatalf("interval = %s, want %s", cfg.IdempotencySweep.Interval, DefaultIdempotencySweepInterval)
			}
			if cfg.IdempotencySweep.MaxBatchesPerTick != DefaultIdempotencySweepMaxBatchesPerTick {
				t.Fatalf("max batches = %d, want %d", cfg.IdempotencySweep.MaxBatchesPerTick, DefaultIdempotencySweepMaxBatchesPerTick)
			}
		})
	}
}

func TestLoadWorkerCustom(t *testing.T) {
	env := map[string]string{
		EnvIdempotencySweepBatchSize:         " 250 ",
		EnvIdempotencySweepInterval:          " 30s ",
		EnvIdempotencySweepMaxBatchesPerTick: "2",
	}

	cfg, err := loadWorker(lookupFrom(env))
	if err != nil {
		t.Fatalf("loadWorker() error: %v", err)
	}
	if cfg.IdempotencySweep.BatchSize != 250 {
		t.Fatalf("batch size = %d", cfg.IdempotencySweep.BatchSize)
	}
	if cfg.IdempotencySweep.Interval != 30*time.Second {
		t.Fatalf("interval = %s", cfg.IdempotencySweep.Interval)
	}
	if cfg.IdempotencySweep.MaxBatchesPerTick != 2 {
		t.Fatalf("max batches = %d", cfg.IdempotencySweep.MaxBatchesPerTick)
	}
}

func TestLoadWorkerBoundaries(t *testing.T) {
	env := map[string]string{
		EnvIdempotencySweepBatchSize:         "1",
		EnvIdempotencySweepInterval:          "1ns",
		EnvIdempotencySweepMaxBatchesPerTick: "1",
	}

	cfg, err := loadWorker(lookupFrom(env))
	if err != nil {
		t.Fatalf("loadWorker() rejected valid boundary values: %v", err)
	}
	if cfg.IdempotencySweep.BatchSize != 1 || cfg.IdempotencySweep.MaxBatchesPerTick != 1 {
		t.Fatalf("unexpected boundary configuration: %+v", cfg.IdempotencySweep)
	}
	if cfg.IdempotencySweep.Interval != time.Nanosecond {
		t.Fatalf("interval = %s", cfg.IdempotencySweep.Interval)
	}
}

func TestLoadWorkerInvalid(t *testing.T) {
	tests := []struct {
		name string
		key  string
		env  map[string]string
	}{
		{name: "batch zero", key: EnvIdempotencySweepBatchSize, env: map[string]string{EnvIdempotencySweepBatchSize: "0"}},
		{name: "batch negative", key: EnvIdempotencySweepBatchSize, env: map[string]string{EnvIdempotencySweepBatchSize: "-1"}},
		{name: "batch text", key: EnvIdempotencySweepBatchSize, env: map[string]string{EnvIdempotencySweepBatchSize: "many"}},
		{name: "batch fraction", key: EnvIdempotencySweepBatchSize, env: map[string]string{EnvIdempotencySweepBatchSize: "1.5"}},
		{name: "interval zero", key: EnvIdempotencySweepInterval, env: map[string]string{EnvIdempotencySweepInterval: "0s"}},
		{name: "interval negative", key: EnvIdempotencySweepInterval, env: map[string]string{EnvIdempotencySweepInterval: "-10s"}},
		{name: "interval text", key: EnvIdempotencySweepInterval, env: map[string]string{EnvIdempotencySweepInterval: "soon"}},
		{name: "interval unitless", key: EnvIdempotencySweepInterval, env: map[string]string{EnvIdempotencySweepInterval: "60"}},
		{name: "max batches zero", key: EnvIdempotencySweepMaxBatchesPerTick, env: map[string]string{EnvIdempotencySweepMaxBatchesPerTick: "0"}},
		{name: "max batches negative", key: EnvIdempotencySweepMaxBatchesPerTick, env: map[string]string{EnvIdempotencySweepMaxBatchesPerTick: "-3"}},
		{name: "max batches text", key: EnvIdempotencySweepMaxBatchesPerTick, env: map[string]string{EnvIdempotencySweepMaxBatchesPerTick: "five"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := loadWorker(lookupFrom(tt.env))
			if err == nil {
				t.Fatal("loadWorker() succeeded with an invalid value")
			}
			if !strings.Contains(err.Error(), tt.key) {
				t.Fatalf("error does not identify %s: %v", tt.key, err)
			}
		})
	}
}

func TestLoadWorkerFromEnvironment(t *testing.T) {
	t.Setenv(EnvIdempotencySweepBatchSize, "42")
	t.Setenv(EnvIdempotencySweepInterval, "2m")
	t.Setenv(EnvIdempotencySweepMaxBatchesPerTick, "3")

	cfg, err := LoadWorker()
	if err != nil {
		t.Fatalf("LoadWorker() error: %v", err)
	}
	if cfg.IdempotencySweep.BatchSize != 42 ||
		cfg.IdempotencySweep.Interval != 2*time.Minute ||
		cfg.IdempotencySweep.MaxBatchesPerTick != 3 {
		t.Fatalf("unexpected worker configuration: %+v", cfg.IdempotencySweep)
	}
}

func TestWorkerConfigurationIsProcessLocal(t *testing.T) {
	readKeys := make(map[string]bool)
	getenv := func(key string) (string, bool) {
		readKeys[key] = true
		return lookupFrom(map[string]string{})(key)
	}

	if _, err := loadWorker(getenv); err != nil {
		t.Fatalf("loadWorker() error: %v", err)
	}

	for _, key := range []string{
		EnvIdempotencySweepBatchSize,
		EnvIdempotencySweepInterval,
		EnvIdempotencySweepMaxBatchesPerTick,
	} {
		if !readKeys[key] {
			t.Fatalf("worker configuration did not read %s", key)
		}
	}

	for key := range readKeys {
		switch key {
		case EnvIdempotencySweepBatchSize, EnvIdempotencySweepInterval, EnvIdempotencySweepMaxBatchesPerTick:
		default:
			t.Fatalf("worker configuration read unrelated variable %s", key)
		}
	}
}

func TestDatabaseConfigurationDoesNotReadWorkerKeys(t *testing.T) {
	getenv := func(key string) (string, bool) {
		if strings.HasPrefix(key, "HOOKFLOW_IDEMPOTENCY_SWEEP_") {
			t.Fatalf("database configuration read worker variable %s", key)
		}
		value, ok := baseEnv()[key]
		return value, ok
	}

	if _, err := load(getenv); err != nil {
		t.Fatalf("load() error: %v", err)
	}
}

func TestWorkerConfigurationErrorDoesNotLeakDatabasePassword(t *testing.T) {
	env := baseEnv()
	env[EnvIdempotencySweepBatchSize] = "invalid"

	getenv := func(key string) (string, bool) {
		value, ok := env[key]
		return value, ok
	}

	_, err := loadWorker(getenv)
	if err == nil {
		t.Fatal("loadWorker() succeeded with an invalid value")
	}
	if strings.Contains(err.Error(), env[EnvDBPassword]) {
		t.Fatal("worker configuration error contains the database password")
	}
}
