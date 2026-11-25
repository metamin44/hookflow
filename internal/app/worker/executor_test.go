package worker

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/platform/destination"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

type executorRoundTripFunc func(*http.Request) (*http.Response, error)

func (f executorRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

type fakeRequestSigner struct {
	headers signing.Headers
	err     error
	calls   int
	id      string
	secret  []byte
	body    []byte
}

func (s *fakeRequestSigner) Sign(deliveryID string, secret, body []byte) (signing.Headers, error) {
	s.calls++
	s.id = deliveryID
	s.secret = append([]byte(nil), secret...)
	s.body = append([]byte(nil), body...)
	return s.headers, s.err
}

func defaultTestSigner() *fakeRequestSigner {
	return &fakeRequestSigner{headers: signing.Headers{
		ID:        "01900000-0000-7000-8000-000000000001",
		Timestamp: "1700000000",
		Signature: "v1,AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA",
	}}
}

func executorTestOptions() HTTPOptions {
	return HTTPOptions{
		Deadline:          2 * time.Second,
		HandshakeBudget:   500 * time.Millisecond,
		PostProcessBudget: 500 * time.Millisecond,
		ShutdownReserve:   500 * time.Millisecond,
		ResponseBodyLimit: 4096,
	}
}

func executorTestPreparation(requestURL string) delivery.HTTPPreparation {
	return delivery.HTTPPreparation{
		DeliveryID:     "delivery-id",
		EventID:        "event-id",
		DispatchSeq:    7,
		Remaining:      30 * time.Second,
		RequestURL:     requestURL,
		RequestHeaders: `{"Authorization":"Bearer request-secret","X-Custom":"snapshot"}`,
		EventPayload:   `{"payload":"raw-event"}`,
	}
}

// executorTestURL is a policy-valid public destination. Tests pair it with a
// client that dials the local TLS fixture instead of resolving example.com.
const executorTestURL = "https://example.com/hook"

func executorTestClient(t *testing.T, server *httptest.Server) *http.Client {
	t.Helper()
	base, ok := server.Client().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("test server transport has type %T", server.Client().Transport)
	}
	transport := base.Clone()
	address := server.Listener.Addr().String()
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, address)
	}
	return &http.Client{Transport: transport}
}

func newExecutorForTest(t *testing.T, opts HTTPOptions) *Executor {
	t.Helper()
	return newExecutorWithSigner(t, opts, defaultTestSigner())
}

func newExecutorWithSigner(t *testing.T, opts HTTPOptions, signer RequestSigner) *Executor {
	t.Helper()
	executor, err := NewExecutor(opts, signer, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	return executor
}

func TestHTTPOptionsValidateAndSafeDefault(t *testing.T) {
	t.Parallel()

	valid := executorTestOptions()
	tests := []struct {
		name   string
		mutate func(*HTTPOptions)
	}{
		{name: "deadline", mutate: func(o *HTTPOptions) { o.Deadline = 0 }},
		{name: "handshake", mutate: func(o *HTTPOptions) { o.HandshakeBudget = -time.Second }},
		{name: "post process", mutate: func(o *HTTPOptions) { o.PostProcessBudget = 0 }},
		{name: "shutdown", mutate: func(o *HTTPOptions) { o.ShutdownReserve = -time.Second }},
		{name: "body limit", mutate: func(o *HTTPOptions) { o.ResponseBodyLimit = 0 }},
		{name: "negative body limit", mutate: func(o *HTTPOptions) { o.ResponseBodyLimit = -1 }},
		{name: "overflow", mutate: func(o *HTTPOptions) { o.Deadline = time.Duration(1<<63 - 1) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := valid
			tt.mutate(&opts)
			if err := opts.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}

	if err := valid.Validate(); err != nil {
		t.Fatal(err)
	}
	wantBudget := valid.Deadline + valid.HandshakeBudget + valid.PostProcessBudget + valid.ShutdownReserve
	if got := valid.RequiredLeaseBudget(); got != wantBudget {
		t.Fatalf("required lease budget = %s, want %s", got, wantBudget)
	}
	if valid.Client == nil {
		t.Fatal("safe default client was not installed")
	}
	transport, ok := valid.Client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("default transport has type %T", valid.Client.Transport)
	}
	if transport.TLSClientConfig == nil || transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		t.Fatal("default transport does not require TLS 1.2 or newer")
	}
	if transport.DialContext == nil || transport.TLSHandshakeTimeout <= 0 || transport.ResponseHeaderTimeout <= 0 {
		t.Fatal("default transport is missing connect or response-header timeouts")
	}
	if transport.Proxy != nil {
		t.Fatal("default transport honors proxy environment configuration")
	}
	if !transport.DisableKeepAlives {
		t.Fatal("default transport could reuse a connection without revalidating the destination")
	}
	request := httptest.NewRequest(http.MethodGet, "https://example.test", nil)
	if err := valid.Client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect policy error = %v, want ErrUseLastResponse", err)
	}
}

