package worker

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

const (
	preHTTPSettlementTimeout = 10 * time.Second
	minWebhookSecretLength   = 16
)

type preHTTPFailure struct {
	reason  delivery.PreHTTPReason
	message string
}

// resolveSecret turns the stored snapshot into the exact signing key bytes.
// Every failure is typed and bounded; no envelope, key, or provider bytes are
// included in a message.
func (r *Runtime) resolveSecret(ctx context.Context, claim delivery.Claim, preparation delivery.HTTPPreparation) ([]byte, *preHTTPFailure) {
	if preparation.SecretCiphertext == "" {
		if r.security.UnsignedAllowed() {
			return nil, nil
		}
		return nil, &preHTTPFailure{
			reason:  delivery.ReasonSecretMissing,
			message: "stored webhook secret is empty and unsigned delivery is not enabled",
		}
	}

	envelope, err := secrets.Decode(preparation.SecretCiphertext)
	if err != nil {
		return nil, &preHTTPFailure{
			reason:  delivery.ReasonSecretInvalid,
			message: "stored webhook secret envelope is invalid",
		}
	}
	if !r.security.AllowsProvider(envelope.Provider) {
		return nil, &preHTTPFailure{
			reason:  delivery.ReasonProviderDisallowed,
			message: "stored webhook secret envelope provider is not allowlisted",
		}
	}
	organizationID, err := secrets.ParseUUID(claim.OrganizationID)
	if err != nil {
		return nil, invalidSecretFailure()
	}
	endpointID, err := secrets.ParseUUID(preparation.EndpointID)
	if err != nil {
		return nil, invalidSecretFailure()
	}

	resolveCtx, cancel := context.WithTimeout(ctx, r.cfg.SecretResolutionBudget)
	defer cancel()
	dek, err := r.provider.Unwrap(resolveCtx,
		kek.Wrapped{KID: envelope.KID, Provider: envelope.Provider, Blob: envelope.WrappedDEK},
		kek.Context{OrganizationID: organizationID, EndpointID: endpointID},
	)
	if err != nil {
		if resolveCtx.Err() != nil || kek.IsTransient(err) {
			return nil, &preHTTPFailure{
				reason:  delivery.ReasonSecretUnavailable,
				message: "webhook secret provider is temporarily unavailable",
			}
		}
		return nil, invalidSecretFailure()
	}
	defer clear(dek)

	aad, err := envelope.BuildAAD(organizationID, endpointID)
	if err != nil {
		return nil, invalidSecretFailure()
	}
	plaintext, err := secrets.Open(dek, envelope.Nonce, envelope.Ciphertext, envelope.Tag, aad[:])
	if err != nil {
		return nil, invalidSecretFailure()
	}
	if len(plaintext) < minWebhookSecretLength || len(plaintext) > secrets.MaxPlaintextLength {
		clear(plaintext)
		return nil, invalidSecretFailure()
	}
	return plaintext, nil
}

func invalidSecretFailure() *preHTTPFailure {
	return &preHTTPFailure{
		reason:  delivery.ReasonSecretInvalid,
		message: "stored webhook secret could not be authenticated or decrypted",
	}
}

// settlePreHTTP persists a pre-HTTP disposition under a fresh bounded context so
// an expired resolution deadline cannot lose the durable failure record. Broker
// settlement remains ACK-after-commit.
func (r *Runtime) settlePreHTTP(ctx context.Context, claim delivery.Claim, failure preHTTPFailure) disposition.Decision {
	work := settlementWork(ctx)
	if work != nil && !work.admitSettlement() {
		return disposition.Decide(disposition.OutcomeUncertain, false)
	}
	forceCtx := settlementForceContext(ctx)
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), preHTTPSettlementTimeout)
	defer cancel()
	if forceCtx != nil {
		// Cancellation may be asynchronous, but admission above is the atomic
		// force-stop gate. A force-stop after admission cancels this admitted work;
		// it cannot make an unadmitted database call start later.
		stop := context.AfterFunc(forceCtx, cancel)
		defer stop()
		if forceCtx.Err() != nil {
			return disposition.Decide(disposition.OutcomeUncertain, false)
		}
	}
	if settleCtx.Err() != nil {
		return disposition.Decide(disposition.OutcomeUncertain, false)
	}

	options := delivery.PreHTTPFailureOptions{Reason: failure.reason, Error: failure.message}
	if failure.reason.Transient() {
		pacing, err := decryptPacingForDelivery(claim.DeliveryID, r.cfg.DecryptFailurePacing)
		if err != nil {
			return disposition.Decide(disposition.OutcomeTransientFailure, false)
		}
		options.Pacing = pacing
	}
	result, err := r.store.RecordPreHTTPFailure(settleCtx, claim, options)
	if err != nil {
		r.logger.Warn("pre-HTTP disposition write failed", "reason", failure.reason)
		return disposition.Decide(disposition.OutcomeUncertain, false)
	}
	r.metrics.Add(preHTTPMetric(failure.reason), 1)
	r.logger.Warn("pre-HTTP delivery failure",
		"reason", failure.reason,
		"expired", result.Expired,
		"applied", result.Applied,
		"fenced", result.Fenced,
	)
	if !result.AckEligible {
		return disposition.Decide(disposition.OutcomeUncertain, false)
	}
	return disposition.Decide(disposition.OutcomeFailed, result.Applied || result.Fenced)
}

