//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/metamin44/hookflow/internal/app/disposition"
	"github.com/metamin44/hookflow/internal/app/worker"
	"github.com/metamin44/hookflow/internal/platform/messaging"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

type capturedHTTPRequest struct {
	method  string
	body    string
	headers http.Header
}

type inspectingProcessor struct {
	delegate worker.Processor
	bodies   chan []byte
}

func (p *inspectingProcessor) Process(ctx context.Context, handle *worker.Handle) disposition.Decision {
	p.bodies <- handle.Body()
	return p.delegate.Process(ctx, handle)
}

func TestWorkerConsumerHTTPDispositionIntegration(t *testing.T) {
	tests := []struct {
		name       string
		status     int
		wantState  string
		wantResult string
		wantRetry  bool
	}{
		{name: "2xx durable success then ACK", status: http.StatusAccepted, wantState: "succeeded", wantResult: "success"},
		{name: "retryable 500 schedules N+1 then ACK", status: http.StatusInternalServerError, wantState: "retrying", wantResult: "retryable_failure", wantRetry: true},
		{name: "terminal 400 has no retry", status: http.StatusBadRequest, wantState: "failed", wantResult: "terminal_failure"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			var requests atomic.Int64
			received := make(chan capturedHTTPRequest, 2)
			payload := `{"source":"real-http-integration"}`
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				body, _ := io.ReadAll(r.Body)
				received <- capturedHTTPRequest{method: r.Method, body: string(body), headers: r.Header.Clone()}
				w.WriteHeader(tc.status)
			}))
			t.Cleanup(server.Close)

			d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL, payload: payload})
			// JSONB normalizes whitespace. The wire body must match the actual
			// durable snapshot, not the original pre-insert JSON spelling.
			payloadCtx, payloadCancel := context.WithTimeout(context.Background(), queryTimeout)
			payloadErr := f.pool.QueryRow(payloadCtx,
				`SELECT payload::text FROM events WHERE id = $1::uuid`, d.eventID).Scan(&payload)
			payloadCancel()
			if payloadErr != nil {
				t.Fatalf("read durable HTTP payload: %v", payloadErr)
			}
			outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
			f.publishPointer(t, d.id, outboxID, 1)
			cfg := testWorkerConfig(1)
			runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
			inspector := &inspectingProcessor{delegate: runtime, bodies: make(chan []byte, 2)}
			consumer := f.newConsumer(t, cfg, inspector)
			run := startConsumer(t, consumer)

			eventually(t, 6*time.Second, "durable HTTP disposition and broker ACK", func() error {
				if row := f.readDelivery(t, d.id); row.state != tc.wantState || row.attemptCount != 1 {
					return &stateError{got: row.state, attempts: row.attemptCount, want: tc.wantState}
				}
				info := f.queueInfo(t, messaging.DispatchQueue)
				if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
					return &queueStateError{info: info}
				}
				return nil
			})
			run.stop(t)

			if got := requests.Load(); got != 1 {
				t.Fatalf("HTTP request count = %d, want 1", got)
			}
			request := <-received
			if request.method != http.MethodPost || request.body != payload {
				t.Fatalf("HTTP request = %s %q, want POST %q", request.method, request.body, payload)
			}
			if request.headers.Get("X-HookFlow-Delivery-Id") != d.id ||
				request.headers.Get("X-HookFlow-Event-Id") != d.eventID ||
				request.headers.Get("X-HookFlow-Dispatch-Sequence") != "1" ||
				request.headers.Get("X-HookFlow-Signature") != "" {
				t.Fatalf("unexpected HTTP delivery headers: %v", request.headers)
			}
			body := <-inspector.bodies
			var envelope messaging.Envelope
			if err := json.Unmarshal(body, &envelope); err != nil {
				t.Fatalf("worker received invalid envelope JSON: %v", err)
			}
			if err := messaging.ValidateEnvelope(envelope); err != nil {
				t.Fatalf("worker received invalid envelope: %v", err)
			}
			if envelope.DeliveryID != d.id || envelope.OutboxID != outboxID || envelope.DispatchSeq != 1 {
				t.Fatalf("worker received wrong pointer: %+v", envelope)
			}
			var fields map[string]json.RawMessage
			if err := json.Unmarshal(body, &fields); err != nil || len(fields) != 5 {
				t.Fatalf("worker envelope was not identifiers-only: fields=%v error=%v", fields, err)
			}

			row := f.readDelivery(t, d.id)
			if !row.lastAttemptAt.Valid || !row.lastResponseStatus.Valid ||
				int(row.lastResponseStatus.Int32) != tc.status || row.leaseToken != "" ||
				row.completedAt.Valid == tc.wantRetry {
				t.Fatalf("unexpected completed delivery: %+v", row)
			}
			if got := f.attemptOutcome(t, d.id); got != tc.wantResult {
				t.Fatalf("stored attempt outcome = %q, want %q", got, tc.wantResult)
			}
			if got := f.metricValue(t, "hookflow_worker_acked_total"); got != 1 {
				t.Fatalf("worker ACK metric = %d, want 1", got)
			}
			outboxes := f.readOutboxes(t, d.id)
			if tc.wantRetry {
				if row.dispatchSeq != 2 || !row.nextAttemptAt.Valid || !row.nextAttemptAt.Time.After(time.Now()) {
					t.Fatalf("retry did not advance generation and schedule next attempt: %+v", row)
				}
				if len(outboxes) != 2 || outboxes[0].status != "published" ||
					outboxes[1].sequence != 2 || outboxes[1].status != "pending" || outboxes[1].attempts != 0 {
					t.Fatalf("retry outbox rows = %+v", outboxes)
				}
			} else {
				if row.dispatchSeq != 1 || row.nextAttemptAt.Valid || len(outboxes) != 1 || outboxes[0].status != "published" {
					t.Fatalf("non-retry disposition created a retry: delivery=%+v outboxes=%+v", row, outboxes)
				}
			}
		})
	}
}