func TestExecutorHTTPStatusClassification(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status int
		kind   delivery.OutcomeKind
	}{
		{status: 200, kind: delivery.OutcomeSuccess},
		{status: 202, kind: delivery.OutcomeSuccess},
		{status: 204, kind: delivery.OutcomeSuccess},
		{status: 299, kind: delivery.OutcomeSuccess},
		{status: 300, kind: delivery.OutcomeTerminalFailure},
		{status: 302, kind: delivery.OutcomeTerminalFailure},
		{status: 307, kind: delivery.OutcomeTerminalFailure},
		{status: 399, kind: delivery.OutcomeTerminalFailure},
		{status: 400, kind: delivery.OutcomeTerminalFailure},
		{status: 404, kind: delivery.OutcomeTerminalFailure},
		{status: 422, kind: delivery.OutcomeTerminalFailure},
		{status: 408, kind: delivery.OutcomeRetryableFailure},
		{status: 429, kind: delivery.OutcomeRetryableFailure},
		{status: 500, kind: delivery.OutcomeRetryableFailure},
		{status: 503, kind: delivery.OutcomeRetryableFailure},
		{status: 599, kind: delivery.OutcomeRetryableFailure},
	}
	for _, tt := range tests {
		t.Run(strconv.Itoa(tt.status), func(t *testing.T) {
			t.Parallel()
			type capturedRequest struct {
				method string
				body   string
				header http.Header
			}
			received := make(chan capturedRequest, 1)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				received <- capturedRequest{method: r.Method, body: string(body), header: r.Header.Clone()}
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Retry-After", "7")
				w.Header().Set("X-Secret", "header-secret")
				w.WriteHeader(tt.status)
				if tt.status != http.StatusNoContent {
					_, _ = io.WriteString(w, `{"password":"response-secret"}`)
				}
			}))
			t.Cleanup(server.Close)

			opts := executorTestOptions()
			opts.Client = executorTestClient(t, server)
			executor := newExecutorForTest(t, opts)
			preparation := executorTestPreparation(executorTestURL)
			outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
			if err != nil {
				t.Fatalf("Execute returned error: %v", err)
			}
			if outcome.Kind != tt.kind {
				t.Fatalf("kind = %s, want %s", outcome.Kind, tt.kind)
			}
			if outcome.ResponseStatus == nil || *outcome.ResponseStatus != tt.status {
				t.Fatalf("response status = %v, want %d", outcome.ResponseStatus, tt.status)
			}
			if outcome.RequestMethod != http.MethodPost || outcome.StartedAt.IsZero() ||
				outcome.FinishedAt.Before(outcome.StartedAt) || outcome.Duration != outcome.FinishedAt.Sub(outcome.StartedAt) {
				t.Fatalf("incomplete outcome metadata: %+v", outcome)
			}
			if tt.kind == delivery.OutcomeSuccess {
				if outcome.ErrorClass != "" || outcome.ErrorMessage != "" {
					t.Fatalf("success contains error metadata: %+v", outcome)
				}
			} else if outcome.ErrorClass != "http_status" || outcome.ErrorMessage == "" {
				t.Fatalf("missing HTTP error metadata: %+v", outcome)
			}

			select {
			case request := <-received:
				if request.method != http.MethodPost || request.body != preparation.EventPayload {
					t.Fatalf("request = %s %q, want POST with raw payload", request.method, request.body)
				}
				for name, want := range map[string]string{
					"Content-Type":                 "application/json",
					"X-HookFlow-Delivery-Id":       preparation.DeliveryID,
					"X-HookFlow-Event-Id":          preparation.EventID,
					"X-HookFlow-Dispatch-Sequence": "7",
					"Authorization":                "Bearer request-secret",
					"X-Custom":                     "snapshot",
				} {
					if got := request.header.Get(name); got != want {
						t.Errorf("request header %s = %q, want %q", name, got, want)
					}
				}
				if request.header.Get("X-HookFlow-Signature") != "" {
					t.Fatal("executor added an unapproved signature header")
				}
				for _, name := range []string{signing.HeaderID, signing.HeaderTimestamp, signing.HeaderSignature} {
					if got := request.header.Get(name); got != "" {
						t.Fatalf("unsigned delivery transmitted %s = %q", name, got)
					}
				}
			default:
				t.Fatal("HTTP handler did not receive the request")
			}

			var safeHeaders map[string]string
			if err := json.Unmarshal([]byte(outcome.ResponseHeaders), &safeHeaders); err != nil {
				t.Fatalf("response headers are not JSON: %v", err)
			}
			if len(safeHeaders) != 3 || safeHeaders["Content-Type"] != "text/plain" || safeHeaders["Retry-After"] != "7" || safeHeaders["Date"] == "" {
				t.Fatalf("unsafe or incomplete response headers: %#v", safeHeaders)
			}
			if strings.Contains(outcome.ResponseHeaders, "header-secret") {
				t.Fatal("response header secret was persisted")
			}
			if tt.status != http.StatusNoContent {
				if !strings.Contains(outcome.ResponseBodySnippet, "[REDACTED]") || strings.Contains(outcome.ResponseBodySnippet, "response-secret") {
					t.Fatalf("response snippet was not redacted: %q", outcome.ResponseBodySnippet)
				}
			}
		})
	}
}