func preHTTPMetric(reason delivery.PreHTTPReason) string {
	switch reason {
	case delivery.ReasonSecretMissing:
		return "hookflow_worker_secret_missing_total"
	case delivery.ReasonSecretInvalid:
		return "hookflow_worker_secret_invalid_total"
	case delivery.ReasonProviderDisallowed:
		return "hookflow_worker_provider_disallowed_total"
	case delivery.ReasonSigningInternal:
		return "hookflow_worker_signing_internal_total"
	case delivery.ReasonDestinationDisallowed:
		return "hookflow_worker_destination_disallowed_total"
	case delivery.ReasonHeadersInvalid:
		return "hookflow_worker_headers_invalid_total"
	default:
		return "hookflow_worker_secret_unavailable_total"
	}
}

// remainingAfterResolution subtracts monotonic local elapsed time from the
// database-sampled remainder. It never trusts a frozen preparation value and
// never increases the remainder.
func remainingAfterResolution(sampled time.Duration, elapsed time.Duration) time.Duration {
	if elapsed <= 0 {
		return sampled
	}
	if sampled <= elapsed {
		return 0
	}
	return sampled - elapsed
}

// Store is the exact-pointer, fenced database surface.
type Store interface {
	AcquirePointer(context.Context, delivery.Pointer, delivery.AcquireOptions) (delivery.PointerResult, error)
	PrepareHTTP(context.Context, delivery.Claim, time.Duration) (delivery.HTTPPreparation, error)
	ReleasePointer(context.Context, delivery.Claim) (bool, error)
	ExpirePointer(context.Context, delivery.Pointer) (bool, error)
	Complete(context.Context, delivery.Claim, delivery.Outcome, delivery.RetryDecision) (delivery.CompletionResult, error)
	RecordPreHTTPFailure(context.Context, delivery.Claim, delivery.PreHTTPFailureOptions) (delivery.PreHTTPFailureResult, error)
}

// HTTPExecutor performs the real request and classifies its outcome.
type HTTPExecutor interface {
	Execute(context.Context, delivery.HTTPPreparation, string) (delivery.Outcome, error)
}

// RetryDecider applies the approved deterministic retry ladder.
type RetryDecider interface {
	Decision(string, int, *int, string) delivery.RetryDecision
}

// Runtime processes one broker pointer through acquisition, budget guard,
// secret resolution, signed HTTP, and atomic fenced completion.
type Runtime struct {
	cfg      config.DeliveryWorker
	security config.DeliverySecurity
	provider kek.Provider
	store    Store
	executor HTTPExecutor
	retry    RetryDecider
	newToken delivery.TokenGenerator
	logger   *slog.Logger
	metrics  *observability.Metrics
	now      func() time.Time
}