func TestWorkerInvalidHTTPStatusTerminalCompletionIntegration(t *testing.T) {
	f := newFixture(t)
	transport := newRecordingTransport(nil)
	transport.status = 600
	client := &http.Client{Transport: transport, Timeout: 2 * time.Second}

	d := f.seedDelivery(t, deliverySeed{})
	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, d.id, outboxID, 1)
	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, client)
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))

	eventually(t, 6*time.Second, "invalid status to terminalize and ACK", func() error {
		row := f.readDelivery(t, d.id)
		if row.state != "failed" || row.attemptCount != 1 || !row.completedAt.Valid ||
			row.nextAttemptAt.Valid || row.lastResponseStatus.Valid || row.leaseToken != "" {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "failed with one nil-status attempt"}
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)

	if got := transport.count(); got != 1 {
		t.Fatalf("HTTP requests = %d, want exactly 1", got)
	}
	if got := f.attemptCount(t, d.id); got != 1 {
		t.Fatalf("delivery_attempts rows = %d, want 1", got)
	}
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	defer cancel()
	var statusNil bool
	var outcome, errorClass, errorMessage string
	if err := f.pool.QueryRow(ctx, `SELECT response_status IS NULL, outcome, error_class, error_message
		FROM delivery_attempts WHERE delivery_id = $1::uuid`, d.id).Scan(&statusNil, &outcome, &errorClass, &errorMessage); err != nil {
		t.Fatalf("read invalid-status attempt: %v", err)
	}
	if !statusNil || outcome != "terminal_failure" || errorClass != "http_status" ||
		errorMessage != "HTTP endpoint returned an invalid response status" {
		t.Fatalf("invalid-status attempt = nil=%v outcome=%q class=%q message=%q", statusNil, outcome, errorClass, errorMessage)
	}
	row := f.readDelivery(t, d.id)
	if !row.lastAttemptAt.Valid || !row.lastError.Valid || !strings.Contains(row.lastError.String, "invalid response status") {
		t.Fatalf("delivery diagnostic = %+v", row)
	}
	if outboxes := f.readOutboxes(t, d.id); len(outboxes) != 1 || outboxes[0].status != "published" {
		t.Fatalf("invalid status created a retry outbox row: %+v", outboxes)
	}
	if got := f.metricValue(t, "hookflow_worker_uncertain_total"); got != 0 {
		t.Fatalf("invalid status was classified as uncertain: %d", got)
	}
}