func TestExecutorRejectsInvalidHeadersWithoutNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	executor := newExecutorForTest(t, executorTestOptions())

	for _, snapshot := range []string{
		`{`,
		`[]`,
		`null`,
		`"headers"`,
		`{"X-Test":1,"Authorization":"request-secret"}`,
		`{"X-Test":null}`,
		`{"X-Test":true}`,
		`{"X-Test":{}}`,
		`{} {}`,
		`{"Bad Name":"value"}`,
		`{"X-Test":"line\r\nInjected: value"}`,
	} {
		preparation := executorTestPreparation(executorTestURL)
		preparation.RequestHeaders = snapshot
		outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
		if err == nil {
			t.Errorf("headers %q were accepted", snapshot)
			continue
		}
		if strings.Contains(err.Error()+outcome.ErrorMessage, "request-secret") {
			t.Errorf("headers %q leaked a secret in the error", snapshot)
		}
	}
	if got := requests.Load(); got != 0 {
		t.Fatalf("invalid headers caused %d network requests", got)
	}
}

func TestExecutorUnresolvedSecretFailsClosed(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	executor := newExecutorForTest(t, executorTestOptions())
	preparation := executorTestPreparation(executorTestURL)
	preparation.SecretCiphertext = "ciphertext-that-must-never-be-sent"

	outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
	var preHTTP *delivery.PreHTTPError
	if !errors.As(err, &preHTTP) || preHTTP.Reason != delivery.ReasonSecretInvalid {
		t.Fatalf("error = %v, want a secret_invalid pre-HTTP failure", err)
	}
	if requests.Load() != 0 {
		t.Fatal("nonempty ciphertext caused a network request")
	}
	if outcome.Kind != delivery.OutcomeTerminalFailure {
		t.Fatalf("outcome kind = %s, want terminal failure", outcome.Kind)
	}
	if strings.Contains(err.Error()+outcome.ErrorMessage+outcome.ResponseBodySnippet, preparation.SecretCiphertext) {
		t.Fatal("ciphertext leaked into outcome diagnostics")
	}

	resolvedOnly := executorTestPreparation(executorTestURL)
	resolvedOnly.SecretPlaintext = []byte("resolved-without-envelope")
	if _, err := executor.Execute(context.Background(), resolvedOnly, http.MethodPost); !errors.As(err, &preHTTP) ||
		preHTTP.Reason != delivery.ReasonSecretInvalid {
		t.Fatalf("resolved secret without envelope error = %v, want secret_invalid", err)
	}
	if requests.Load() != 0 {
		t.Fatal("secret/envelope mismatch caused a network request")
	}
}

