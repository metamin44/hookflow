package worker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

const (
	testOrganizationID = "11111111-1111-4111-8111-111111111111"
	testDeliveryID     = "22222222-2222-4222-8222-222222222222"
	testOutboxID       = "33333333-3333-4333-8333-333333333333"
	testEventID        = "44444444-4444-4444-8444-444444444444"
	testLeaseToken     = "55555555-5555-4555-8555-555555555555"
	testEndpointID     = "66666666-6666-4666-8666-666666666666"
)

var testWebhookSecret = []byte("webhook-secret-0123456789")

type fakeStore struct {
	acquire       delivery.PointerResult
	acquireErr    error
	prepare       delivery.HTTPPreparation
	prepareErr    error
	complete      delivery.CompletionResult
	completeErr   error
	preHTTP       delivery.PreHTTPFailureResult
	preHTTPErr    error
	preHTTPClaim  delivery.Claim
	preHTTPOption delivery.PreHTTPFailureOptions
	acquired      int
	prepared      int
	completed     int
	released      int
	expired       int
	preHTTPCalls  int
	// prepareRequired records the fixed lease budget the processor demanded
	// before any secret resolution or HTTP.
	prepareRequired time.Duration
}

func (s *fakeStore) AcquirePointer(context.Context, delivery.Pointer, delivery.AcquireOptions) (delivery.PointerResult, error) {
	s.acquired++
	return s.acquire, s.acquireErr
}
func (s *fakeStore) PrepareHTTP(_ context.Context, _ delivery.Claim, required time.Duration) (delivery.HTTPPreparation, error) {
	s.prepared++
	s.prepareRequired = required
	return s.prepare, s.prepareErr
}
func (s *fakeStore) ReleasePointer(context.Context, delivery.Claim) (bool, error) {
	s.released++
	return true, nil
}
func (s *fakeStore) ExpirePointer(context.Context, delivery.Pointer) (bool, error) {
	s.expired++
	return true, nil
}
func (s *fakeStore) Complete(context.Context, delivery.Claim, delivery.Outcome, delivery.RetryDecision) (delivery.CompletionResult, error) {
	s.completed++
	return s.complete, s.completeErr
}
func (s *fakeStore) RecordPreHTTPFailure(_ context.Context, claim delivery.Claim, options delivery.PreHTTPFailureOptions) (delivery.PreHTTPFailureResult, error) {
	s.preHTTPCalls++
	s.preHTTPClaim = claim
	s.preHTTPOption = options
	return s.preHTTP, s.preHTTPErr
}

type fakeExecutor struct {
	outcome     delivery.Outcome
	err         error
	calls       int
	preparation delivery.HTTPPreparation
	secret      []byte
	remaining   time.Duration
}

func (e *fakeExecutor) Execute(_ context.Context, preparation delivery.HTTPPreparation, _ string) (delivery.Outcome, error) {
	e.calls++
	e.preparation = preparation
	e.secret = append([]byte(nil), preparation.SecretPlaintext...)
	e.remaining = preparation.Remaining
	return e.outcome, e.err
}

type fakeRetry struct{ decision delivery.RetryDecision }

func (r *fakeRetry) Decision(string, int, *int, string) delivery.RetryDecision { return r.decision }

type stubProvider struct {
	unwrap func(context.Context, kek.Wrapped, kek.Context) ([]byte, error)
}

func (p *stubProvider) Wrap(context.Context, []byte, kek.Context) (kek.Wrapped, error) {
	return kek.Wrapped{}, errors.New("unexpected wrap")
}
func (p *stubProvider) Unwrap(ctx context.Context, wrapped kek.Wrapped, c kek.Context) ([]byte, error) {
	if p.unwrap == nil {
		return nil, errors.New("unexpected unwrap")
	}
	return p.unwrap(ctx, wrapped, c)
}
func (p *stubProvider) CurrentKID() string { return "kek-1" }

func testWorkerConfig() config.DeliveryWorker {
	return config.DeliveryWorker{LeaseDuration: 30 * time.Second, HTTPDeadline: 10 * time.Second, HandshakeBudget: 2 * time.Second, PostProcessBudget: 3 * time.Second, ShutdownReserve: 10 * time.Second, SecretResolutionBudget: 2 * time.Second, Concurrency: 1, Prefetch: 1, PoolSize: 2, MaxAttempts: 8, RetryHorizon: 12 * time.Hour, DecryptFailurePacing: 30 * time.Second, ResponseBodyLimit: 1024}
}

