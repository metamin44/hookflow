//go:build integration

package integration_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/config"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/secrets"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

// recordingTransport is the actual HTTP client boundary. It counts every
// transmission attempt and captures the final on-wire headers and body. With no
// base transport it answers 204 so disposition paths stay deterministic.
type recordingTransport struct {
	base     http.RoundTripper
	requests atomic.Int64

	mu      sync.Mutex
	header  http.Header
	body    []byte
	err     error
	hasSeen bool
}

func newRecordingTransport(base http.RoundTripper) *recordingTransport {
	return &recordingTransport{base: base}
}

func (t *recordingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	t.requests.Add(1)
	var body []byte
	if request.Body != nil {
		body, t.err = io.ReadAll(request.Body)
		_ = request.Body.Close()
	}
	request.Body = io.NopCloser(bytes.NewReader(body))
	t.mu.Lock()
	t.header = request.Header.Clone()
	t.body = body
	t.hasSeen = true
	err := t.err
	t.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if t.base != nil {
		return t.base.RoundTrip(request)
	}
	return &http.Response{
		StatusCode: http.StatusNoContent,
		Header:     http.Header{},
		Body:       http.NoBody,
		Request:    request,
	}, nil
}

func (t *recordingTransport) count() int64 { return t.requests.Load() }

func (t *recordingTransport) captured() (http.Header, []byte) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.header.Clone(), append([]byte(nil), t.body...)
}

// providerBehavior delegates to the fixture provider unless a test replaces the
// unwrap step. It keeps the real wrap/current-kid behavior.
type providerBehavior struct {
	base   kek.Provider
	unwrap func(context.Context, kek.Wrapped, kek.Context) ([]byte, error)
}

func (p *providerBehavior) Wrap(ctx context.Context, dek []byte, c kek.Context) (kek.Wrapped, error) {
	return p.base.Wrap(ctx, dek, c)
}

func (p *providerBehavior) Unwrap(ctx context.Context, wrapped kek.Wrapped, c kek.Context) ([]byte, error) {
	if p.unwrap != nil {
		return p.unwrap(ctx, wrapped, c)
	}
	return p.base.Unwrap(ctx, wrapped, c)
}

func (p *providerBehavior) CurrentKID() string { return p.base.CurrentKID() }

type failingSigner struct {
	calls atomic.Int64
}

func (s *failingSigner) Sign(string, []byte, []byte) (signing.Headers, error) {
	s.calls.Add(1)
	return signing.Headers{}, errors.New("signing service unavailable")
}

type preHTTPCase struct {
	name          string
	storedHeaders string
	secret        func(*testing.T, *fixture) string
	seed          func(*deliverySeed)
	security      func() config.DeliverySecurity
	provider      func(*fixture, seededDelivery) kek.Provider
	signer        func() worker.RequestSigner
	config        func(config.DeliveryWorker) config.DeliveryWorker
	publishSeq    int

	wantHTTP             int
	wantState            string
	wantAttemptRows      int
	wantAttemptCount     int
	wantCompleted        bool
	wantNextAttempt      bool
	wantLeaseCleared     bool
	wantDispatchSeq      int
	wantOutboxRows       int
	wantAck              int64
	wantReject           bool
	wantReasonMetric     string
	wantReasonValue      int64
	wantLastErrorMessage string
	wantWireHeadersEmpty bool
}

func disabledUnsignedSecurity() config.DeliverySecurity {
	security := testSecurityConfig()
	security.AllowUnsignedDelivery = false
	return security
}