func TestExecutorSignsExactTransmittedBody(t *testing.T) {
	t.Parallel()

	type capturedRequest struct {
		body   string
		header http.Header
	}
	received := make(chan capturedRequest, 1)
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		received <- capturedRequest{body: string(body), header: r.Header.Clone()}
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)

	const deliveryID = "22222222-2222-4222-8222-222222222222"
	secret := []byte("0123456789abcdef-secret")
	preparation := executorTestPreparation(executorTestURL)
	preparation.DeliveryID = deliveryID
	preparation.SecretCiphertext = "hfse1.stored-envelope-placeholder"
	preparation.SecretPlaintext = secret

	signer := &fakeRequestSigner{headers: signing.Headers{
		ID:        deliveryID,
		Timestamp: "1700000000",
		Signature: "v1,BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
	}}
	opts := executorTestOptions()
	opts.Client = executorTestClient(t, server)
	executor := newExecutorWithSigner(t, opts, signer)
	outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
	if err != nil {
		t.Fatalf("Execute returned error: %v", err)
	}
	if outcome.Kind != delivery.OutcomeSuccess {
		t.Fatalf("kind = %s, want success", outcome.Kind)
	}
	if signer.calls != 1 {
		t.Fatalf("signer calls = %d, want 1", signer.calls)
	}
	if signer.id != deliveryID {
		t.Fatalf("signed id = %q, want the stable delivery id", signer.id)
	}
	if string(signer.secret) != string(secret) {
		t.Fatal("signer did not receive the exact decrypted secret bytes")
	}
	if string(signer.body) != preparation.EventPayload {
		t.Fatalf("signed body = %q, want the exact event payload", signer.body)
	}

	select {
	case request := <-received:
		if request.body != preparation.EventPayload {
			t.Fatalf("transmitted body = %q, want the exact event payload", request.body)
		}
		want := map[string]string{
			signing.HeaderID:        deliveryID,
			signing.HeaderTimestamp: "1700000000",
			signing.HeaderSignature: "v1,BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB",
		}
		for name, value := range want {
			if got := request.header.Get(name); got != value {
				t.Errorf("transmitted %s = %q, want %q", name, got, value)
			}
		}
	default:
		t.Fatal("HTTP handler did not receive the request")
	}

	var audit map[string]string
	if err := json.Unmarshal([]byte(outcome.RequestHeaders), &audit); err != nil {
		t.Fatalf("audit headers are not JSON: %v", err)
	}
	if len(audit) != 3 || audit[signing.HeaderID] != deliveryID ||
		audit[signing.HeaderTimestamp] != "1700000000" ||
		audit[signing.HeaderSignature] != "v1,BBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBBB" {
		t.Fatalf("audit headers = %#v", audit)
	}
	if strings.Contains(outcome.RequestHeaders, string(secret)) {
		t.Fatal("audit headers contain secret material")
	}
}

