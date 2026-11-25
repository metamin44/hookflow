package config

import (
	"math"
	"strings"
	"testing"
	"time"
)

func lookup(values map[string]string) func(string) (string, bool) {
	return func(key string) (string, bool) {
		value, ok := values[key]
		return value, ok
	}
}

func approvedPublisher() Publisher {
	return Publisher{
		BatchSize:      200,
		MaxClaims:      5,
		LeaseDuration:  30 * time.Second,
		PollInterval:   500 * time.Millisecond,
		ReapInterval:   10 * time.Second,
		BackoffBase:    2 * time.Second,
		BackoffCap:     60 * time.Second,
		BackoffMin:     100 * time.Millisecond,
		ConfirmTimeout: 5 * time.Second,
	}
}

func validPublisherEnv() map[string]string {
	return map[string]string{
		EnvPublisherBatchSize:      "250",
		EnvPublisherMaxClaims:      "6",
		EnvPublisherLeaseDuration:  "36s",
		EnvPublisherPollInterval:   "600ms",
		EnvPublisherReapInterval:   "12s",
		EnvPublisherBackoffBase:    "3s",
		EnvPublisherBackoffCap:     "90s",
		EnvPublisherBackoffMin:     "200ms",
		EnvPublisherConfirmTimeout: "4s",
	}
}

func approvedRecovery() Recovery {
	return Recovery{
		Cadence: 60 * time.Second,
		Total:   45 * time.Second,
		Expire: RecoveryPassBudget{
			MaxCandidates: 500,
			MaxDuration:   12 * time.Second,
		},
		Repair: RecoveryPassBudget{
			MaxCandidates: 250,
			MaxDuration:   10 * time.Second,
		},
		ReArm: RecoveryPassBudget{
			MaxCandidates: 250,
			MaxDuration:   10 * time.Second,
		},
		Stale: RecoveryPassBudget{
			MaxCandidates: 100,
			MaxDuration:   8 * time.Second,
		},
		StalePublishedAfter: 5 * time.Minute,
	}
}

func validRecoveryEnv() map[string]string {
	return map[string]string{
		EnvRecoveryCadence:             "90s",
		EnvRecoveryTotalDuration:       "40s",
		EnvRecoveryExpireMaxCandidates: "20",
		EnvRecoveryExpireMaxDuration:   "8s",
		EnvRecoveryRepairMaxCandidates: "21",
		EnvRecoveryRepairMaxDuration:   "7s",
		EnvRecoveryReArmMaxCandidates:  "22",
		EnvRecoveryReArmMaxDuration:    "6s",
		EnvRecoveryStaleMaxCandidates:  "23",
		EnvRecoveryStaleMaxDuration:    "5s",
		EnvRecoveryStalePublishedAfter: "15m",
	}
}

func approvedWorker() DeliveryWorker {
	return DeliveryWorker{
		LeaseDuration:          30 * time.Second,
		HTTPDeadline:           10 * time.Second,
		HandshakeBudget:        2 * time.Second,
		PostProcessBudget:      3 * time.Second,
		ShutdownReserve:        10 * time.Second,
		Concurrency:            24,
		Prefetch:               24,
		PoolSize:               30,
		MaxAttempts:            8,
		RetryHorizon:           12 * time.Hour,
		DecryptFailurePacing:   30 * time.Second,
		SecretResolutionBudget: 2 * time.Second,
		ResponseBodyLimit:      65536,
	}
}

func validWorkerEnv() map[string]string {
	return map[string]string{
		EnvWorkerLeaseDuration:        "36s",
		EnvWorkerHTTPDeadline:         "8s",
		EnvWorkerHandshakeBudget:      "2s",
		EnvWorkerPostProcessBudget:    "4s",
		EnvWorkerShutdownReserve:      "8s",
		EnvWorkerConcurrency:          "20",
		EnvWorkerPrefetch:             "20",
		EnvWorkerPoolSize:             "25",
		EnvWorkerMaxAttempts:          "6",
		EnvWorkerRetryHorizon:         "2h",
		EnvWorkerDecryptFailurePacing: "5m",
		EnvWorkerSecretResolution:     "1s",
		EnvWorkerResponseBodyLimit:    "1024",
	}
}

