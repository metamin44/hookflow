//go:build integration

package integration_test

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/secrets"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

// TestWorkerEndpointIdentityBindingIntegration proves the stored organization
// and endpoint identities flow from the dispatch pointer through the claim and
// preparation into both the payload AAD and the KEK wrap context, and that a
// mismatch in either binding fails closed before any HTTP.
func TestWorkerEndpointIdentityBindingIntegration(t *testing.T) {
	type identityCase struct {
		name       string
		secret     func(*testing.T, *fixture, string) string
		wantHTTP   int
		wantState  string
		wantMetric string
	}
	cases := []identityCase{
		{
			name: "exact organization and endpoint resolve and sign",
			secret: func(t *testing.T, f *fixture, _ string) string {
				return f.sealSecret(t, []byte("integration-webhook-secret-0123456789"))
			},
			wantHTTP:  1,
			wantState: "succeeded",
		},
		{
			name: "envelope bound to another organization fails closed",
			secret: func(t *testing.T, f *fixture, _ string) string {
				return f.sealSecretFor(t, mustID(t), f.endpointID, []byte("integration-webhook-secret-0123456789"))
			},
			wantHTTP:   0,
			wantState:  "failed",
			wantMetric: "hookflow_worker_secret_invalid_total",
		},
		{
			name: "envelope wrapped for another endpoint fails closed",
			secret: func(t *testing.T, f *fixture, otherEndpoint string) string {
				return f.sealSecretFor(t, f.orgID, otherEndpoint, []byte("integration-webhook-secret-0123456789"))
			},
			wantHTTP:   0,
			wantState:  "failed",
			wantMetric: "hookflow_worker_secret_invalid_total",
		},
		{
			name: "payload AAD bound to another endpoint fails closed",
			secret: func(t *testing.T, f *fixture, otherEndpoint string) string {
				return f.buildSecretEnvelope(t, f.orgID, f.endpointID, f.orgID, otherEndpoint,
					[]byte("integration-webhook-secret-0123456789"), secrets.ProviderLocalFile, "", nil)
			},
			wantHTTP:   0,
			wantState:  "failed",
			wantMetric: "hookflow_worker_secret_invalid_total",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			otherEndpoint := f.seedEndpoint(t)
			transport := newRecordingTransport(nil)
			client := &http.Client{Transport: transport, Timeout: 2 * time.Second}
			secret := tc.secret(t, f, otherEndpoint)
			d := f.seedDelivery(t, deliverySeed{
				url:    "https://example.test/hook",
				secret: secret,
			})
			outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
			f.publishPointer(t, d.id, outboxID, 1)

			cfg := testWorkerConfig(1)
			runtime, _, _ := f.newWorker(t, cfg, client)
			run := startConsumer(t, f.newConsumer(t, cfg, runtime))
			eventually(t, 6*time.Second, "endpoint identity disposition", func() error {
				row := f.readDelivery(t, d.id)
				if row.state != tc.wantState {
					return &stateError{got: row.state, attempts: row.attemptCount, want: tc.wantState}
				}
				if tc.wantMetric != "" && f.metricValue(t, tc.wantMetric) < 1 {
					return &metricCountError{name: tc.wantMetric, got: f.metricValue(t, tc.wantMetric)}
				}
				info := f.queueInfo(t, messaging.DispatchQueue)
				if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
					return &queueStateError{info: info}
				}
				return nil
			})
			run.stop(t)

			if got := transport.count(); got != int64(tc.wantHTTP) {
				t.Fatalf("HTTP requests = %d, want %d", got, tc.wantHTTP)
			}
			if tc.wantHTTP == 0 {
				if f.attemptCount(t, d.id) != 0 {
					t.Fatalf("identity mismatch wrote attempt rows")
				}
				if got := f.metricValue(t, "hookflow_worker_acked_total"); got != 1 {
					t.Fatalf("identity mismatch ACK count = %d, want 1", got)
				}
				return
			}

			header, body := transport.captured()
			signed := signedWireRequest{
				id:        header.Get(signing.HeaderID),
				timestamp: header.Get(signing.HeaderTimestamp),
				signature: header.Get(signing.HeaderSignature),
				body:      string(body),
			}
			if signed.id != d.id {
				t.Fatalf("webhook-id = %q, want delivery id %q", signed.id, d.id)
			}
			if header.Get("X-HookFlow-Delivery-Id") != d.id {
				t.Fatalf("X-HookFlow-Delivery-Id = %q, want %q", header.Get("X-HookFlow-Delivery-Id"), d.id)
			}
			if !bodyContainsEventIdentity(t, f, d, signed.body) {
				t.Fatal("transmitted body does not match the durable event payload")
			}
			if err := signing.VerifyWithTolerance([]byte("integration-webhook-secret-0123456789"),
				signed.id, signed.timestamp, []byte(signed.body), signed.signature, time.Now(), 5*time.Minute); err != nil {
				t.Fatalf("transmitted signature did not verify with the exact stored secret: %v", err)
			}
			row := f.readDelivery(t, d.id)
			if row.attemptCount != 1 || f.attemptCount(t, d.id) != 1 || row.leaseToken != "" {
				t.Fatalf("successful signed delivery state = %+v", row)
			}
		})
	}
}