func TestExecutorPreHTTPFailuresNeverSend(t *testing.T) {
	t.Parallel()

	const deliveryID = "22222222-2222-4222-8222-222222222222"
	tests := []struct {
		name       string
		ciphertext string
		plaintext  []byte
		signer     *fakeRequestSigner
		wantReason delivery.PreHTTPReason
	}{
		{
			name:       "signer error",
			ciphertext: "hfse1.stored-envelope-placeholder",
			plaintext:  []byte("0123456789abcdef-secret"),
			signer:     &fakeRequestSigner{err: errors.New("signer unavailable")},
			wantReason: delivery.ReasonSigningInternal,
		},
		{
			name:       "signer returned a mismatched id",
			ciphertext: "hfse1.stored-envelope-placeholder",
			plaintext:  []byte("0123456789abcdef-secret"),
			signer: &fakeRequestSigner{headers: signing.Headers{
				ID:        "33333333-3333-4333-8333-333333333333",
				Timestamp: "1700000000",
				Signature: "v1,CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
			}},
			wantReason: delivery.ReasonSigningInternal,
		},
		{
			name:       "signer returned a non-canonical timestamp",
			ciphertext: "hfse1.stored-envelope-placeholder",
			plaintext:  []byte("0123456789abcdef-secret"),
			signer: &fakeRequestSigner{headers: signing.Headers{
				ID:        deliveryID,
				Timestamp: "01700000000",
				Signature: "v1,CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
			}},
			wantReason: delivery.ReasonSigningInternal,
		},
		{
			name:       "signer returned a malformed signature",
			ciphertext: "hfse1.stored-envelope-placeholder",
			plaintext:  []byte("0123456789abcdef-secret"),
			signer: &fakeRequestSigner{headers: signing.Headers{
				ID:        deliveryID,
				Timestamp: "1700000000",
				Signature: "v2,CCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCCC",
			}},
			wantReason: delivery.ReasonSigningInternal,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var requests atomic.Int32
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				requests.Add(1)
				w.WriteHeader(http.StatusOK)
			}))
			t.Cleanup(server.Close)

			preparation := executorTestPreparation(executorTestURL)
			preparation.DeliveryID = deliveryID
			preparation.SecretCiphertext = tt.ciphertext
			preparation.SecretPlaintext = tt.plaintext
			executor := newExecutorWithSigner(t, executorTestOptions(), tt.signer)

			outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
			var preHTTP *delivery.PreHTTPError
			if !errors.As(err, &preHTTP) || preHTTP.Reason != tt.wantReason {
				t.Fatalf("error = %v, want reason %s", err, tt.wantReason)
			}
			if requests.Load() != 0 {
				t.Fatal("pre-HTTP failure caused a network request")
			}
			if outcome.RequestHeaders != "" || outcome.Kind != delivery.OutcomeTerminalFailure {
				t.Fatalf("pre-HTTP outcome = %+v", outcome)
			}
		})
	}
}

func TestExecutorRefusesInsufficientLeaseBudget(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	opts := executorTestOptions()
	opts.Client = executorTestClient(t, server)
	executor := newExecutorForTest(t, opts)
	preparation := executorTestPreparation(executorTestURL)
	preparation.Remaining = opts.RequiredLeaseBudget() - time.Nanosecond

	if _, err := executor.Execute(context.Background(), preparation, http.MethodPost); !errors.Is(err, delivery.ErrInsufficientLease) {
		t.Fatalf("error = %v, want ErrInsufficientLease", err)
	}
	if requests.Load() != 0 {
		t.Fatal("request started without the required lease budget")
	}

	preparation.Remaining = opts.RequiredLeaseBudget()
	outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
	if err != nil || outcome.Kind != delivery.OutcomeSuccess || requests.Load() != 1 {
		t.Fatalf("exact-budget execution = (%+v, %v), requests %d", outcome, err, requests.Load())
	}
}

func TestExecutorRejectsNonPOSTAndInvalidURL(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	executor := newExecutorForTest(t, executorTestOptions())
	preparation := executorTestPreparation(executorTestURL)

	if _, err := executor.Execute(context.Background(), preparation, http.MethodGet); err == nil {
		t.Fatal("GET was accepted")
	}
	preparation.RequestURL = "/relative"
	if _, err := executor.Execute(context.Background(), preparation, http.MethodPost); err == nil {
		t.Fatal("relative request URL was accepted")
	}
	preparation.RequestURL = "http://user:url-secret@example.test/hook"
	preparation.RequestHeaders = `{"X-Test":`
	if _, err := executor.Execute(context.Background(), preparation, http.MethodPost); err == nil {
		t.Fatal("invalid headers were accepted")
	}
	if requests.Load() != 0 {
		t.Fatal("invalid preflight input caused a network request")
	}
}

func TestExecutorDoesNotFollowRedirects(t *testing.T) {
	t.Parallel()

	var targetRequests atomic.Int32
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		targetRequests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(target.Close)
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Location", target.URL+"/capture")
		w.WriteHeader(http.StatusFound)
		_, _ = io.WriteString(w, "redirect-response")
	}))
	t.Cleanup(origin.Close)

	opts := executorTestOptions()
	opts.Client = executorTestClient(t, origin)
	executor := newExecutorForTest(t, opts)
	outcome, err := executor.Execute(context.Background(), executorTestPreparation(executorTestURL), http.MethodPost)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != delivery.OutcomeTerminalFailure || outcome.ResponseStatus == nil || *outcome.ResponseStatus != http.StatusFound {
		t.Fatalf("redirect outcome = %+v", outcome)
	}
	if targetRequests.Load() != 0 {
		t.Fatal("redirect was followed")
	}
	if outcome.ResponseBodySnippet != "redirect-response" {
		t.Fatalf("snippet = %q, want original redirect response", outcome.ResponseBodySnippet)
	}
	if strings.Contains(outcome.ResponseHeaders, "Location") {
		t.Fatal("redirect location was persisted as a safe response header")
	}
}