func assertInvalidRuntimeValue(t *testing.T, key string, integer bool, load func(map[string]string) error) {
	t.Helper()
	cases := []string{"0", "-1", "not-a-number", " \t\n "}
	if integer {
		cases = append(cases, "1.5", "999999999999999999999999999999")
	} else {
		cases = append(cases, "60", "999999999999999999999h")
	}
	for _, value := range cases {
		t.Run(key+"/"+value, func(t *testing.T) {
			err := load(map[string]string{key: value})
			if err == nil || !strings.Contains(err.Error(), key) {
				t.Fatalf("invalid %s=%q error = %v", key, value, err)
			}
		})
	}
}

func TestLoadPublisherDefaultsAndOverrides(t *testing.T) {
	publisher, err := loadPublisher(lookup(map[string]string{}))
	if err != nil {
		t.Fatalf("loadPublisher() defaults error: %v", err)
	}
	if want := approvedPublisher(); publisher != want {
		t.Fatalf("publisher = %+v, want %+v", publisher, want)
	}

	publisher, err = loadPublisher(lookup(validPublisherEnv()))
	if err != nil {
		t.Fatalf("loadPublisher() overrides error: %v", err)
	}
	want := Publisher{
		BatchSize:      250,
		MaxClaims:      6,
		LeaseDuration:  36 * time.Second,
		PollInterval:   600 * time.Millisecond,
		ReapInterval:   12 * time.Second,
		BackoffBase:    3 * time.Second,
		BackoffCap:     90 * time.Second,
		BackoffMin:     200 * time.Millisecond,
		ConfirmTimeout: 4 * time.Second,
	}
	if publisher != want {
		t.Fatalf("publisher = %+v, want %+v", publisher, want)
	}
}

func TestLoadPublisherRejectsInvalidValues(t *testing.T) {
	fields := []struct {
		key     string
		integer bool
	}{
		{EnvPublisherBatchSize, true},
		{EnvPublisherMaxClaims, true},
		{EnvPublisherLeaseDuration, false},
		{EnvPublisherPollInterval, false},
		{EnvPublisherReapInterval, false},
		{EnvPublisherBackoffBase, false},
		{EnvPublisherBackoffCap, false},
		{EnvPublisherBackoffMin, false},
		{EnvPublisherConfirmTimeout, false},
	}
	for _, field := range fields {
		t.Run(field.key, func(t *testing.T) {
			assertInvalidRuntimeValue(t, field.key, field.integer, func(overrides map[string]string) error {
				values := validPublisherEnv()
				for key, value := range overrides {
					values[key] = value
				}
				_, err := loadPublisher(lookup(values))
				return err
			})
		})
	}
}

func TestLoadPublisherRejectsInconsistentTimings(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"confirm exceeds lease sixth", EnvPublisherConfirmTimeout, "36s"},
		{"minimum exceeds base", EnvPublisherBackoffMin, "4s"},
		{"base exceeds cap", EnvPublisherBackoffBase, "91s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := validPublisherEnv()
			values[tc.key] = tc.value
			if _, err := loadPublisher(lookup(values)); err == nil {
				t.Fatalf("inconsistent publisher timing accepted: %s=%s", tc.key, tc.value)
			}
		})
	}
}

func TestLoadersNormalizeSurroundingWhitespace(t *testing.T) {
	for _, value := range []string{"5", " 5 ", " 5", "5 "} {
		t.Run("integer "+value, func(t *testing.T) {
			values := validPublisherEnv()
			values[EnvPublisherBatchSize] = value
			publisher, err := loadPublisher(lookup(values))
			if err != nil {
				t.Fatalf("loadPublisher() error: %v", err)
			}
			if publisher.BatchSize != 5 {
				t.Fatalf("batch size = %d, want 5", publisher.BatchSize)
			}
		})
	}
	for _, value := range []string{"2m", " 2m ", " 2m", "2m "} {
		t.Run("duration "+value, func(t *testing.T) {
			values := validWorkerEnv()
			values[EnvWorkerLeaseDuration] = value
			worker, err := loadDeliveryWorker(lookup(values))
			if err != nil {
				t.Fatalf("loadDeliveryWorker() error: %v", err)
			}
			if worker.LeaseDuration != 2*time.Minute {
				t.Fatalf("lease duration = %s, want 2m", worker.LeaseDuration)
			}
		})
	}
}