type stateError struct {
	got      string
	attempts int
	want     string
}

func (e *stateError) Error() string {
	return "delivery state=" + e.got + " attempts=" + strconv.Itoa(e.attempts) + ", want " + e.want + " with one attempt"
}

type queueStateError struct {
	info queueInfo
}

func (e *queueStateError) Error() string {
	return "dispatch queue state: ready=" + strconv.Itoa(e.info.MessagesReady) +
		" unacknowledged=" + strconv.Itoa(e.info.MessagesUnacknowledged)
}

func TestWorkerConsumerSelectsExactBNotAIntegration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	received := make(chan string, 2)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		received <- r.Header.Get("X-HookFlow-Delivery-Id")
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	a := f.seedDelivery(t, deliverySeed{url: deliveryTestURL, nextAttemptAt: time.Now().Add(time.Hour)})
	b := f.seedDelivery(t, deliverySeed{url: deliveryTestURL, nextAttemptAt: time.Now().Add(-time.Minute)})
	aOutbox := f.seedOutbox(t, a.id, 1, "published", time.Time{}, time.Time{})
	bOutbox := f.seedOutbox(t, b.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, a.id, aOutbox, 1)
	f.publishPointer(t, b.id, bOutbox, 1)
	beforeA := f.readDelivery(t, a.id)

	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	inspector := &inspectingProcessor{delegate: runtime, bodies: make(chan []byte, 2)}
	run := startConsumer(t, f.newConsumer(t, cfg, inspector))
	eventually(t, 6*time.Second, "exact delivery B to complete", func() error {
		if row := f.readDelivery(t, b.id); row.state != "succeeded" {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded"}
		}
		if info := f.queueInfo(t, messaging.DispatchQueue); info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)

	if got := requests.Load(); got != 1 {
		t.Fatalf("HTTP request count = %d, want only B", got)
	}
	if got := <-received; got != b.id {
		t.Fatalf("HTTP delivery ID = %q, want B %q", got, b.id)
	}
	if after := f.readDelivery(t, a.id); after != beforeA {
		t.Fatalf("not-due delivery A was mutated: before=%+v after=%+v", beforeA, after)
	}
	if f.attemptCount(t, a.id) != 0 || f.attemptCount(t, b.id) != 1 {
		t.Fatalf("attempt counts A=%d B=%d, want 0 and 1", f.attemptCount(t, a.id), f.attemptCount(t, b.id))
	}
	seen := map[string]bool{}
	for range 2 {
		var envelope messaging.Envelope
		if err := json.Unmarshal(<-inspector.bodies, &envelope); err != nil {
			t.Fatalf("decode consumed envelope: %v", err)
		}
		seen[envelope.DeliveryID] = true
	}
	if !seen[a.id] || !seen[b.id] || len(seen) != 2 {
		t.Fatalf("consumer did not handle the exact published pointers: %v", seen)
	}
}