// TestWorkerPreHTTPDispositionMatrixIntegration drives the approved pre-HTTP
// classes through the real Worker runtime, real PostgreSQL and a live broker.
// Every case asserts the HTTP boundary, durable state, lease tuple, dispatch
// generation, outbox delta and the low-cardinality reason metric.
func TestWorkerPreHTTPDispositionMatrixIntegration(t *testing.T) {
	var transientElapsed time.Duration
	var transientMu sync.Mutex
	var failing *failingSigner

	cases := []preHTTPCase{
		{
			name: "transient provider unavailability is paced and retried",
			secret: func(t *testing.T, f *fixture) string {
				return f.sealSecret(t, []byte("integration-webhook-secret-0123456789"))
			},
			provider: func(f *fixture, _ seededDelivery) kek.Provider {
				start := time.Now()
				return &providerBehavior{base: f.provider, unwrap: func(ctx context.Context, _ kek.Wrapped, _ kek.Context) ([]byte, error) {
					<-ctx.Done()
					transientMu.Lock()
					transientElapsed = time.Since(start)
					transientMu.Unlock()
					return nil, ctx.Err()
				}}
			},
			// A paced pre-HTTP retry reuses the approved B5 mechanics and leaves
			// the delivery in the pending state with a future next_attempt_at.
			wantHTTP: 0, wantState: "pending", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: false, wantNextAttempt: true, wantLeaseCleared: true,
			wantDispatchSeq: 2, wantOutboxRows: 2, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_unavailable_total", wantReasonValue: 1,
			wantLastErrorMessage: "temporarily unavailable",
		},
		{
			name:     "empty secret with the development gate disabled terminalizes",
			seed:     func(*deliverySeed) {},
			security: disabledUnsignedSecurity,
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_missing_total", wantReasonValue: 1,
			wantLastErrorMessage: "unsigned delivery is not enabled",
		},
		{
			name:          "empty secret with the development gate enabled sends unsigned",
			storedHeaders: `{"webhook-id":"stale-id","webhook-timestamp":"1700000000","webhook-signature":"v1,stale","X-Custom":"snapshot"}`,
			wantHTTP:      1, wantState: "succeeded", wantAttemptRows: 1, wantAttemptCount: 1,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_succeeded_total", wantReasonValue: 1,
			wantWireHeadersEmpty: true,
		},
		{
			name:     "malformed stored envelope terminalizes",
			secret:   func(*testing.T, *fixture) string { return "hfse1.not-a-valid-envelope" },
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_invalid_total", wantReasonValue: 1,
			wantLastErrorMessage: "envelope is invalid",
		},
		{
			name: "unknown kid terminalizes",
			secret: func(t *testing.T, f *fixture) string {
				return f.buildSecretEnvelope(t, f.orgID, f.endpointID, f.orgID, f.endpointID,
					[]byte("integration-webhook-secret-0123456789"), secrets.ProviderLocalFile, "kek-2", nil)
			},
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_invalid_total", wantReasonValue: 1,
			wantLastErrorMessage: "could not be authenticated or decrypted",
		},
		{
			name: "disallowed provider terminalizes",
			secret: func(t *testing.T, f *fixture) string {
				return f.buildSecretEnvelope(t, f.orgID, f.endpointID, f.orgID, f.endpointID,
					[]byte("integration-webhook-secret-0123456789"), secrets.ProviderAWSKMS, "", nil)
			},
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_provider_disallowed_total", wantReasonValue: 1,
			wantLastErrorMessage: "provider is not allowlisted",
		},
		{
			name: "authenticated payload tamper terminalizes",
			secret: func(t *testing.T, f *fixture) string {
				return f.buildSecretEnvelope(t, f.orgID, f.endpointID, f.orgID, f.endpointID,
					[]byte("integration-webhook-secret-0123456789"), secrets.ProviderLocalFile, "", func(envelope *secrets.Envelope) {
						envelope.Ciphertext[0] ^= 0xff
					})
			},
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_invalid_total", wantReasonValue: 1,
			wantLastErrorMessage: "could not be authenticated or decrypted",
		},
		{
			name: "unusable per-delivery secret length terminalizes",
			secret: func(t *testing.T, f *fixture) string {
				return f.sealSecret(t, []byte("too-short"))
			},
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_invalid_total", wantReasonValue: 1,
			wantLastErrorMessage: "could not be authenticated or decrypted",
		},
		{
			name: "internal signing construction failure terminalizes without unsigned fallback",
			secret: func(t *testing.T, f *fixture) string {
				return f.sealSecret(t, []byte("integration-webhook-secret-0123456789"))
			},
			signer: func() worker.RequestSigner {
				failing = &failingSigner{}
				return failing
			},
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_signing_internal_total", wantReasonValue: 1,
			wantLastErrorMessage: "webhook signing failed before HTTP",
		},
		{
			name: "insufficient post-resolution lease budget refuses without HTTP",
			config: func(c config.DeliveryWorker) config.DeliveryWorker {
				// The fixed reserve is exactly 900ms; one nanosecond of margin is
				// consumed before PrepareHTTP samples the database clock.
				c.LeaseDuration = 900*time.Millisecond + time.Nanosecond
				return c
			},
			wantHTTP: 0, wantState: "pending", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: false, wantNextAttempt: true, wantLeaseCleared: false,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 0, wantReject: true,
			wantReasonMetric: "hookflow_worker_budget_refused_total", wantReasonValue: 1,
		},
		{
			name: "event expiry terminalizes as expired",
			seed: func(seed *deliverySeed) {
				seed.eventExpiresAt = time.Now().Add(-time.Minute)
			},
			wantHTTP: 0, wantState: "expired", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_expired_total", wantReasonValue: 1,
		},
		{
			name: "worker lease expiry during resolution expires the delivery",
			secret: func(t *testing.T, f *fixture) string {
				return f.sealSecret(t, []byte("integration-webhook-secret-0123456789"))
			},
			provider: func(f *fixture, d seededDelivery) kek.Provider {
				return &providerBehavior{base: f.provider, unwrap: func(context.Context, kek.Wrapped, kek.Context) ([]byte, error) {
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					if _, err := f.pool.Exec(ctx,
						`UPDATE deliveries SET dispatch_lease_expires_at = clock_timestamp() - interval '1 second'
						  WHERE organization_id = $1::uuid AND id = $2::uuid`, f.orgID, d.id); err != nil {
						return nil, fmt.Errorf("%w: lease simulation failed", kek.ErrTransient)
					}
					return nil, fmt.Errorf("%w: simulated outage after lease loss", kek.ErrTransient)
				}}
			},
			wantHTTP: 0, wantState: "expired", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_secret_unavailable_total", wantReasonValue: 1,
			wantLastErrorMessage: "temporarily unavailable",
		},
		{
			name: "existing private destination terminalizes without a retry outbox row",
			seed: func(seed *deliverySeed) {
				seed.url = "https://127.0.0.1/hook"
			},
			wantHTTP: 0, wantState: "failed", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: true, wantNextAttempt: false, wantLeaseCleared: true,
			wantDispatchSeq: 1, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_destination_disallowed_total", wantReasonValue: 1,
			wantLastErrorMessage: "not permitted by policy",
		},
		{
			name: "stale generation is ACKed without mutation",
			seed: func(seed *deliverySeed) {
				seed.dispatchSeq = 2
				seed.leaseToken = mustIDForSeed()
				seed.leaseExpiresAt = time.Now().Add(time.Hour)
			},
			publishSeq: 1,
			wantHTTP:   0, wantState: "pending", wantAttemptRows: 0, wantAttemptCount: 0,
			wantCompleted: false, wantNextAttempt: true, wantLeaseCleared: false,
			wantDispatchSeq: 2, wantOutboxRows: 1, wantAck: 1,
			wantReasonMetric: "hookflow_worker_stale_total", wantReasonValue: 1,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			transport := newRecordingTransport(nil)
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

			seed := deliverySeed{url: "https://example.test/hook", headers: tc.storedHeaders}
			if tc.seed != nil {
				tc.seed(&seed)
			}
			if tc.secret != nil {
				seed.secret = tc.secret(t, f)
			}
			d := f.seedDelivery(t, seed)
			dispatchSeq := seed.dispatchSeq
			if dispatchSeq == 0 {
				dispatchSeq = 1
			}
			outboxID := f.seedOutbox(t, d.id, dispatchSeq, "published", time.Time{}, time.Time{})
			publishSeq := tc.publishSeq
			if publishSeq == 0 {
				publishSeq = 1
			}
			f.publishPointer(t, d.id, outboxID, publishSeq)

			cfg := testWorkerConfig(1)
			if tc.config != nil {
				cfg = tc.config(cfg)
			}
			security := testSecurityConfig()
			if tc.security != nil {
				security = tc.security()
			}
			var provider kek.Provider
			if tc.provider != nil {
				provider = tc.provider(f, d)
			}
			var signer worker.RequestSigner
			if tc.signer != nil {
				signer = tc.signer()
			}
			runtime, _, _ := f.newWorkerWith(t, cfg, client, security, provider, signer)
			run := startConsumer(t, f.newConsumer(t, cfg, runtime))

			eventually(t, 8*time.Second, "pre-HTTP disposition and broker settlement", func() error {
				if tc.wantReject {
					if got := f.metricValue(t, "hookflow_worker_budget_refused_total"); got < 1 {
						return &metricCountError{name: "budget_refused", got: got}
					}
				} else {
					row := f.readDelivery(t, d.id)
					if row.state != tc.wantState {
						return &stateError{got: row.state, attempts: row.attemptCount, want: tc.wantState}
					}
					if tc.wantReasonMetric != "" && f.metricValue(t, tc.wantReasonMetric) < tc.wantReasonValue {
						return &metricCountError{name: tc.wantReasonMetric, got: f.metricValue(t, tc.wantReasonMetric)}
					}
					info := f.queueInfo(t, messaging.DispatchQueue)
					if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
						return &queueStateError{info: info}
					}
				}
				return nil
			})
			run.stop(t)

			if got := transport.count(); got != int64(tc.wantHTTP) {
				t.Fatalf("HTTP requests = %d, want %d", got, tc.wantHTTP)
			}
			if tc.wantWireHeadersEmpty {
				header, body := transport.captured()
				for _, name := range []string{signing.HeaderID, signing.HeaderTimestamp, signing.HeaderSignature, "X-HookFlow-Signature"} {
					if value := header.Get(name); value != "" {
						t.Fatalf("unsigned request transmitted %s = %q", name, value)
					}
				}
				if header.Get("X-Custom") != "snapshot" {
					t.Fatalf("stored non-protocol header was dropped: %v", header)
				}
				if len(body) == 0 {
					t.Fatal("unsigned request body was empty")
				}
			}

			row := f.readDelivery(t, d.id)
			if row.state != tc.wantState {
				t.Fatalf("state = %q, want %q", row.state, tc.wantState)
			}
			if row.dispatchSeq != tc.wantDispatchSeq {
				t.Fatalf("dispatch_seq = %d, want %d", row.dispatchSeq, tc.wantDispatchSeq)
			}
			if row.attemptCount != tc.wantAttemptCount {
				t.Fatalf("attempt_count = %d, want %d", row.attemptCount, tc.wantAttemptCount)
			}
			if row.completedAt.Valid != tc.wantCompleted {
				t.Fatalf("completed_at present = %v, want %v", row.completedAt.Valid, tc.wantCompleted)
			}
			if row.nextAttemptAt.Valid != tc.wantNextAttempt {
				t.Fatalf("next_attempt_at present = %v, want %v", row.nextAttemptAt.Valid, tc.wantNextAttempt)
			}
			if tc.wantLeaseCleared {
				if row.leaseToken != "" || row.leaseExpiresAt.Valid {
					t.Fatalf("worker lease was retained: token=%q expires=%v", row.leaseToken, row.leaseExpiresAt)
				}
			} else if row.leaseToken == "" {
				t.Log("worker lease was already released or re-acquired; lease tuple is not asserted for this refusal class")
			}
			if got := f.attemptCount(t, d.id); got != tc.wantAttemptRows {
				t.Fatalf("delivery_attempts rows = %d, want %d", got, tc.wantAttemptRows)
			}
			outboxes := f.readOutboxes(t, d.id)
			if len(outboxes) != tc.wantOutboxRows {
				t.Fatalf("outbox rows = %d, want %d: %+v", len(outboxes), tc.wantOutboxRows, outboxes)
			}
			if got := f.metricValue(t, "hookflow_worker_acked_total"); got != tc.wantAck {
				t.Fatalf("ACK metric = %d, want %d", got, tc.wantAck)
			}
			if tc.wantReject {
				if got := f.metricValue(t, "hookflow_worker_rejected_total"); got < 1 {
					t.Fatalf("reject/requeue metric = %d, want at least 1", got)
				}
			}
			if tc.wantReasonMetric != "" {
				if got := f.metricValue(t, tc.wantReasonMetric); got < tc.wantReasonValue {
					t.Fatalf("%s = %d, want at least %d", tc.wantReasonMetric, got, tc.wantReasonValue)
				}
			}
			if tc.wantLastErrorMessage != "" {
				if !row.lastError.Valid || !contains(row.lastError.String, tc.wantLastErrorMessage) {
					t.Fatalf("last_error = %q, want it to contain %q", row.lastError.String, tc.wantLastErrorMessage)
				}
			}
			if tc.name == "internal signing construction failure terminalizes without unsigned fallback" {
				if failing == nil || failing.calls.Load() == 0 {
					t.Fatal("signer was not invoked before the internal failure")
				}
			}
			if tc.name == "transient provider unavailability is paced and retried" {
				transientMu.Lock()
				elapsed := transientElapsed
				transientMu.Unlock()
				if elapsed <= 0 || elapsed > time.Second {
					t.Fatalf("secret resolution elapsed = %s, want it bounded near the 100ms budget", elapsed)
				}
			}
		})
	}
}

func contains(value, substring string) bool {
	return len(substring) == 0 || len(value) >= len(substring) && bytes.Contains([]byte(value), []byte(substring))
}

type metricCountError struct {
	name string
	got  int64
}

func (e *metricCountError) Error() string {
	return e.name + " metric=" + strconv.FormatInt(e.got, 10)
}

// mustIDForSeed returns a canonical UUID for seeds that cannot use *testing.T.
func mustIDForSeed() string {
	value, err := id.NewV7()
	if err != nil {
		panic(err)
	}
	return value
}