func testSignedSecurity() config.DeliverySecurity {
	return config.DeliverySecurity{
		Mode:             config.ModeDevelopment,
		Provider:         kek.ProviderLocalFile,
		KeyringPath:      "/outside/repository/keyring.json",
		CurrentKID:       "kek-1",
		AllowedKIDs:      []string{"kek-1"},
		AllowedProviders: []string{kek.ProviderLocalFile},
	}
}

func testUnsignedSecurity() config.DeliverySecurity {
	security := testSignedSecurity()
	security.AllowUnsignedDelivery = true
	return security
}

func testMemoryProvider(t *testing.T) *kek.MemoryProvider {
	t.Helper()
	provider, err := kek.NewMemoryProvider("kek-1", map[string][]byte{
		"kek-1": []byte("0123456789abcdef0123456789abcdef"),
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	return provider
}

func testHandle(t *testing.T) *Handle {
	t.Helper()
	envelope := messaging.Envelope{SchemaVersion: 1, OrganizationID: testOrganizationID, DeliveryID: testDeliveryID, DispatchSeq: 1, OutboxID: testOutboxID}
	publishing, err := messaging.EncodePublishing(envelope)
	if err != nil {
		t.Fatal(err)
	}
	return newHandle(&channelGeneration{}, amqp.Delivery{Body: publishing.Body, Headers: publishing.Headers, ContentType: publishing.ContentType, DeliveryMode: publishing.DeliveryMode, MessageId: publishing.MessageId, CorrelationId: publishing.CorrelationId, AppId: publishing.AppId, Exchange: messaging.DispatchExchange, RoutingKey: messaging.DispatchRoutingKey})
}

func testClaim() delivery.Claim {
	return delivery.Claim{OrganizationID: testOrganizationID, DeliveryID: testDeliveryID, EventID: testEventID, EndpointID: testEndpointID, DispatchSeq: 1, LeaseToken: testLeaseToken, LeaseExpiresAt: time.Now().Add(30 * time.Second)}
}

// encryptedPreparation builds a real stored envelope for the test tenant. The
// envelope is encoded exactly as the delivery store would persist it.
func encryptedPreparation(t *testing.T, wrapping kek.Provider, plaintext []byte, envelopeProvider byte) delivery.HTTPPreparation {
	t.Helper()
	organization, err := secrets.ParseUUID(testOrganizationID)
	if err != nil {
		t.Fatal(err)
	}
	endpoint, err := secrets.ParseUUID(testEndpointID)
	if err != nil {
		t.Fatal(err)
	}
	dek := bytes.Repeat([]byte{0x2a}, secrets.DEKSize)
	nonce := bytes.Repeat([]byte{0x11}, secrets.NonceSize)
	aad, err := secrets.BuildAAD(secrets.AlgorithmAES256GCM, secrets.PurposeWebhookSecret, nonce, organization, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, tag, err := secrets.Seal(dek, nonce, plaintext, aad[:])
	if err != nil {
		t.Fatal(err)
	}
	wrapped, err := wrapping.Wrap(context.Background(), dek, kek.Context{OrganizationID: organization, EndpointID: endpoint})
	if err != nil {
		t.Fatal(err)
	}
	envelope := secrets.Envelope{
		Version:    secrets.EnvelopeVersion,
		Algorithm:  secrets.AlgorithmAES256GCM,
		Purpose:    secrets.PurposeWebhookSecret,
		KID:        wrapped.KID,
		Provider:   envelopeProvider,
		WrappedDEK: wrapped.Blob,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		Tag:        tag,
	}
	encoded, err := envelope.Encode()
	if err != nil {
		t.Fatal(err)
	}
	return delivery.HTTPPreparation{EndpointID: testEndpointID, Remaining: 30 * time.Second, SecretCiphertext: encoded}
}

func newTestRuntime(t *testing.T, store *fakeStore, executor *fakeExecutor) *Runtime {
	t.Helper()
	return newTestRuntimeWith(t, testWorkerConfig(), testUnsignedSecurity(), testMemoryProvider(t), store, executor)
}

func newTestRuntimeWith(t *testing.T, cfg config.DeliveryWorker, security config.DeliverySecurity, provider kek.Provider, store *fakeStore, executor *fakeExecutor) *Runtime {
	t.Helper()
	runtime, _ := newTestRuntimeAndMetrics(t, cfg, security, provider, store, executor)
	return runtime
}

func newTestRuntimeAndMetrics(t *testing.T, cfg config.DeliveryWorker, security config.DeliverySecurity, provider kek.Provider, store *fakeStore, executor *fakeExecutor) (*Runtime, *observability.Metrics) {
	t.Helper()
	metrics := observability.New()
	runtime, err := NewRuntime(cfg, security, provider, store, executor, &fakeRetry{}, delivery.RandomToken, slog.New(slog.NewTextHandler(io.Discard, nil)), metrics)
	if err != nil {
		t.Fatal(err)
	}
	return runtime, metrics
}

func metricValue(t *testing.T, metrics *observability.Metrics, name string) int64 {
	t.Helper()
	recorder := httptest.NewRecorder()
	metrics.Handler().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	for _, line := range strings.Split(recorder.Body.String(), "\n") {
		rest, ok := strings.CutPrefix(line, name+" ")
		if !ok {
			continue
		}
		value, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			t.Fatalf("parse metric %s: %v", name, err)
		}
		return value
	}
	return 0
}

func acquiredStore(preparation delivery.HTTPPreparation) *fakeStore {
	claim := testClaim()
	return &fakeStore{acquire: delivery.PointerResult{Status: delivery.PointerAcquired, Claim: &claim}, prepare: preparation}
}

func TestProcessAckOnlyAfterDurableSuccess(t *testing.T) {
	store := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	store.complete = delivery.CompletionResult{Applied: true, Status: "succeeded"}
	executor := &fakeExecutor{outcome: delivery.Outcome{Kind: delivery.OutcomeSuccess}}
	decision := newTestRuntime(t, store, executor).Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || store.completed != 1 || executor.calls != 1 {
		t.Fatalf("decision=%+v completed=%d http=%d", decision, store.completed, executor.calls)
	}
}

func TestProcessPendingPublicationHoldsWithoutHTTP(t *testing.T) {
	store := &fakeStore{acquire: delivery.PointerResult{Status: delivery.PointerPublishing}}
	executor := &fakeExecutor{}
	decision := newTestRuntime(t, store, executor).Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionHold || executor.calls != 0 || store.prepared != 0 || store.completed != 0 {
		t.Fatalf("publication before T2: decision=%+v HTTP=%d prepared=%d completed=%d", decision, executor.calls, store.prepared, store.completed)
	}
}

func TestProcessUnknownCommitHolds(t *testing.T) {
	store := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	store.completeErr = errors.New("commit response lost")
	executor := &fakeExecutor{outcome: delivery.Outcome{Kind: delivery.OutcomeSuccess}}
	decision := newTestRuntime(t, store, executor).Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionHold {
		t.Fatalf("decision = %+v, want hold", decision)
	}
}

func TestProcessTransientDatabaseFailureRejects(t *testing.T) {
	store := &fakeStore{acquireErr: errors.New("database unavailable")}
	executor := &fakeExecutor{}
	decision := newTestRuntime(t, store, executor).Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionRejectRequeue || executor.calls != 0 {
		t.Fatalf("decision=%+v http=%d", decision, executor.calls)
	}
}

func TestProcessStalePointerAcksWithoutHTTP(t *testing.T) {
	store := &fakeStore{acquire: delivery.PointerResult{Status: delivery.PointerStale}}
	executor := &fakeExecutor{}
	decision := newTestRuntime(t, store, executor).Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || executor.calls != 0 || store.completed != 0 {
		t.Fatalf("decision=%+v http=%d completed=%d", decision, executor.calls, store.completed)
	}
}

func TestProcessMalformedPointerAcksWithoutHTTP(t *testing.T) {
	store := &fakeStore{}
	executor := &fakeExecutor{}
	handle := newHandle(&channelGeneration{}, amqp.Delivery{Body: []byte(`{"schema_version":1}`)})
	decision := newTestRuntime(t, store, executor).Process(context.Background(), handle)
	if decision.Action != disposition.ActionAck || executor.calls != 0 || store.acquired != 0 {
		t.Fatalf("decision=%+v http=%d acquired=%d", decision, executor.calls, store.acquired)
	}
}

func TestProcessMissingSecretTerminalizesWithoutHTTP(t *testing.T) {
	store := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	executor := &fakeExecutor{}
	runtime, metrics := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), testMemoryProvider(t), store, executor)
	decision := runtime.Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || executor.calls != 0 || store.completed != 0 {
		t.Fatalf("decision=%+v HTTP=%d completed=%d", decision, executor.calls, store.completed)
	}
	if store.preHTTPCalls != 1 || store.preHTTPOption.Reason != delivery.ReasonSecretMissing {
		t.Fatalf("pre-HTTP calls=%d reason=%s", store.preHTTPCalls, store.preHTTPOption.Reason)
	}
	if got := metricValue(t, metrics, "hookflow_worker_secret_missing_total"); got != 1 {
		t.Fatalf("secret_missing metric = %d, want 1", got)
	}
	if store.preHTTPOption.Pacing != 0 {
		t.Fatalf("permanent failure carried pacing %s", store.preHTTPOption.Pacing)
	}
	if strings.TrimSpace(store.preHTTPOption.Error) == "" {
		t.Fatal("permanent failure carried no bounded diagnostic")
	}
}