func TestWorkerConsumerStaleAndDuplicatePointersIntegration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	leaseToken := mustID(t)
	d := f.seedDelivery(t, deliverySeed{
		url: deliveryTestURL, dispatchSeq: 2, leaseToken: leaseToken,
		leaseExpiresAt: time.Now().Add(time.Hour),
	})
	oldOutbox := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	currentOutbox := f.seedOutbox(t, d.id, 2, "published", time.Time{}, time.Time{})
	f.publishPointer(t, d.id, oldOutbox, 1)
	f.publishPointer(t, d.id, currentOutbox, 2)
	before := f.readDelivery(t, d.id)
	beforeOutboxes := f.readOutboxes(t, d.id)

	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 6*time.Second, "stale and duplicate pointers to be durably rejected", func() error {
		if got := f.metricValue(t, "hookflow_worker_processed_total"); got < 2 {
			return &processedCountError{got: got}
		}
		if info := f.queueInfo(t, messaging.DispatchQueue); info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)

	if requests.Load() != 0 || f.attemptCount(t, d.id) != 0 {
		t.Fatalf("stale/duplicate work reached HTTP or wrote attempts: requests=%d attempts=%d", requests.Load(), f.attemptCount(t, d.id))
	}
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("stale/duplicate pointer mutated delivery: before=%+v after=%+v", before, after)
	}
	after := f.readOutboxes(t, d.id)
	if len(after) != len(beforeOutboxes) {
		t.Fatalf("stale/duplicate pointer changed outbox rows: before=%+v after=%+v", beforeOutboxes, after)
	}
	for i := range beforeOutboxes {
		if beforeOutboxes[i] != after[i] {
			t.Fatalf("stale/duplicate pointer mutated outbox row %d: before=%+v after=%+v", i, beforeOutboxes[i], after[i])
		}
	}
	if stale := f.metricValue(t, "hookflow_worker_stale_total"); stale != 1 {
		t.Fatalf("stale metric = %d, want 1", stale)
	}
	if duplicate := f.metricValue(t, "hookflow_worker_duplicate_total"); duplicate != 1 {
		t.Fatalf("duplicate metric = %d, want 1", duplicate)
	}
	if acked := f.metricValue(t, "hookflow_worker_acked_total"); acked != 2 {
		t.Fatalf("ACK metric = %d, want 2", acked)
	}
}

type processedCountError struct{ got int64 }

func (e *processedCountError) Error() string {
	return "processed metric=" + strconv.FormatInt(e.got, 10) + ", want at least 2"
}

type signedWireRequest struct {
	id        string
	timestamp string
	signature string
	body      string
}

func TestWorkerConsumerPermanentSecretFailureACKsWithoutHTTPIntegration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL, secret: "hfse1.not-a-valid-envelope"})
	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, d.id, outboxID, 1)
	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 6*time.Second, "permanent secret failure to terminalize and ACK", func() error {
		row := f.readDelivery(t, d.id)
		if row.state != "failed" || !row.completedAt.Valid || row.nextAttemptAt.Valid || row.attemptCount != 0 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "failed without an attempt"}
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)

	if requests.Load() != 0 {
		t.Fatalf("permanent secret failure reached HTTP %d time(s)", requests.Load())
	}
	if n := f.attemptCount(t, d.id); n != 0 {
		t.Fatalf("permanent secret failure wrote %d attempt rows", n)
	}
	if outboxes := f.readOutboxes(t, d.id); len(outboxes) != 1 || outboxes[0].status != "published" {
		t.Fatalf("permanent secret failure created a retry: %+v", outboxes)
	}
	if got := f.metricValue(t, "hookflow_worker_secret_invalid_total"); got != 1 {
		t.Fatalf("secret_invalid metric = %d, want 1", got)
	}
	if got := f.metricValue(t, "hookflow_worker_acked_total"); got != 1 {
		t.Fatalf("ACK metric = %d, want 1", got)
	}
}