// NewRuntime validates the approved worker profile and dependencies.
func NewRuntime(cfg config.DeliveryWorker, security config.DeliverySecurity, provider kek.Provider, store Store, executor HTTPExecutor, retry RetryDecider, newToken delivery.TokenGenerator, logger *slog.Logger, metrics *observability.Metrics) (*Runtime, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if err := security.Validate(); err != nil {
		return nil, err
	}
	if provider == nil || store == nil || executor == nil || retry == nil || newToken == nil {
		return nil, errors.New("worker provider, store, executor, retry policy and token generator are required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if metrics == nil {
		metrics = observability.New()
	}
	for _, name := range []string{
		"hookflow_worker_processed_total", "hookflow_worker_stale_total",
		"hookflow_worker_duplicate_total", "hookflow_worker_budget_refused_total",
		"hookflow_worker_secret_unavailable_total", "hookflow_worker_secret_missing_total",
		"hookflow_worker_secret_invalid_total", "hookflow_worker_provider_disallowed_total",
		"hookflow_worker_signing_internal_total", "hookflow_worker_succeeded_total",
		"hookflow_worker_destination_disallowed_total", "hookflow_worker_headers_invalid_total",
		"hookflow_worker_retry_scheduled_total", "hookflow_worker_failed_total",
		"hookflow_worker_expired_total", "hookflow_worker_uncertain_total",
		"hookflow_worker_publication_pending_total",
	} {
		metrics.Set(name, 0)
	}
	return &Runtime{
		cfg: cfg, security: security, provider: provider, store: store,
		executor: executor, retry: retry, newToken: newToken, logger: logger,
		metrics: metrics, now: time.Now,
	}, nil
}

// Process never acknowledges before the required durable state check or commit.
func (r *Runtime) Process(ctx context.Context, handle *Handle) disposition.Decision {
	r.metrics.Add("hookflow_worker_processed_total", 1)
	envelope, err := messaging.ParseDelivery(handle.delivery)
	if err != nil {
		// Strict parsing establishes that no approved work can be mapped.
		return disposition.Decide(disposition.OutcomeInvalid, true)
	}
	pointer := delivery.Pointer{OrganizationID: envelope.OrganizationID, DeliveryID: envelope.DeliveryID, OutboxID: envelope.OutboxID, DispatchSeq: envelope.DispatchSeq}
	result, err := r.store.AcquirePointer(ctx, pointer, delivery.AcquireOptions{LeaseDuration: r.cfg.LeaseDuration, MaxAttempts: r.cfg.MaxAttempts, NewToken: r.newToken})
	if err != nil {
		return disposition.Decide(disposition.OutcomeTransientFailure, false)
	}
	if result.Status != delivery.PointerAcquired || result.Claim == nil {
		return r.nonAcquired(ctx, pointer, result.Status)
	}
	claim := *result.Claim
	required := r.cfg.HTTPDeadline + r.cfg.HandshakeBudget + r.cfg.PostProcessBudget + r.cfg.ShutdownReserve + r.cfg.SecretResolutionBudget
	preparationStarted := r.now()
	preparation, err := r.store.PrepareHTTP(ctx, claim, required)
	if err != nil {
		return r.settlePreparationError(ctx, pointer, claim, err)
	}

	secret, failure := r.resolveSecret(ctx, claim, preparation)
	if failure != nil {
		return r.settlePreHTTP(ctx, claim, *failure)
	}
	if secret != nil {
		defer clear(secret)
	}
	preparation.SecretPlaintext = secret
	preparation.Remaining = remainingAfterResolution(preparation.Remaining, r.now().Sub(preparationStarted))

	outcome, err := r.executor.Execute(ctx, preparation, "POST")
	if err != nil {
		return r.settleExecutionError(ctx, claim, err)
	}

	decision := delivery.RetryDecision{}
	if outcome.Kind == delivery.OutcomeRetryableFailure {
		_, retryAfter := retryMetadata(outcome.ResponseHeaders)
		decision = r.retry.Decision(claim.DeliveryID, claim.AttemptCount+1, outcome.ResponseStatus, retryAfter)
	}
	completed, err := r.store.Complete(ctx, claim, outcome, decision)
	if err != nil {
		// A lost commit response is not evidence that no mutation occurred.
		r.metrics.Add("hookflow_worker_uncertain_total", 1)
		return disposition.Decide(disposition.OutcomeUncertain, false)
	}
	if !completed.Applied && !completed.Fenced {
		r.metrics.Add("hookflow_worker_uncertain_total", 1)
		return disposition.Decide(disposition.OutcomeUncertain, false)
	}
	if completed.Fenced {
		r.metrics.Add("hookflow_worker_stale_total", 1)
		return disposition.Decide(disposition.OutcomeStale, true)
	}
	switch completed.Status {
	case "succeeded":
		r.metrics.Add("hookflow_worker_succeeded_total", 1)
		return disposition.Decide(disposition.OutcomeSucceeded, true)
	case "retrying":
		r.metrics.Add("hookflow_worker_retry_scheduled_total", 1)
		return disposition.Decide(disposition.OutcomeRetryScheduled, true)
	case "expired":
		r.metrics.Add("hookflow_worker_expired_total", 1)
		return disposition.Decide(disposition.OutcomeExpired, true)
	default:
		r.metrics.Add("hookflow_worker_failed_total", 1)
		return disposition.Decide(disposition.OutcomeFailed, true)
	}
}

// settlePreparationError dispositions a failed HTTP preparation. No branch
// acknowledges: the pointer either stays owned by another generation or is
// released for a paced retry.
func (r *Runtime) settlePreparationError(ctx context.Context, pointer delivery.Pointer, claim delivery.Claim, err error) disposition.Decision {
	switch {
	case errors.Is(err, delivery.ErrPointerStale):
		return r.nonAcquired(ctx, pointer, delivery.PointerStale)
	case errors.Is(err, delivery.ErrEventExpired):
		return r.expire(ctx, pointer)
	case errors.Is(err, delivery.ErrInsufficientLease):
		r.metrics.Add("hookflow_worker_budget_refused_total", 1)
		if _, releaseErr := r.store.ReleasePointer(ctx, claim); releaseErr != nil {
			return disposition.Decide(disposition.OutcomeTransientFailure, false)
		}
		return disposition.Decide(disposition.OutcomeTransientFailure, false)
	default:
		return disposition.Decide(disposition.OutcomeTransientFailure, false)
	}
}

// settleExecutionError dispositions an executor refusal. A typed pre-HTTP
// failure terminalizes without HTTP; lease loss releases the pointer and keeps
// the retry paced.
func (r *Runtime) settleExecutionError(ctx context.Context, claim delivery.Claim, err error) disposition.Decision {
	var preHTTP *delivery.PreHTTPError
	switch {
	case errors.As(err, &preHTTP):
		return r.settlePreHTTP(ctx, claim, preHTTPFailure{
			reason:  preHTTP.Reason,
			message: preHTTP.Message,
		})
	case errors.Is(err, delivery.ErrInsufficientLease):
		r.metrics.Add("hookflow_worker_budget_refused_total", 1)
		if _, releaseErr := r.store.ReleasePointer(ctx, claim); releaseErr != nil {
			return disposition.Decide(disposition.OutcomeTransientFailure, false)
		}
		return disposition.Decide(disposition.OutcomeTransientFailure, false)
	default:
		return disposition.Decide(disposition.OutcomeTransientFailure, false)
	}
}

func (r *Runtime) nonAcquired(ctx context.Context, pointer delivery.Pointer, status delivery.PointerStatus) disposition.Decision {
	switch status {
	case delivery.PointerPublishing:
		// Delivery can beat the Publisher's fenced T2 transaction. Retain the
		// exact pointer without ACK/reject or HTTP; channel close will requeue it.
		r.metrics.Add("hookflow_worker_publication_pending_total", 1)
		return disposition.Decide(disposition.OutcomeUncertain, false)
	case delivery.PointerExpired:
		return r.expire(ctx, pointer)
	case delivery.PointerStale:
		r.metrics.Add("hookflow_worker_stale_total", 1)
		return disposition.Decide(disposition.OutcomeStale, true)
	case delivery.PointerDuplicate:
		r.metrics.Add("hookflow_worker_duplicate_total", 1)
		return disposition.Decide(disposition.OutcomeDuplicate, true)
	case delivery.PointerObsolete, delivery.PointerNotDue:
		return disposition.Decide(disposition.OutcomeObsolete, true)
	default:
		return disposition.Decide(disposition.OutcomeInvalid, true)
	}
}

func (r *Runtime) expire(ctx context.Context, pointer delivery.Pointer) disposition.Decision {
	applied, err := r.store.ExpirePointer(ctx, pointer)
	if err != nil {
		return disposition.Decide(disposition.OutcomeTransientFailure, false)
	}
	if applied {
		r.metrics.Add("hookflow_worker_expired_total", 1)
	}
	if !applied {
		// A zero-row update can mean another worker already expired or
		// terminalized the row. AcquirePointer's durable expired classification
		// still permits ACK; no state is revived.
		return disposition.Decide(disposition.OutcomeExpired, true)
	}
	return disposition.Decide(disposition.OutcomeExpired, true)
}

func retryMetadata(headers string) (*int, string) {
	var values map[string]any
	if json.Unmarshal([]byte(headers), &values) != nil {
		return nil, ""
	}
	var status *int
	for key, value := range values {
		if equalFoldASCII(key, "status") {
			if number, ok := value.(float64); ok && number >= 100 && number <= 599 {
				converted := int(number)
				status = &converted
			}
		}
	}
	retryAfter := ""
	for key, value := range values {
		if equalFoldASCII(key, "retry-after") {
			retryAfter, _ = value.(string)
		}
	}
	return status, retryAfter
}

func equalFoldASCII(left, right string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		a, b := left[i], right[i]
		if a >= 'A' && a <= 'Z' {
			a += 'a' - 'A'
		}
		if b >= 'A' && b <= 'Z' {
			b += 'a' - 'A'
		}
		if a != b {
			return false
		}
	}
	return true
}

func decryptPacingForDelivery(deliveryID string, base time.Duration) (time.Duration, error) {
	pacing := DecryptFailurePacing(deliveryID, base)
	if pacing <= 0 {
		return 0, errors.New("decrypt failure pacing must be positive")
	}
	return pacing, nil
}