func TestProcessUnsignedDeliveryRequiresTheGate(t *testing.T) {
	unsigned := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	unsigned.complete = delivery.CompletionResult{Applied: true, Status: "succeeded"}
	executor := &fakeExecutor{outcome: delivery.Outcome{Kind: delivery.OutcomeSuccess}}
	decision := newTestRuntimeWith(t, testWorkerConfig(), testUnsignedSecurity(), testMemoryProvider(t), unsigned, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || executor.calls != 1 || unsigned.completed != 1 {
		t.Fatalf("gated unsigned decision=%+v HTTP=%d completed=%d", decision, executor.calls, unsigned.completed)
	}
	if executor.secret != nil {
		t.Fatalf("unsigned delivery received secret bytes: %d", len(executor.secret))
	}

	gated := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	gated.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	executor = &fakeExecutor{}
	decision = newTestRuntimeWith(t, testWorkerConfig(), testSignedSecurity(), testMemoryProvider(t), gated, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || executor.calls != 0 || gated.completed != 0 {
		t.Fatalf("closed-gate unsigned decision=%+v HTTP=%d completed=%d", decision, executor.calls, gated.completed)
	}
	if gated.preHTTPOption.Reason != delivery.ReasonSecretMissing {
		t.Fatalf("closed-gate unsigned reason = %s", gated.preHTTPOption.Reason)
	}
}

func TestProcessResolvedSecretAndFreshRemainingBudget(t *testing.T) {
	provider := testMemoryProvider(t)
	preparation := encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderLocalFile)
	store := acquiredStore(preparation)
	store.complete = delivery.CompletionResult{Applied: true, Status: "succeeded"}
	executor := &fakeExecutor{outcome: delivery.Outcome{Kind: delivery.OutcomeSuccess}}
	runtime := newTestRuntimeWith(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor)

	base := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	calls := 0
	runtime.now = func() time.Time {
		calls++
		if calls == 1 {
			return base
		}
		return base.Add(time.Second)
	}

	decision := runtime.Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || store.completed != 1 || executor.calls != 1 {
		t.Fatalf("decision=%+v completed=%d HTTP=%d", decision, store.completed, executor.calls)
	}
	if string(executor.secret) != string(testWebhookSecret) {
		t.Fatalf("resolved secret = %d bytes, want the exact stored secret", len(executor.secret))
	}
	if executor.remaining != 29*time.Second {
		t.Fatalf("remaining budget = %s, want 30s sampled minus 1s local resolution", executor.remaining)
	}
}

func TestProcessPermanentSecretFailuresTerminalizeWithoutHTTP(t *testing.T) {
	provider := testMemoryProvider(t)
	tests := []struct {
		name        string
		preparation func(*testing.T) delivery.HTTPPreparation
		wantReason  delivery.PreHTTPReason
		wantMetric  string
	}{
		{
			name: "malformed envelope",
			preparation: func(*testing.T) delivery.HTTPPreparation {
				return delivery.HTTPPreparation{EndpointID: testEndpointID, Remaining: 30 * time.Second, SecretCiphertext: "hfse1.not-valid-base64!"}
			},
			wantReason: delivery.ReasonSecretInvalid,
			wantMetric: "hookflow_worker_secret_invalid_total",
		},
		{
			name: "provider not allowlisted",
			preparation: func(t *testing.T) delivery.HTTPPreparation {
				return encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderAWSKMS)
			},
			wantReason: delivery.ReasonProviderDisallowed,
			wantMetric: "hookflow_worker_provider_disallowed_total",
		},
		{
			name: "too short plaintext",
			preparation: func(t *testing.T) delivery.HTTPPreparation {
				return encryptedPreparation(t, provider, []byte("short"), secrets.ProviderLocalFile)
			},
			wantReason: delivery.ReasonSecretInvalid,
			wantMetric: "hookflow_worker_secret_invalid_total",
		},
		{
			name: "mismatched endpoint aad",
			preparation: func(t *testing.T) delivery.HTTPPreparation {
				preparation := encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderLocalFile)
				preparation.EndpointID = "77777777-7777-4777-8777-777777777777"
				return preparation
			},
			wantReason: delivery.ReasonSecretInvalid,
			wantMetric: "hookflow_worker_secret_invalid_total",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := acquiredStore(tt.preparation(t))
			store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
			executor := &fakeExecutor{}
			runtime, metrics := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor)
			decision := runtime.Process(context.Background(), testHandle(t))
			if decision.Action != disposition.ActionAck || executor.calls != 0 || store.completed != 0 {
				t.Fatalf("decision=%+v HTTP=%d completed=%d", decision, executor.calls, store.completed)
			}
			if store.preHTTPCalls != 1 || store.preHTTPOption.Reason != tt.wantReason {
				t.Fatalf("pre-HTTP calls=%d reason=%s, want %s", store.preHTTPCalls, store.preHTTPOption.Reason, tt.wantReason)
			}
			if got := metricValue(t, metrics, tt.wantMetric); got != 1 {
				t.Fatalf("%s metric = %d, want 1", tt.wantMetric, got)
			}
			if store.preHTTPOption.Pacing != 0 {
				t.Fatalf("permanent failure carried pacing %s", store.preHTTPOption.Pacing)
			}
			if strings.Contains(store.preHTTPOption.Error, string(testWebhookSecret)) {
				t.Fatal("bounded diagnostic leaked the webhook secret")
			}
		})
	}
}