func TestWorkerConsumerSignedDeliveryEndToEndIntegration(t *testing.T) {
	f := newFixture(t)
	secret := []byte("integration-webhook-secret-0123456789")
	var requests atomic.Int64
	received := make(chan signedWireRequest, 4)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- signedWireRequest{
			id:        r.Header.Get(signing.HeaderID),
			timestamp: r.Header.Get(signing.HeaderTimestamp),
			signature: r.Header.Get(signing.HeaderSignature),
			body:      string(body),
		}
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)

	d := f.seedDelivery(t, deliverySeed{
		url: deliveryTestURL, payload: `{"source":"signed-integration"}`,
		secret: f.sealSecret(t, secret),
	})
	payload := `{"source":"signed-integration"}`
	ctx, cancel := context.WithTimeout(context.Background(), queryTimeout)
	payloadErr := f.pool.QueryRow(ctx,
		`SELECT payload::text FROM events WHERE id = $1::uuid`, d.eventID).Scan(&payload)
	cancel()
	if payloadErr != nil {
		t.Fatalf("read durable signed HTTP payload: %v", payloadErr)
	}

	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	f.publishPointer(t, d.id, outboxID, 1)

	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 6*time.Second, "first signed attempt to schedule its retry", func() error {
		if row := f.readDelivery(t, d.id); row.state != "retrying" || row.attemptCount != 1 || row.dispatchSeq != 2 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "retrying at generation 2"}
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)

	first := <-received
	firstUnix, parseErr := strconv.ParseInt(first.timestamp, 10, 64)
	if parseErr != nil {
		t.Fatalf("first webhook-timestamp %q is not Unix seconds: %v", first.timestamp, parseErr)
	}
	if first.id != d.id {
		t.Fatalf("first webhook-id = %q, want stable delivery id %q", first.id, d.id)
	}
	if first.body != payload {
		t.Fatalf("first signed body = %q, want %q", first.body, payload)
	}
	if err := signing.VerifyWithTolerance(secret, first.id, first.timestamp, []byte(first.body),
		first.signature, time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("first transmitted signature did not verify: %v", err)
	}

	// A retry must carry a fresh timestamp, so let the wall clock cross the
	// next second before the recorded retry pointer is published.
	eventually(t, 2*time.Second, "the next wall-clock second for a fresh timestamp", func() error {
		if time.Now().Unix() <= firstUnix {
			return errors.New("timestamp second has not advanced yet")
		}
		return nil
	})
	// The approved retry ladder schedules the second attempt seconds ahead.
	// Publishing before it is due would be rejected as not-due work.
	eventually(t, 20*time.Second, "durable retry attempt to become due", func() error {
		row := f.readDelivery(t, d.id)
		if !row.nextAttemptAt.Valid {
			return errors.New("retry has no durable next attempt time")
		}
		if time.Now().Before(row.nextAttemptAt.Time) {
			return errors.New("retry is not due yet")
		}
		return nil
	})

	outboxes := f.readOutboxes(t, d.id)
	if len(outboxes) != 2 || outboxes[1].sequence != 2 || outboxes[1].status != "pending" {
		t.Fatalf("first attempt did not leave one pending retry outbox: %+v", outboxes)
	}
	ctx, cancel = context.WithTimeout(context.Background(), queryTimeout)
	_, updateErr := f.pool.Exec(ctx,
		`UPDATE outbox_messages
		    SET status = 'published', published_at = clock_timestamp(), updated_at = clock_timestamp()
		  WHERE id = $1::uuid`, outboxes[1].id)
	cancel()
	if updateErr != nil {
		t.Fatalf("mark own retry outbox published: %v", updateErr)
	}
	f.publishPointer(t, d.id, outboxes[1].id, 2)

	retryRun := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 8*time.Second, "retry signed attempt to succeed", func() error {
		if row := f.readDelivery(t, d.id); row.state != "succeeded" || row.attemptCount != 2 {
			return &stateError{got: row.state, attempts: row.attemptCount, want: "succeeded"}
		}
		info := f.queueInfo(t, messaging.DispatchQueue)
		if info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	retryRun.stop(t)

	second := <-received
	if got := requests.Load(); got != 2 {
		t.Fatalf("HTTP request count = %d, want 2 signed attempts", got)
	}
	secondUnix, parseErr := strconv.ParseInt(second.timestamp, 10, 64)
	if parseErr != nil {
		t.Fatalf("retry webhook-timestamp %q is not Unix seconds: %v", second.timestamp, parseErr)
	}
	if second.id != d.id {
		t.Fatalf("retry webhook-id = %q, want the stable delivery id %q", second.id, d.id)
	}
	if secondUnix <= firstUnix {
		t.Fatalf("retry timestamp %d did not advance past the first attempt %d", secondUnix, firstUnix)
	}
	if second.signature == first.signature {
		t.Fatal("retry reused the first attempt signature")
	}
	if second.body != payload {
		t.Fatalf("retry signed body = %q, want %q", second.body, payload)
	}
	if err := signing.VerifyWithTolerance(secret, second.id, second.timestamp, []byte(second.body),
		second.signature, time.Now(), 5*time.Minute); err != nil {
		t.Fatalf("retry transmitted signature did not verify: %v", err)
	}

	row := f.readDelivery(t, d.id)
	if row.leaseToken != "" || !row.completedAt.Valid || row.nextAttemptAt.Valid {
		t.Fatalf("succeeded signed delivery retained live state: %+v", row)
	}
	ctx, cancel = context.WithTimeout(context.Background(), queryTimeout)
	var finalOutcome string
	outcomeErr := f.pool.QueryRow(ctx,
		`SELECT outcome FROM delivery_attempts
		  WHERE delivery_id = $1::uuid ORDER BY attempt_number DESC LIMIT 1`, d.id).Scan(&finalOutcome)
	cancel()
	if outcomeErr != nil {
		t.Fatalf("read final stored attempt outcome: %v", outcomeErr)
	}
	if finalOutcome != "success" {
		t.Fatalf("final stored attempt outcome = %q, want success", finalOutcome)
	}
	if got := f.metricValue(t, "hookflow_worker_succeeded_total"); got != 1 {
		t.Fatalf("succeeded metric = %d, want 1", got)
	}

	audits := f.attemptHeaders(t, d.id)
	if len(audits) != 2 {
		t.Fatalf("attempt request-header audits = %d, want 2", len(audits))
	}
	transmitted := []signedWireRequest{first, second}
	for i, audit := range audits {
		var recorded map[string]string
		if err := json.Unmarshal([]byte(audit), &recorded); err != nil {
			t.Fatalf("attempt %d request headers are not a JSON object: %v", i+1, err)
		}
		if len(recorded) != 3 {
			t.Fatalf("attempt %d audit recorded %d headers, want exactly the 3 signing headers: %s", i+1, len(recorded), audit)
		}
		if recorded[signing.HeaderID] != transmitted[i].id ||
			recorded[signing.HeaderTimestamp] != transmitted[i].timestamp ||
			recorded[signing.HeaderSignature] != transmitted[i].signature {
			t.Fatalf("attempt %d audit does not match the wire headers: audit=%s wire=%+v", i+1, audit, transmitted[i])
		}
		if strings.Contains(audit, string(secret)) {
			t.Fatalf("attempt %d audit leaked the webhook secret", i+1)
		}
	}
}