func TestPublisherValidate(t *testing.T) {
	valid := approvedPublisher()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid publisher rejected: %v", err)
	}
	cases := []struct {
		name     string
		mutate   func(*Publisher)
		contains string
	}{
		{"batch", func(p *Publisher) { p.BatchSize = 0 }, "batch size"},
		{"claims", func(p *Publisher) { p.MaxClaims = 0 }, "maximum claims"},
		{"lease", func(p *Publisher) { p.LeaseDuration = 0 }, "lease duration"},
		{"poll", func(p *Publisher) { p.PollInterval = 0 }, "poll interval"},
		{"reap", func(p *Publisher) { p.ReapInterval = 0 }, "reap interval"},
		{"reap reaches lease", func(p *Publisher) { p.ReapInterval = p.LeaseDuration }, "shorter than"},
		{"backoff base", func(p *Publisher) { p.BackoffBase = 0 }, "backoff base"},
		{"backoff cap", func(p *Publisher) { p.BackoffCap = 0 }, "backoff cap"},
		{"backoff minimum", func(p *Publisher) { p.BackoffMin = 0 }, "backoff minimum"},
		{"confirm timeout", func(p *Publisher) { p.ConfirmTimeout = 0 }, "confirm timeout"},
		{"confirm exceeds lease sixth", func(p *Publisher) { p.ConfirmTimeout = 5*time.Second + time.Nanosecond }, "one sixth"},
		{"minimum exceeds base", func(p *Publisher) { p.BackoffMin = 2*time.Second + time.Nanosecond }, "minimum"},
		{"base exceeds cap", func(p *Publisher) { p.BackoffBase = 60*time.Second + time.Nanosecond }, "backoff base"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			publisher := valid
			tc.mutate(&publisher)
			err := publisher.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	boundary := valid
	boundary.BackoffMin = boundary.BackoffBase
	boundary.BackoffBase = boundary.BackoffCap
	if err := boundary.Validate(); err != nil {
		t.Fatalf("equal backoff bounds rejected: %v", err)
	}
}

func TestLoadRecoveryDefaultsAndOverrides(t *testing.T) {
	recovery, err := loadRecovery(lookup(map[string]string{}))
	if err != nil {
		t.Fatalf("loadRecovery() defaults error: %v", err)
	}
	if want := approvedRecovery(); recovery != want {
		t.Fatalf("recovery = %+v, want %+v", recovery, want)
	}

	recovery, err = loadRecovery(lookup(validRecoveryEnv()))
	if err != nil {
		t.Fatalf("loadRecovery() overrides error: %v", err)
	}
	want := Recovery{
		Cadence: 90 * time.Second,
		Total:   40 * time.Second,
		Expire: RecoveryPassBudget{
			MaxCandidates: 20,
			MaxDuration:   8 * time.Second,
		},
		Repair: RecoveryPassBudget{
			MaxCandidates: 21,
			MaxDuration:   7 * time.Second,
		},
		ReArm: RecoveryPassBudget{
			MaxCandidates: 22,
			MaxDuration:   6 * time.Second,
		},
		Stale: RecoveryPassBudget{
			MaxCandidates: 23,
			MaxDuration:   5 * time.Second,
		},
		StalePublishedAfter: 15 * time.Minute,
	}
	if recovery != want {
		t.Fatalf("recovery = %+v, want %+v", recovery, want)
	}
}

func TestLoadRecoveryRejectsInvalidValues(t *testing.T) {
	fields := []struct {
		key     string
		integer bool
	}{
		{EnvRecoveryCadence, false},
		{EnvRecoveryTotalDuration, false},
		{EnvRecoveryExpireMaxCandidates, true},
		{EnvRecoveryExpireMaxDuration, false},
		{EnvRecoveryRepairMaxCandidates, true},
		{EnvRecoveryRepairMaxDuration, false},
		{EnvRecoveryReArmMaxCandidates, true},
		{EnvRecoveryReArmMaxDuration, false},
		{EnvRecoveryStaleMaxCandidates, true},
		{EnvRecoveryStaleMaxDuration, false},
		{EnvRecoveryStalePublishedAfter, false},
	}
	for _, field := range fields {
		t.Run(field.key, func(t *testing.T) {
			assertInvalidRuntimeValue(t, field.key, field.integer, func(overrides map[string]string) error {
				values := validRecoveryEnv()
				for key, value := range overrides {
					values[key] = value
				}
				_, err := loadRecovery(lookup(values))
				return err
			})
		})
	}
}

func TestLoadRecoveryRejectsInconsistentBudget(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"pass durations exceed total", EnvRecoveryTotalDuration, "3s"},
		{"cadence equals total", EnvRecoveryCadence, "40s"},
		{"cadence below total", EnvRecoveryCadence, "39s"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := validRecoveryEnv()
			values[tc.key] = tc.value
			if _, err := loadRecovery(lookup(values)); err == nil {
				t.Fatalf("inconsistent recovery configuration accepted: %s=%s", tc.key, tc.value)
			}
		})
	}
}