func TestExecutorTimeoutBeforeResponseHeaders(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) {
		close(started)
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	opts := executorTestOptions()
	opts.Deadline = 75 * time.Millisecond
	opts.Client = executorTestClient(t, server)
	executor := newExecutorForTest(t, opts)

	outcome, err := executor.Execute(context.Background(), executorTestPreparation(executorTestURL), http.MethodPost)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	default:
		t.Fatal("request did not reach the server")
	}
	if outcome.Kind != delivery.OutcomeRetryableFailure || outcome.ErrorClass != "timeout" || outcome.ResponseStatus != nil {
		t.Fatalf("timeout outcome = %+v", outcome)
	}
	if strings.Contains(outcome.ErrorMessage, server.URL) {
		t.Fatal("timeout diagnostic contains the raw URL")
	}
}

func TestExecutorParentDeadlineCoversBodyRead(t *testing.T) {
	t.Parallel()

	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, "partial-body")
		w.(http.Flusher).Flush()
		close(started)
		<-release
	}))
	t.Cleanup(server.Close)
	t.Cleanup(func() { close(release) })
	opts := executorTestOptions()
	opts.Client = executorTestClient(t, server)
	executor := newExecutorForTest(t, opts)
	ctx, cancel := context.WithTimeout(context.Background(), 75*time.Millisecond)
	defer cancel()

	outcome, err := executor.Execute(ctx, executorTestPreparation(executorTestURL), http.MethodPost)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	default:
		t.Fatal("request did not reach the server")
	}
	if outcome.Kind != delivery.OutcomeRetryableFailure || outcome.ErrorClass != "timeout" ||
		outcome.ResponseStatus == nil || *outcome.ResponseStatus != http.StatusOK {
		t.Fatalf("body timeout outcome = %+v", outcome)
	}
	if !strings.Contains(outcome.ResponseBodySnippet, "partial-body") {
		t.Fatalf("partial body snippet = %q", outcome.ResponseBodySnippet)
	}
}

func TestExecutorExpiredContextDoesNotSend(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	executor := newExecutorForTest(t, executorTestOptions())
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()

	outcome, err := executor.Execute(ctx, executorTestPreparation(executorTestURL), http.MethodPost)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v, want deadline exceeded", err)
	}
	if requests.Load() != 0 || outcome.ErrorClass != "timeout" {
		t.Fatalf("expired-context result = (%+v, %v), requests %d", outcome, err, requests.Load())
	}
}

func TestExecutorResponseBodyCapRedactionAndUTF8(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		limit     int
		body      string
		checkBody func(*testing.T, string)
	}{
		{
			name:  "cap plus overflow byte",
			limit: 16,
			body:  strings.Repeat("a", 16) + "password=tail-secret",
			checkBody: func(t *testing.T, snippet string) {
				if snippet != strings.Repeat("a", 16) {
					t.Fatalf("capped snippet = %q", snippet)
				}
			},
		},
		{
			name:  "credential redaction",
			limit: 4096,
			body:  `{"password":"body-secret"}`,
			checkBody: func(t *testing.T, snippet string) {
				if !strings.Contains(snippet, "[REDACTED]") || strings.Contains(snippet, "body-secret") {
					t.Fatalf("redacted snippet = %q", snippet)
				}
			},
		},
		{
			name:  "UTF-8 safe snippet",
			limit: 8192,
			body:  strings.Repeat("界", 3000),
			checkBody: func(t *testing.T, snippet string) {
				if !utf8.ValidString(snippet) || utf8.RuneCountInString(snippet) > 2000 {
					t.Fatalf("snippet is invalid or too long: runes=%d", utf8.RuneCountInString(snippet))
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "text/plain")
				w.Header().Set("Set-Cookie", "cookie-secret")
				w.Header().Set("X-Api-Key", "api-secret")
				_, _ = io.WriteString(w, tt.body)
			}))
			t.Cleanup(server.Close)
			opts := executorTestOptions()
			opts.ResponseBodyLimit = tt.limit
			opts.Client = executorTestClient(t, server)
			executor := newExecutorForTest(t, opts)

			outcome, err := executor.Execute(context.Background(), executorTestPreparation(executorTestURL), http.MethodPost)
			if err != nil || outcome.Kind != delivery.OutcomeSuccess {
				t.Fatalf("Execute = (%+v, %v)", outcome, err)
			}
			tt.checkBody(t, outcome.ResponseBodySnippet)
			if strings.Contains(outcome.ResponseHeaders, "cookie-secret") || strings.Contains(outcome.ResponseHeaders, "api-secret") {
				t.Fatalf("unsafe response headers persisted: %s", outcome.ResponseHeaders)
			}
		})
	}
}