func bodyContainsEventIdentity(t *testing.T, f *fixture, d seededDelivery, body string) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var payload string
	if err := f.pool.QueryRow(ctx,
		`SELECT payload::text FROM events WHERE id = $1::uuid`, d.eventID).Scan(&payload); err != nil {
		t.Fatalf("read durable event payload: %v", err)
	}
	return payload == body
}

// TestWorkerSecretResolutionHoldsNoDatabaseLockIntegration blocks inside the
// provider and proves the delivery row is not locked while provider I/O is in
// flight: the preparation transaction committed before the provider call.
func TestWorkerSecretResolutionHoldsNoDatabaseLockIntegration(t *testing.T) {
	f := newFixture(t)
	transport := newRecordingTransport(nil)
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	started := make(chan struct{})
	release := make(chan struct{})
	var startOnce sync.Once
	provider := &providerBehavior{base: f.provider, unwrap: func(ctx context.Context, wrapped kek.Wrapped, c kek.Context) ([]byte, error) {
		startOnce.Do(func() { close(started) })
		select {
		case <-release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return f.provider.Unwrap(ctx, wrapped, c)
	}}

	d := f.seedDelivery(t, deliverySeed{
		url:    "https://example.test/hook",
		secret: f.sealSecret(t, []byte("integration-webhook-secret-0123456789")),
	})
	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, d.id, outboxID, 1)

	cfg := testWorkerConfig(1)
	cfg.LeaseDuration = 5 * time.Second
	cfg.SecretResolutionBudget = 2 * time.Second
	runtime, _, _ := f.newWorkerWith(t, cfg, client, testSecurityConfig(), provider, nil)
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))

	select {
	case <-started:
	case <-time.After(6 * time.Second):
		t.Fatal("provider resolution did not start")
	}

	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	tx, err := f.pool.Begin(ctx)
	if err != nil {
		cancel()
		t.Fatalf("begin lock probe transaction: %v", err)
	}
	var one int
	if err := tx.QueryRow(ctx,
		`SELECT 1 FROM deliveries WHERE organization_id = $1::uuid AND id = $2::uuid FOR UPDATE NOWAIT`,
		f.orgID, d.id).Scan(&one); err != nil {
		_ = tx.Rollback(ctx)
		cancel()
		t.Fatalf("delivery row was locked during provider I/O: %v", err)
	}
	_ = tx.Rollback(ctx)
	cancel()

	if got := transport.count(); got != 0 {
		t.Fatalf("HTTP started before secret resolution completed: %d request(s)", got)
	}
	if got := f.attemptCount(t, d.id); got != 0 {
		t.Fatalf("attempt row was written before secret resolution completed: %d", got)
	}

	close(release)
	eventually(t, 8*time.Second, "blocked resolution to complete and deliver", func() error {
		row := f.readDelivery(t, d.id)
		if row.state != "succeeded" || row.attemptCount != 1 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded"}
		}
		return nil
	})
	run.stop(t)

	if got := transport.count(); got != 1 {
		t.Fatalf("HTTP requests after resolution = %d, want 1", got)
	}
	if got := f.attemptCount(t, d.id); got != 1 {
		t.Fatalf("attempt rows after success = %d, want 1", got)
	}
}