func TestProcessTransientProviderFailureUsesPacedDisposition(t *testing.T) {
	wrapping := testMemoryProvider(t)
	preparation := encryptedPreparation(t, wrapping, testWebhookSecret, secrets.ProviderLocalFile)
	provider := &stubProvider{unwrap: func(context.Context, kek.Wrapped, kek.Context) ([]byte, error) {
		return nil, kek.ErrTransient
	}}
	store := acquiredStore(preparation)
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	executor := &fakeExecutor{}
	runtime, metrics := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor)
	decision := runtime.Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || executor.calls != 0 || store.completed != 0 {
		t.Fatalf("decision=%+v HTTP=%d completed=%d", decision, executor.calls, store.completed)
	}
	if store.preHTTPCalls != 1 || store.preHTTPOption.Reason != delivery.ReasonSecretUnavailable {
		t.Fatalf("pre-HTTP calls=%d reason=%s", store.preHTTPCalls, store.preHTTPOption.Reason)
	}
	if got := metricValue(t, metrics, "hookflow_worker_secret_unavailable_total"); got != 1 {
		t.Fatalf("secret_unavailable metric = %d, want 1", got)
	}
	if store.preHTTPOption.Pacing <= 0 {
		t.Fatalf("transient failure pacing = %s, want positive", store.preHTTPOption.Pacing)
	}
}