func TestWorkerConsumerMalformedPointerACKIntegration(t *testing.T) {
	f := newFixture(t)
	var requests atomic.Int64
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusNoContent)
	}))
	t.Cleanup(server.Close)
	d := f.seedDelivery(t, deliverySeed{url: deliveryTestURL})
	outboxID := f.seedOutbox(t, d.id, 1, "published", time.Time{}, time.Time{})
	before := f.readDelivery(t, d.id)
	beforeOutboxes := f.readOutboxes(t, d.id)
	f.publishMalformed(t, `{"malformed":"`+mustID(t)+`"}`)

	cfg := testWorkerConfig(1)
	runtime, _, _ := f.newWorker(t, cfg, pinnedClient(t, server))
	run := startConsumer(t, f.newConsumer(t, cfg, runtime))
	eventually(t, 6*time.Second, "malformed pointer to be ACKed without HTTP", func() error {
		if got := f.metricValue(t, "hookflow_worker_acked_total"); got != 1 {
			return &processedCountError{got: got}
		}
		if info := f.queueInfo(t, messaging.DispatchQueue); info.MessagesReady != 0 || info.MessagesUnacknowledged != 0 {
			return &queueStateError{info: info}
		}
		return nil
	})
	run.stop(t)

	if requests.Load() != 0 || f.attemptCount(t, d.id) != 0 {
		t.Fatalf("malformed pointer reached HTTP or wrote attempts: requests=%d attempts=%d", requests.Load(), f.attemptCount(t, d.id))
	}
	if after := f.readDelivery(t, d.id); after != before {
		t.Fatalf("malformed pointer mutated delivery: before=%+v after=%+v", before, after)
	}
	if after := f.readOutboxes(t, d.id); len(after) != 1 || after[0].id != outboxID || after[0] != beforeOutboxes[0] {
		t.Fatalf("malformed pointer mutated outbox: before=%+v after=%+v", beforeOutboxes, after)
	}
}