func TestExecutorNetworkErrorClassificationAndSecretSafety(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		err   error
		class string
		kind  delivery.OutcomeKind
	}{
		{name: "timeout", err: context.DeadlineExceeded, class: "timeout", kind: delivery.OutcomeRetryableFailure},
		{name: "DNS", err: &net.DNSError{Err: "password=dns-secret", Name: "secret.invalid"}, class: "dns", kind: delivery.OutcomeRetryableFailure},
		{name: "connect", err: &net.OpError{Op: "dial", Net: "tcp", Err: errors.New("password=connect-secret")}, class: "connect", kind: delivery.OutcomeRetryableFailure},
		{name: "TLS", err: tls.RecordHeaderError{Msg: "password=tls-secret"}, class: "tls", kind: delivery.OutcomeRetryableFailure},
		{name: "internal", err: errors.New("https://user:url-secret@example.test Authorization: Bearer header-secret"), class: "internal", kind: delivery.OutcomeTerminalFailure},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			opts := executorTestOptions()
			opts.Client = &http.Client{Transport: executorRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, tt.err
			})}
			executor := newExecutorForTest(t, opts)
			outcome, err := executor.Execute(context.Background(), executorTestPreparation("https://example.test/hook"), http.MethodPost)
			if err != nil {
				t.Fatal(err)
			}
			if outcome.Kind != tt.kind || outcome.ErrorClass != tt.class {
				t.Fatalf("network outcome = %+v, want %s/%s", outcome, tt.kind, tt.class)
			}
			for _, secret := range []string{"url-secret", "header-secret", "dns-secret", "connect-secret", "tls-secret"} {
				if strings.Contains(outcome.ErrorMessage, secret) {
					t.Fatalf("network diagnostic leaked %q: %q", secret, outcome.ErrorMessage)
				}
			}
		})
	}
}

func TestExecutorRealTLSFailureIsRetryable(t *testing.T) {
	t.Parallel()

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	t.Cleanup(server.Close)
	// The injected client reaches the test listener while keeping the public
	// hostname for SNI, so certificate verification fails as it would against
	// an untrusted public endpoint. Production uses the approved transport.
	opts := executorTestOptions()
	opts.Client = &http.Client{Transport: &http.Transport{
		DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, network, server.Listener.Addr().String())
		},
		TLSHandshakeTimeout: time.Second,
	}}
	executor := newExecutorForTest(t, opts)

	outcome, err := executor.Execute(context.Background(), executorTestPreparation("https://example.com/hook"), http.MethodPost)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != delivery.OutcomeRetryableFailure || outcome.ErrorClass != "tls" {
		t.Fatalf("TLS outcome = %+v", outcome)
	}
}

// The approved deployed defaults reserve 10s HTTP + 2s handshake + 3s
// post-processing + 10s shutdown before a request may start.
func TestApprovedDefaultHTTPReserveIsTwentyFiveSeconds(t *testing.T) {
	t.Parallel()

	opts := HTTPOptions{
		Deadline:          10 * time.Second,
		HandshakeBudget:   2 * time.Second,
		PostProcessBudget: 3 * time.Second,
		ShutdownReserve:   10 * time.Second,
		ResponseBodyLimit: 4096,
	}
	if got := opts.RequiredLeaseBudget(); got != 25*time.Second {
		t.Fatalf("required lease budget = %s, want 25s", got)
	}
}