func TestProcessResolutionTimeoutIsTransient(t *testing.T) {
	wrapping := testMemoryProvider(t)
	preparation := encryptedPreparation(t, wrapping, testWebhookSecret, secrets.ProviderLocalFile)
	provider := &stubProvider{unwrap: func(ctx context.Context, _ kek.Wrapped, _ kek.Context) ([]byte, error) {
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	cfg := testWorkerConfig()
	cfg.SecretResolutionBudget = 10 * time.Millisecond
	store := acquiredStore(preparation)
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	executor := &fakeExecutor{}
	decision := newTestRuntimeWith(t, cfg, testSignedSecurity(), provider, store, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || executor.calls != 0 {
		t.Fatalf("decision=%+v HTTP=%d", decision, executor.calls)
	}
	if store.preHTTPOption.Reason != delivery.ReasonSecretUnavailable || store.preHTTPOption.Pacing <= 0 {
		t.Fatalf("reason=%s pacing=%s", store.preHTTPOption.Reason, store.preHTTPOption.Pacing)
	}
}

func TestProcessExecutorPreHTTPFailureSettles(t *testing.T) {
	provider := testMemoryProvider(t)
	store := acquiredStore(encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderLocalFile))
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	executor := &fakeExecutor{
		outcome: delivery.Outcome{Kind: delivery.OutcomeTerminalFailure},
		err: &delivery.PreHTTPError{
			Reason:  delivery.ReasonSigningInternal,
			Message: "webhook signing failed before HTTP",
		},
	}
	runtime, metrics := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor)
	decision := runtime.Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || store.completed != 0 {
		t.Fatalf("decision=%+v completed=%d", decision, store.completed)
	}
	if store.preHTTPCalls != 1 || store.preHTTPOption.Reason != delivery.ReasonSigningInternal {
		t.Fatalf("pre-HTTP calls=%d reason=%s", store.preHTTPCalls, store.preHTTPOption.Reason)
	}
	if got := metricValue(t, metrics, "hookflow_worker_signing_internal_total"); got != 1 {
		t.Fatalf("signing_internal metric = %d, want 1", got)
	}
}

func TestProcessDestinationDisallowedSettlesPermanently(t *testing.T) {
	provider := testMemoryProvider(t)
	store := acquiredStore(encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderLocalFile))
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: true}
	executor := &fakeExecutor{
		outcome: delivery.Outcome{Kind: delivery.OutcomeTerminalFailure},
		err: &delivery.PreHTTPError{
			Reason:  delivery.ReasonDestinationDisallowed,
			Message: "webhook destination is not permitted by policy",
		},
	}
	runtime, metrics := newTestRuntimeAndMetrics(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor)
	decision := runtime.Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || store.completed != 0 || store.released != 0 {
		t.Fatalf("decision=%+v completed=%d released=%d", decision, store.completed, store.released)
	}
	if store.preHTTPCalls != 1 || store.preHTTPOption.Reason != delivery.ReasonDestinationDisallowed {
		t.Fatalf("pre-HTTP calls=%d reason=%s", store.preHTTPCalls, store.preHTTPOption.Reason)
	}
	if store.preHTTPOption.Pacing != 0 {
		t.Fatalf("permanent destination failure carried pacing %s", store.preHTTPOption.Pacing)
	}
	if got := metricValue(t, metrics, "hookflow_worker_destination_disallowed_total"); got != 1 {
		t.Fatalf("destination_disallowed metric = %d, want 1", got)
	}
}

