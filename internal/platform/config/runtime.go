package config

import (
	"errors"
	"fmt"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

// Runtime component loaders use owner-approved defaults while allowing every
// operational value to be overridden explicitly. Missing values use the
// approved defaults; values that are present but invalid fail activation.

// Publisher environment variable names.
const (
	EnvPublisherBatchSize      = "HOOKFLOW_PUBLISHER_BATCH_SIZE"
	EnvPublisherMaxClaims      = "HOOKFLOW_PUBLISHER_MAX_CLAIMS"
	EnvPublisherLeaseDuration  = "HOOKFLOW_PUBLISHER_LEASE_DURATION"
	EnvPublisherPollInterval   = "HOOKFLOW_PUBLISHER_POLL_INTERVAL"
	EnvPublisherReapInterval   = "HOOKFLOW_PUBLISHER_REAP_INTERVAL"
	EnvPublisherBackoffBase    = "HOOKFLOW_PUBLISHER_BACKOFF_BASE"
	EnvPublisherBackoffCap     = "HOOKFLOW_PUBLISHER_BACKOFF_CAP"
	EnvPublisherBackoffMin     = "HOOKFLOW_PUBLISHER_BACKOFF_MIN"
	EnvPublisherConfirmTimeout = "HOOKFLOW_PUBLISHER_CONFIRM_TIMEOUT"
)

// Publisher holds the approved publisher accounting, polling, and backoff
// parameters.
type Publisher struct {
	BatchSize      int
	MaxClaims      int
	LeaseDuration  time.Duration
	PollInterval   time.Duration
	ReapInterval   time.Duration
	BackoffBase    time.Duration
	BackoffCap     time.Duration
	BackoffMin     time.Duration
	ConfirmTimeout time.Duration
}

// Validate reports whether every publisher value is positive and the timing
// invariants required by the publisher loop hold.
func (p Publisher) Validate() error {
	if p.BatchSize <= 0 {
		return errors.New("publisher batch size must be positive")
	}
	if p.MaxClaims <= 0 {
		return errors.New("publisher maximum claims must be positive")
	}
	if p.LeaseDuration <= 0 {
		return errors.New("publisher lease duration must be positive")
	}
	if p.PollInterval <= 0 {
		return errors.New("publisher poll interval must be positive")
	}
	if p.ReapInterval <= 0 {
		return errors.New("publisher reap interval must be positive")
	}
	if p.ReapInterval >= p.LeaseDuration {
		return errors.New("publisher reap interval must be shorter than the lease duration")
	}
	if p.BackoffBase <= 0 {
		return errors.New("publisher backoff base must be positive")
	}
	if p.BackoffCap <= 0 {
		return errors.New("publisher backoff cap must be positive")
	}
	if p.BackoffMin <= 0 {
		return errors.New("publisher backoff minimum must be positive")
	}
	if p.ConfirmTimeout <= 0 {
		return errors.New("publisher confirm timeout must be positive")
	}
	if p.ConfirmTimeout > p.LeaseDuration/6 {
		return errors.New("publisher confirm timeout must not exceed one sixth of the lease duration")
	}
	if p.BackoffMin > p.BackoffBase {
		return errors.New("publisher backoff minimum must not exceed the backoff base")
	}
	if p.BackoffBase > p.BackoffCap {
		return errors.New("publisher backoff base must not exceed the backoff cap")
	}
	return nil
}

// LoadPublisher reads and validates the publisher configuration from the
// process environment.
func LoadPublisher() (Publisher, error) {
	return loadPublisher(os.LookupEnv)
}

func loadPublisher(getenv func(string) (string, bool)) (Publisher, error) {
	publisher := Publisher{
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

	var err error
	publisher.BatchSize, err = positiveIntWithDefault(getenv, EnvPublisherBatchSize, publisher.BatchSize)
	if err != nil {
		return Publisher{}, err
	}
	publisher.MaxClaims, err = positiveIntWithDefault(getenv, EnvPublisherMaxClaims, publisher.MaxClaims)
	if err != nil {
		return Publisher{}, err
	}
	publisher.LeaseDuration, err = positiveDurationWithDefault(getenv, EnvPublisherLeaseDuration, publisher.LeaseDuration)
	if err != nil {
		return Publisher{}, err
	}
	publisher.PollInterval, err = positiveDurationWithDefault(getenv, EnvPublisherPollInterval, publisher.PollInterval)
	if err != nil {
		return Publisher{}, err
	}
	publisher.ReapInterval, err = positiveDurationWithDefault(getenv, EnvPublisherReapInterval, publisher.ReapInterval)
	if err != nil {
		return Publisher{}, err
	}
	publisher.BackoffBase, err = positiveDurationWithDefault(getenv, EnvPublisherBackoffBase, publisher.BackoffBase)
	if err != nil {
		return Publisher{}, err
	}
	publisher.BackoffCap, err = positiveDurationWithDefault(getenv, EnvPublisherBackoffCap, publisher.BackoffCap)
	if err != nil {
		return Publisher{}, err
	}
	publisher.BackoffMin, err = positiveDurationWithDefault(getenv, EnvPublisherBackoffMin, publisher.BackoffMin)
	if err != nil {
		return Publisher{}, err
	}
	publisher.ConfirmTimeout, err = positiveDurationWithDefault(getenv, EnvPublisherConfirmTimeout, publisher.ConfirmTimeout)
	if err != nil {
		return Publisher{}, err
	}

	if err := publisher.Validate(); err != nil {
		return Publisher{}, fmt.Errorf("invalid publisher configuration: %w", err)
	}
	return publisher, nil
}

// Recovery environment variable names.
const (
	EnvRecoveryCadence             = "HOOKFLOW_RECOVERY_CADENCE"
	EnvRecoveryTotalDuration       = "HOOKFLOW_RECOVERY_TOTAL_DURATION"
	EnvRecoveryExpireMaxCandidates = "HOOKFLOW_RECOVERY_EXPIRE_MAX_CANDIDATES"
	EnvRecoveryExpireMaxDuration   = "HOOKFLOW_RECOVERY_EXPIRE_MAX_DURATION"
	EnvRecoveryRepairMaxCandidates = "HOOKFLOW_RECOVERY_REPAIR_MAX_CANDIDATES"
	EnvRecoveryRepairMaxDuration   = "HOOKFLOW_RECOVERY_REPAIR_MAX_DURATION"
	EnvRecoveryReArmMaxCandidates  = "HOOKFLOW_RECOVERY_REARM_MAX_CANDIDATES"
	EnvRecoveryReArmMaxDuration    = "HOOKFLOW_RECOVERY_REARM_MAX_DURATION"
	EnvRecoveryStaleMaxCandidates  = "HOOKFLOW_RECOVERY_STALE_MAX_CANDIDATES"
	EnvRecoveryStaleMaxDuration    = "HOOKFLOW_RECOVERY_STALE_MAX_DURATION"
	EnvRecoveryStalePublishedAfter = "HOOKFLOW_RECOVERY_STALE_PUBLISHED_AFTER"
)

// RecoveryPassBudget is the reserved candidate and time capacity for one
// Recovery Sweep duty.
type RecoveryPassBudget struct {
	MaxCandidates int
	MaxDuration   time.Duration
}

// Validate reports whether the pass budget is usable.
func (b RecoveryPassBudget) Validate() error {
	if b.MaxCandidates <= 0 {
		return errors.New("recovery pass maximum candidates must be positive")
	}
	if b.MaxDuration <= 0 {
		return errors.New("recovery pass maximum duration must be positive")
	}
	return nil
}

// ValidateDurationBudget reports whether total is positive, every duration is
// positive, and their sum does not exceed total. It is overflow-safe and is the
// single invariant shared by configuration validation and the Recovery Sweep
// constructor.
func ValidateDurationBudget(total time.Duration, durations ...time.Duration) error {
	if total <= 0 {
		return errors.New("total duration must be positive")
	}
	sum, err := positiveDurationSum(durations...)
	if err != nil {
		return err
	}
	if sum > total {
		return errors.New("duration budget exceeds the total duration")
	}
	return nil
}

// Recovery holds the approved Recovery Sweep cadence and budgets. Each duty
// receives a reserved window inside the total invocation deadline.
type Recovery struct {
	Cadence             time.Duration
	Total               time.Duration
	Expire              RecoveryPassBudget
	Repair              RecoveryPassBudget
	ReArm               RecoveryPassBudget
	Stale               RecoveryPassBudget
	StalePublishedAfter time.Duration
}

// Validate reports whether every recovery value is positive, the cadence is
// longer than an invocation, and all reserved pass windows fit inside the total
// invocation deadline.
func (r Recovery) Validate() error {
	if r.Cadence <= 0 {
		return errors.New("recovery cadence must be positive")
	}
	if r.Total <= 0 {
		return errors.New("recovery total duration must be positive")
	}
	passes := []struct {
		name   string
		budget RecoveryPassBudget
	}{
		{"expire", r.Expire},
		{"repair", r.Repair},
		{"re-arm", r.ReArm},
		{"stale", r.Stale},
	}
	for _, pass := range passes {
		if err := pass.budget.Validate(); err != nil {
			return fmt.Errorf("%s pass: %w", pass.name, err)
		}
	}
	if err := ValidateDurationBudget(r.Total,
		r.Expire.MaxDuration, r.Repair.MaxDuration, r.ReArm.MaxDuration, r.Stale.MaxDuration,
	); err != nil {
		return fmt.Errorf("recovery pass durations: %w", err)
	}
	if r.StalePublishedAfter <= 0 {
		return errors.New("recovery stale published threshold must be positive")
	}
	if r.Cadence <= r.Total {
		return errors.New("recovery cadence must be greater than the total duration")
	}
	return nil
}

// LoadRecovery reads and validates the Recovery Sweep configuration from the
// process environment.
func LoadRecovery() (Recovery, error) {
	return loadRecovery(os.LookupEnv)
}

func loadRecovery(getenv func(string) (string, bool)) (Recovery, error) {
	recovery := Recovery{
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

	var err error
	recovery.Cadence, err = positiveDurationWithDefault(getenv, EnvRecoveryCadence, recovery.Cadence)
	if err != nil {
		return Recovery{}, err
	}
	recovery.Total, err = positiveDurationWithDefault(getenv, EnvRecoveryTotalDuration, recovery.Total)
	if err != nil {
		return Recovery{}, err
	}
	recovery.Expire, err = loadRecoveryPass(getenv, EnvRecoveryExpireMaxCandidates, EnvRecoveryExpireMaxDuration, recovery.Expire)
	if err != nil {
		return Recovery{}, err
	}
	recovery.Repair, err = loadRecoveryPass(getenv, EnvRecoveryRepairMaxCandidates, EnvRecoveryRepairMaxDuration, recovery.Repair)
	if err != nil {
		return Recovery{}, err
	}
	recovery.ReArm, err = loadRecoveryPass(getenv, EnvRecoveryReArmMaxCandidates, EnvRecoveryReArmMaxDuration, recovery.ReArm)
	if err != nil {
		return Recovery{}, err
	}
	recovery.Stale, err = loadRecoveryPass(getenv, EnvRecoveryStaleMaxCandidates, EnvRecoveryStaleMaxDuration, recovery.Stale)
	if err != nil {
		return Recovery{}, err
	}
	recovery.StalePublishedAfter, err = positiveDurationWithDefault(getenv, EnvRecoveryStalePublishedAfter, recovery.StalePublishedAfter)
	if err != nil {
		return Recovery{}, err
	}

	if err := recovery.Validate(); err != nil {
		return Recovery{}, fmt.Errorf("invalid recovery configuration: %w", err)
	}
	return recovery, nil
}

func loadRecoveryPass(getenv func(string) (string, bool), candidatesKey, durationKey string, defaults RecoveryPassBudget) (RecoveryPassBudget, error) {
	candidates, err := positiveIntWithDefault(getenv, candidatesKey, defaults.MaxCandidates)
	if err != nil {
		return RecoveryPassBudget{}, err
	}
	duration, err := positiveDurationWithDefault(getenv, durationKey, defaults.MaxDuration)
	if err != nil {
		return RecoveryPassBudget{}, err
	}
	return RecoveryPassBudget{MaxCandidates: candidates, MaxDuration: duration}, nil
}

// Delivery worker environment variable names.
const (
	EnvWorkerLeaseDuration        = "HOOKFLOW_WORKER_LEASE_DURATION"
	EnvWorkerHTTPDeadline         = "HOOKFLOW_WORKER_HTTP_DEADLINE"
	EnvWorkerHandshakeBudget      = "HOOKFLOW_WORKER_HANDSHAKE_BUDGET"
	EnvWorkerPostProcessBudget    = "HOOKFLOW_WORKER_POST_PROCESS_BUDGET"
	EnvWorkerShutdownReserve      = "HOOKFLOW_WORKER_SHUTDOWN_RESERVE"
	EnvWorkerConcurrency          = "HOOKFLOW_WORKER_CONCURRENCY"
	EnvWorkerPrefetch             = "HOOKFLOW_WORKER_PREFETCH"
	EnvWorkerPoolSize             = "HOOKFLOW_WORKER_POOL_SIZE"
	EnvWorkerMaxAttempts          = "HOOKFLOW_WORKER_MAX_ATTEMPTS"
	EnvWorkerRetryHorizon         = "HOOKFLOW_WORKER_RETRY_HORIZON"
	EnvWorkerDecryptFailurePacing = "HOOKFLOW_WORKER_DECRYPT_FAILURE_PACING"
	EnvWorkerSecretResolution     = "HOOKFLOW_WORKER_SECRET_RESOLUTION_BUDGET" //nolint:gosec // Environment variable name, not a credential.
	EnvWorkerResponseBodyLimit    = "HOOKFLOW_WORKER_RESPONSE_BODY_LIMIT"
)

const (
	maxWorkerAttempts     = 8
	maxWorkerRetryHorizon = 12 * time.Hour
)

// DeliveryWorker holds the approved B3/B4/B5 database-side worker parameters.
type DeliveryWorker struct {
	LeaseDuration          time.Duration
	HTTPDeadline           time.Duration
	HandshakeBudget        time.Duration
	PostProcessBudget      time.Duration
	ShutdownReserve        time.Duration
	Concurrency            int
	Prefetch               int
	PoolSize               int
	MaxAttempts            int
	RetryHorizon           time.Duration
	DecryptFailurePacing   time.Duration
	SecretResolutionBudget time.Duration
	ResponseBodyLimit      int
}

// Validate reports whether every worker value is positive and the concurrency,
// lease, attempt-ladder, and retry-horizon invariants hold.
func (w DeliveryWorker) Validate() error {
	if w.LeaseDuration <= 0 {
		return errors.New("worker lease duration must be positive")
	}
	if w.HTTPDeadline <= 0 {
		return errors.New("worker HTTP deadline must be positive")
	}
	if w.HandshakeBudget <= 0 {
		return errors.New("worker handshake budget must be positive")
	}
	if w.PostProcessBudget <= 0 {
		return errors.New("worker post-process budget must be positive")
	}
	if w.ShutdownReserve <= 0 {
		return errors.New("worker shutdown reserve must be positive")
	}
	if w.Concurrency <= 0 {
		return errors.New("worker concurrency must be positive")
	}
	if w.Prefetch <= 0 {
		return errors.New("worker prefetch must be positive")
	}
	if w.PoolSize <= 0 {
		return errors.New("worker pool size must be positive")
	}
	if w.MaxAttempts <= 0 {
		return errors.New("worker maximum attempts must be positive")
	}
	if w.RetryHorizon <= 0 {
		return errors.New("worker retry horizon must be positive")
	}
	if w.DecryptFailurePacing <= 0 {
		return errors.New("worker decrypt failure pacing must be positive")
	}
	if w.SecretResolutionBudget <= 0 {
		return errors.New("worker secret resolution budget must be positive")
	}
	if w.ResponseBodyLimit <= 0 {
		return errors.New("worker response body limit must be positive")
	}
	if w.Prefetch != w.Concurrency {
		return errors.New("worker prefetch must equal concurrency")
	}
	if w.PoolSize < w.Concurrency {
		return errors.New("worker pool size must be at least concurrency")
	}
	if w.PoolSize > math.MaxInt32 {
		return errors.New("worker pool size must fit a signed 32-bit integer")
	}
	if w.Concurrency > 65535 {
		return errors.New("worker concurrency and prefetch must not exceed 65535")
	}
	reserved, err := positiveDurationSum(w.HTTPDeadline, w.HandshakeBudget, w.PostProcessBudget, w.ShutdownReserve, w.SecretResolutionBudget)
	if err != nil {
		return fmt.Errorf("worker reserved durations: %w", err)
	}
	if w.LeaseDuration <= reserved {
		return errors.New("worker lease duration must exceed HTTP, handshake, post-process, shutdown, and secret resolution budgets")
	}
	if w.MaxAttempts > maxWorkerAttempts {
		return errors.New("worker maximum attempts must not exceed 8")
	}
	if w.RetryHorizon > maxWorkerRetryHorizon {
		return errors.New("worker retry horizon must not exceed 12h")
	}
	return nil
}

// LoadDeliveryWorker reads and validates the worker configuration from the
// process environment.
func LoadDeliveryWorker() (DeliveryWorker, error) {
	return loadDeliveryWorker(os.LookupEnv)
}

func loadDeliveryWorker(getenv func(string) (string, bool)) (DeliveryWorker, error) {
	worker := DeliveryWorker{
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

	var err error
	worker.LeaseDuration, err = positiveDurationWithDefault(getenv, EnvWorkerLeaseDuration, worker.LeaseDuration)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.HTTPDeadline, err = positiveDurationWithDefault(getenv, EnvWorkerHTTPDeadline, worker.HTTPDeadline)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.HandshakeBudget, err = positiveDurationWithDefault(getenv, EnvWorkerHandshakeBudget, worker.HandshakeBudget)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.PostProcessBudget, err = positiveDurationWithDefault(getenv, EnvWorkerPostProcessBudget, worker.PostProcessBudget)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.ShutdownReserve, err = positiveDurationWithDefault(getenv, EnvWorkerShutdownReserve, worker.ShutdownReserve)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.Concurrency, err = positiveIntWithDefault(getenv, EnvWorkerConcurrency, worker.Concurrency)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.Prefetch, err = positiveIntWithDefault(getenv, EnvWorkerPrefetch, worker.Prefetch)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.PoolSize, err = positiveIntWithDefault(getenv, EnvWorkerPoolSize, worker.PoolSize)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.MaxAttempts, err = positiveIntWithDefault(getenv, EnvWorkerMaxAttempts, worker.MaxAttempts)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.RetryHorizon, err = positiveDurationWithDefault(getenv, EnvWorkerRetryHorizon, worker.RetryHorizon)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.DecryptFailurePacing, err = positiveDurationWithDefault(getenv, EnvWorkerDecryptFailurePacing, worker.DecryptFailurePacing)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.SecretResolutionBudget, err = positiveDurationWithDefault(getenv, EnvWorkerSecretResolution, worker.SecretResolutionBudget)
	if err != nil {
		return DeliveryWorker{}, err
	}
	worker.ResponseBodyLimit, err = positiveIntWithDefault(getenv, EnvWorkerResponseBodyLimit, worker.ResponseBodyLimit)
	if err != nil {
		return DeliveryWorker{}, err
	}

	if err := worker.Validate(); err != nil {
		return DeliveryWorker{}, fmt.Errorf("invalid worker configuration: %w", err)
	}
	return worker, nil
}

func requireValues(getenv func(string) (string, bool), keys []string) (map[string]string, error) {
	values := make(map[string]string, len(keys))
	missing := make([]string, 0, len(keys))
	for _, key := range keys {
		value, ok := getenv(key)
		if !ok || strings.TrimSpace(value) == "" {
			missing = append(missing, key)
			continue
		}
		values[key] = value
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required environment variables: %s", strings.Join(missing, ", "))
	}
	return values, nil
}

func requiredPositiveInt(key, value string) (int, error) {
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive integer", key)
	}
	return parsed, nil
}

func requiredPositiveDuration(key, value string) (time.Duration, error) {
	parsed, err := time.ParseDuration(strings.TrimSpace(value))
	if err != nil || parsed <= 0 {
		return 0, fmt.Errorf("%s must be a positive duration", key)
	}
	return parsed, nil
}

func positiveIntWithDefault(getenv func(string) (string, bool), key string, fallback int) (int, error) {
	value, ok := getenv(key)
	if !ok {
		return fallback, nil
	}
	return requiredPositiveInt(key, value)
}

func positiveDurationWithDefault(getenv func(string) (string, bool), key string, fallback time.Duration) (time.Duration, error) {
	value, ok := getenv(key)
	if !ok {
		return fallback, nil
	}
	return requiredPositiveDuration(key, value)
}

func positiveDurationSum(durations ...time.Duration) (time.Duration, error) {
	sum := time.Duration(0)
	for _, duration := range durations {
		if duration <= 0 {
			return 0, errors.New("each duration must be positive")
		}
		if duration > time.Duration(math.MaxInt64)-sum {
			return 0, errors.New("duration budget overflows")
		}
		sum += duration
	}
	return sum, nil
}