func TestRecoveryValidateBudget(t *testing.T) {
	build := func(total time.Duration, pass time.Duration) Recovery {
		recovery := approvedRecovery()
		recovery.Cadence = total + time.Second
		recovery.Total = total
		recovery.Expire.MaxDuration = pass
		recovery.Repair.MaxDuration = pass
		recovery.ReArm.MaxDuration = pass
		recovery.Stale.MaxDuration = pass
		return recovery
	}

	if err := build(5*time.Second, time.Second).Validate(); err != nil {
		t.Fatalf("budget below total rejected: %v", err)
	}
	if err := build(4*time.Second, time.Second).Validate(); err != nil {
		t.Fatalf("budget equal to total rejected: %v", err)
	}
	if err := build(3*time.Second, time.Second).Validate(); err == nil {
		t.Fatal("budget above total accepted")
	}

	overflow := build(time.Duration(1<<63-1), time.Duration(1<<62))
	if err := overflow.Validate(); err == nil {
		t.Fatal("overflowing budget accepted")
	}
}

func TestValidateDurationBudget(t *testing.T) {
	if err := ValidateDurationBudget(4*time.Second, time.Second, time.Second, time.Second, time.Second); err != nil {
		t.Fatalf("equal budget rejected: %v", err)
	}
	if err := ValidateDurationBudget(3*time.Second, time.Second, time.Second, time.Second, time.Second); err == nil {
		t.Fatal("over-budget accepted")
	}
	if err := ValidateDurationBudget(0, time.Second); err == nil {
		t.Fatal("zero total accepted")
	}
	if err := ValidateDurationBudget(time.Second, 0); err == nil {
		t.Fatal("zero duration accepted")
	}
	if err := ValidateDurationBudget(time.Second, -time.Second); err == nil {
		t.Fatal("negative duration accepted")
	}
	const maximum = time.Duration(1<<63 - 1)
	if err := ValidateDurationBudget(maximum, maximum, time.Nanosecond); err == nil {
		t.Fatal("overflow accepted")
	}
}