func TestProcessPreHTTPWriteFailureHolds(t *testing.T) {
	store := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	store.preHTTPErr = errors.New("database unavailable")
	executor := &fakeExecutor{}
	decision := newTestRuntimeWith(t, testWorkerConfig(), testSignedSecurity(), testMemoryProvider(t), store, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionHold || executor.calls != 0 {
		t.Fatalf("decision=%+v HTTP=%d", decision, executor.calls)
	}
}

func TestProcessPreHTTPNotAckEligibleHolds(t *testing.T) {
	store := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	store.preHTTP = delivery.PreHTTPFailureResult{Applied: true, AckEligible: false}
	executor := &fakeExecutor{}
	decision := newTestRuntimeWith(t, testWorkerConfig(), testSignedSecurity(), testMemoryProvider(t), store, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionHold || executor.calls != 0 || store.completed != 0 {
		t.Fatalf("decision=%+v HTTP=%d completed=%d", decision, executor.calls, store.completed)
	}
}

func TestProcessBudgetRefusalReleasesThenRejects(t *testing.T) {
	store := acquiredStore(delivery.HTTPPreparation{})
	store.prepareErr = delivery.ErrInsufficientLease
	executor := &fakeExecutor{}
	decision := newTestRuntime(t, store, executor).Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionRejectRequeue || store.released != 1 || executor.calls != 0 {
		t.Fatalf("decision=%+v released=%d http=%d", decision, store.released, executor.calls)
	}
}

func TestProcessExecutorBudgetRefusalReleasesThenRejects(t *testing.T) {
	provider := testMemoryProvider(t)
	store := acquiredStore(encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderLocalFile))
	executor := &fakeExecutor{err: delivery.ErrInsufficientLease}
	decision := newTestRuntimeWith(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionRejectRequeue || store.released != 1 || store.completed != 0 {
		t.Fatalf("decision=%+v released=%d completed=%d", decision, store.released, store.completed)
	}
}

func TestRemainingAfterResolution(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		sampled time.Duration
		elapsed time.Duration
		want    time.Duration
	}{
		{name: "no elapsed time", sampled: 30 * time.Second, elapsed: 0, want: 30 * time.Second},
		{name: "negative elapsed is not trusted", sampled: 30 * time.Second, elapsed: -time.Second, want: 30 * time.Second},
		{name: "normal subtraction", sampled: 30 * time.Second, elapsed: 1500 * time.Millisecond, want: 28500 * time.Millisecond},
		{name: "exact exhaustion", sampled: time.Second, elapsed: time.Second, want: 0},
		{name: "over-run clamps to zero", sampled: time.Second, elapsed: 2 * time.Second, want: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := remainingAfterResolution(tt.sampled, tt.elapsed); got != tt.want {
				t.Fatalf("remainingAfterResolution(%s, %s) = %s, want %s", tt.sampled, tt.elapsed, got, tt.want)
			}
		})
	}
}