// The gated unsigned branch must never transmit a stale protocol header from the
// stored snapshot, and must never call the signer.
func TestExecutorUnsignedPathStripsStaleProtocolHeaders(t *testing.T) {
	t.Parallel()

	var transmitted http.Header
	opts := executorTestOptions()
	opts.Client = &http.Client{Transport: executorRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		transmitted = request.Header.Clone()
		return &http.Response{
			StatusCode: http.StatusNoContent,
			Header:     http.Header{},
			Body:       http.NoBody,
			Request:    request,
		}, nil
	})}
	signer := defaultTestSigner()
	executor := newExecutorWithSigner(t, opts, signer)

	preparation := executorTestPreparation("https://example.test/hook")
	preparation.RequestHeaders = `{"webhook-id":"stale-id","webhook-timestamp":"1700000000","webhook-signature":"v1,stale","X-Custom":"snapshot"}`
	preparation.SecretCiphertext = ""
	preparation.SecretPlaintext = nil

	outcome, err := executor.Execute(context.Background(), preparation, http.MethodPost)
	if err != nil {
		t.Fatalf("Execute() error: %v", err)
	}
	if outcome.Kind != delivery.OutcomeSuccess {
		t.Fatalf("outcome = %+v, want success", outcome)
	}
	if signer.calls != 0 {
		t.Fatalf("unsigned path called the signer %d time(s)", signer.calls)
	}
	if outcome.RequestHeaders != "" {
		t.Fatalf("unsigned attempt recorded signing headers %q", outcome.RequestHeaders)
	}
	if transmitted == nil {
		t.Fatal("unsigned request was not transmitted")
	}
	for _, name := range []string{signing.HeaderID, signing.HeaderTimestamp, signing.HeaderSignature} {
		if value := transmitted.Get(name); value != "" {
			t.Fatalf("stale %s = %q was transmitted", name, value)
		}
	}
	if got := transmitted.Get("X-Custom"); got != "snapshot" {
		t.Fatalf("snapshot header X-Custom = %q, want snapshot", got)
	}
}

func TestExecutorRejectsDisallowedDestinationBeforeNetwork(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	opts := executorTestOptions()
	opts.Client = &http.Client{Transport: executorRoundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)
		return nil, errors.New("unexpected network")
	})}
	executor := newExecutorForTest(t, opts)

	for _, raw := range []string{
		"http://example.test/hook",
		"https://user:pass@example.test/hook",
		"https://example.test/hook#frag",
		"https://example.test:8443/hook",
		"https://example.test:/hook",
		"https://10.0.0.5/hook",
		"https://127.0.0.1/hook",
		"https://[::1]/hook",
		"https://169.254.169.254/latest/meta-data",
		"https://[fe80::1%25eth0]/hook",
	} {
		outcome, err := executor.Execute(context.Background(), executorTestPreparation(raw), http.MethodPost)
		var preHTTP *delivery.PreHTTPError
		if !errors.As(err, &preHTTP) || preHTTP.Reason != delivery.ReasonDestinationDisallowed {
			t.Fatalf("%q error = %v, want destination_disallowed", raw, err)
		}
		if outcome.Kind != delivery.OutcomeTerminalFailure || outcome.ErrorClass != "connect" {
			t.Fatalf("%q outcome = %+v", raw, outcome)
		}
		if strings.Contains(err.Error()+outcome.ErrorMessage, "example.test") {
			t.Fatalf("%q leaked the destination: %q", raw, err.Error())
		}
	}
	if requests.Load() != 0 {
		t.Fatalf("disallowed destinations made %d network requests", requests.Load())
	}
}

func TestExecutorProhibitedDialIsPermanentPreHTTP(t *testing.T) {
	t.Parallel()

	opts := executorTestOptions()
	opts.Client = &http.Client{Transport: executorRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, &destination.ProhibitedError{Reason: "prohibited range"}
	})}
	executor := newExecutorForTest(t, opts)

	outcome, err := executor.Execute(context.Background(), executorTestPreparation("https://example.test/hook"), http.MethodPost)
	var preHTTP *delivery.PreHTTPError
	if !errors.As(err, &preHTTP) || preHTTP.Reason != delivery.ReasonDestinationDisallowed {
		t.Fatalf("error = %v, want destination_disallowed", err)
	}
	if outcome.Kind != delivery.OutcomeTerminalFailure || outcome.ErrorClass != "connect" {
		t.Fatalf("outcome = %+v", outcome)
	}
}