func TestRecoveryValidate(t *testing.T) {
	valid := approvedRecovery()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid recovery rejected: %v", err)
	}
	cases := []struct {
		name     string
		mutate   func(*Recovery)
		contains string
	}{
		{"cadence", func(r *Recovery) { r.Cadence = 0 }, "cadence"},
		{"total", func(r *Recovery) { r.Total = 0 }, "total duration"},
		{"expire candidates", func(r *Recovery) { r.Expire.MaxCandidates = 0 }, "expire pass"},
		{"expire duration", func(r *Recovery) { r.Expire.MaxDuration = 0 }, "expire pass"},
		{"repair candidates", func(r *Recovery) { r.Repair.MaxCandidates = 0 }, "repair pass"},
		{"repair duration", func(r *Recovery) { r.Repair.MaxDuration = 0 }, "repair pass"},
		{"re-arm candidates", func(r *Recovery) { r.ReArm.MaxCandidates = 0 }, "re-arm pass"},
		{"re-arm duration", func(r *Recovery) { r.ReArm.MaxDuration = 0 }, "re-arm pass"},
		{"stale candidates", func(r *Recovery) { r.Stale.MaxCandidates = 0 }, "stale pass"},
		{"stale duration", func(r *Recovery) { r.Stale.MaxDuration = 0 }, "stale pass"},
		{"sum exceeds total", func(r *Recovery) { r.Total = 39 * time.Second }, "pass durations"},
		{"cadence equals total", func(r *Recovery) { r.Cadence = r.Total }, "cadence"},
		{"cadence below total", func(r *Recovery) { r.Cadence = r.Total - time.Nanosecond }, "cadence"},
		{"stale threshold", func(r *Recovery) { r.StalePublishedAfter = 0 }, "stale published threshold"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			recovery := valid
			tc.mutate(&recovery)
			err := recovery.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}
}

func TestLoadDeliveryWorkerDefaultsAndOverrides(t *testing.T) {
	worker, err := loadDeliveryWorker(lookup(map[string]string{}))
	if err != nil {
		t.Fatalf("loadDeliveryWorker() defaults error: %v", err)
	}
	if want := approvedWorker(); worker != want {
		t.Fatalf("worker = %+v, want %+v", worker, want)
	}

	worker, err = loadDeliveryWorker(lookup(validWorkerEnv()))
	if err != nil {
		t.Fatalf("loadDeliveryWorker() overrides error: %v", err)
	}
	want := DeliveryWorker{
		LeaseDuration:          36 * time.Second,
		HTTPDeadline:           8 * time.Second,
		HandshakeBudget:        2 * time.Second,
		PostProcessBudget:      4 * time.Second,
		ShutdownReserve:        8 * time.Second,
		Concurrency:            20,
		Prefetch:               20,
		PoolSize:               25,
		MaxAttempts:            6,
		RetryHorizon:           2 * time.Hour,
		DecryptFailurePacing:   5 * time.Minute,
		SecretResolutionBudget: time.Second,
		ResponseBodyLimit:      1024,
	}
	if worker != want {
		t.Fatalf("worker = %+v, want %+v", worker, want)
	}
}

func TestLoadDeliveryWorkerRejectsInvalidValues(t *testing.T) {
	fields := []struct {
		key     string
		integer bool
	}{
		{EnvWorkerLeaseDuration, false},
		{EnvWorkerHTTPDeadline, false},
		{EnvWorkerHandshakeBudget, false},
		{EnvWorkerPostProcessBudget, false},
		{EnvWorkerShutdownReserve, false},
		{EnvWorkerConcurrency, true},
		{EnvWorkerPrefetch, true},
		{EnvWorkerPoolSize, true},
		{EnvWorkerMaxAttempts, true},
		{EnvWorkerRetryHorizon, false},
		{EnvWorkerDecryptFailurePacing, false},
		{EnvWorkerSecretResolution, false},
		{EnvWorkerResponseBodyLimit, true},
	}
	for _, field := range fields {
		t.Run(field.key, func(t *testing.T) {
			assertInvalidRuntimeValue(t, field.key, field.integer, func(overrides map[string]string) error {
				values := validWorkerEnv()
				for key, value := range overrides {
					values[key] = value
				}
				_, err := loadDeliveryWorker(lookup(values))
				return err
			})
		})
	}
}