// The processor must demand the fixed HTTP reserve plus the bounded secret
// resolution budget before it starts any provider I/O.
func TestProcessRequiresSecretResolutionBudgetInTheFixedLease(t *testing.T) {
	t.Parallel()

	provider := testMemoryProvider(t)
	store := acquiredStore(encryptedPreparation(t, provider, testWebhookSecret, secrets.ProviderLocalFile))
	store.complete = delivery.CompletionResult{Applied: true, Status: "succeeded"}
	executor := &fakeExecutor{outcome: delivery.Outcome{Kind: delivery.OutcomeSuccess}}
	decision := newTestRuntimeWith(t, testWorkerConfig(), testSignedSecurity(), provider, store, executor).
		Process(context.Background(), testHandle(t))
	if decision.Action != disposition.ActionAck || store.prepared != 1 {
		t.Fatalf("decision=%+v prepared=%d", decision, store.prepared)
	}
	const approved = 10*time.Second + 2*time.Second + 3*time.Second + 10*time.Second + 2*time.Second
	if store.prepareRequired != approved {
		t.Fatalf("pre-HTTP required budget = %s, want the approved 27s fixed reserve", store.prepareRequired)
	}
}

// Deployment modes that cannot gate unsigned delivery are rejected at
// construction, before any worker consumption is possible.
func TestNewRuntimeRefusesModesThatCannotConsume(t *testing.T) {
	t.Parallel()

	provider := testMemoryProvider(t)
	store := acquiredStore(delivery.HTTPPreparation{Remaining: 30 * time.Second})
	executor := &fakeExecutor{}
	cases := []struct {
		name     string
		mutate   func(*config.DeliverySecurity)
		contains string
	}{
		{name: "unset mode", mutate: func(s *config.DeliverySecurity) { s.Mode = "" }, contains: "deployment mode"},
		{name: "unknown mode", mutate: func(s *config.DeliverySecurity) { s.Mode = "staging" }, contains: "deployment mode"},
		{name: "production local file provider", mutate: func(s *config.DeliverySecurity) {
			s.Mode = config.ModeProduction
			s.AllowUnsignedDelivery = false
		}, contains: "production"},
		{name: "production unsigned gate", mutate: func(s *config.DeliverySecurity) {
			s.Mode = config.ModeProduction
			s.AllowUnsignedDelivery = true
		}, contains: "unsigned delivery"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			security := testSignedSecurity()
			tc.mutate(&security)
			_, err := NewRuntime(testWorkerConfig(), security, provider, store, executor,
				&fakeRetry{}, delivery.RandomToken, slog.New(slog.NewTextHandler(io.Discard, nil)), observability.New())
			if err == nil || !strings.Contains(err.Error(), tc.contains) {
				t.Fatalf("NewRuntime() error = %v, want %q", err, tc.contains)
			}
			if store.acquired != 0 || executor.calls != 0 {
				t.Fatalf("refused runtime consumed work: acquired=%d HTTP=%d", store.acquired, executor.calls)
			}
		})
	}
}