func TestLoadDeliveryWorkerRejectsInconsistentValues(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{"prefetch differs", EnvWorkerPrefetch, "21"},
		{"pool below concurrency", EnvWorkerPoolSize, "19"},
		{"lease equals reserved budgets", EnvWorkerLeaseDuration, "23s"},
		{"attempts exceed ladder", EnvWorkerMaxAttempts, "9"},
		{"retry horizon exceeds cap", EnvWorkerRetryHorizon, "12h1ns"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			values := validWorkerEnv()
			values[tc.key] = tc.value
			if _, err := loadDeliveryWorker(lookup(values)); err == nil {
				t.Fatalf("inconsistent worker configuration accepted: %s=%s", tc.key, tc.value)
			}
		})
	}
}

func TestDeliveryWorkerValidate(t *testing.T) {
	valid := approvedWorker()
	if err := valid.Validate(); err != nil {
		t.Fatalf("valid worker rejected: %v", err)
	}
	cases := []struct {
		name     string
		mutate   func(*DeliveryWorker)
		contains string
	}{
		{"lease", func(w *DeliveryWorker) { w.LeaseDuration = 0 }, "lease duration"},
		{"HTTP deadline", func(w *DeliveryWorker) { w.HTTPDeadline = 0 }, "HTTP deadline"},
		{"handshake", func(w *DeliveryWorker) { w.HandshakeBudget = 0 }, "handshake budget"},
		{"post-process", func(w *DeliveryWorker) { w.PostProcessBudget = 0 }, "post-process budget"},
		{"shutdown", func(w *DeliveryWorker) { w.ShutdownReserve = 0 }, "shutdown reserve"},
		{"concurrency", func(w *DeliveryWorker) { w.Concurrency = 0 }, "concurrency"},
		{"prefetch", func(w *DeliveryWorker) { w.Prefetch = 0 }, "prefetch"},
		{"pool", func(w *DeliveryWorker) { w.PoolSize = 0 }, "pool size"},
		{"attempts", func(w *DeliveryWorker) { w.MaxAttempts = 0 }, "maximum attempts"},
		{"retry horizon", func(w *DeliveryWorker) { w.RetryHorizon = 0 }, "retry horizon"},
		{"pacing", func(w *DeliveryWorker) { w.DecryptFailurePacing = 0 }, "pacing"},
		{"secret resolution", func(w *DeliveryWorker) { w.SecretResolutionBudget = 0 }, "secret resolution"},
		{"body limit", func(w *DeliveryWorker) { w.ResponseBodyLimit = 0 }, "response body limit"},
		{"prefetch differs", func(w *DeliveryWorker) { w.Prefetch++ }, "prefetch"},
		{"pool below concurrency", func(w *DeliveryWorker) { w.PoolSize = w.Concurrency - 1 }, "pool size"},
		{"pool exceeds pgx limit", func(w *DeliveryWorker) { w.PoolSize = math.MaxInt32 + 1 }, "32-bit"},
		{"prefetch exceeds AMQP limit", func(w *DeliveryWorker) { w.Concurrency = 65536; w.Prefetch = 65536; w.PoolSize = 65536 }, "65535"},
		{"lease equals reserved", func(w *DeliveryWorker) { w.LeaseDuration = 27 * time.Second }, "lease duration"},
		{"attempts exceed ladder", func(w *DeliveryWorker) { w.MaxAttempts = 9 }, "maximum attempts"},
		{"retry horizon exceeds cap", func(w *DeliveryWorker) { w.RetryHorizon = 12*time.Hour + time.Nanosecond }, "retry horizon"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			worker := valid
			tc.mutate(&worker)
			err := worker.Validate()
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("Validate() error = %v", err)
			}
		})
	}

	boundary := valid
	boundary.LeaseDuration = 27*time.Second + time.Nanosecond
	boundary.PoolSize = boundary.Concurrency
	boundary.MaxAttempts = 8
	boundary.RetryHorizon = 12 * time.Hour
	if err := boundary.Validate(); err != nil {
		t.Fatalf("valid worker boundaries rejected: %v", err)
	}

	overflow := valid
	overflow.HTTPDeadline = time.Duration(1<<63 - 1)
	overflow.HandshakeBudget = time.Nanosecond
	if err := overflow.Validate(); err == nil || !strings.Contains(err.Error(), "overflows") {
		t.Fatalf("overflowing worker duration budget error = %v", err)
	}
}
