package worker

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"math"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/platform/destination"
	"github.com/metamin44/hookflow/internal/platform/observability"
	"github.com/metamin44/hookflow/internal/platform/signing"
)

const (
	maxResponseSnippetRunes               = 2000
	legacyCredentialHeadersFilteredMetric = "hookflow_worker_legacy_credential_headers_filtered_total"
)

var (
	errHTTPContextRequired = errors.New("HTTP execution context is required")
	errHTTPMethodNotPOST   = errors.New("HTTP delivery execution requires POST")
	errHTTPHeadersInvalid  = errors.New("HTTP delivery request headers must be a JSON object of strings")
	errExecutorUnavailable = errors.New("HTTP executor is not initialized")
)

// RequestSigner produces the approved three webhook headers for one attempt.
type RequestSigner interface {
	Sign(deliveryID string, secret, body []byte) (signing.Headers, error)
}

// HTTPOptions contains the end-to-end HTTP deadline and the lease budgets that
// must be reserved around execution and durable completion.
type HTTPOptions struct {
	Deadline          time.Duration
	HandshakeBudget   time.Duration
	PostProcessBudget time.Duration
	ShutdownReserve   time.Duration
	ResponseBodyLimit int
	// Resolver is the approved destination resolver. Production construction
	// leaves it nil, which selects net.DefaultResolver. It exists so tests can
	// pin answers without external DNS; it is not configuration-driven.
	Resolver destination.Resolver
	Client   *http.Client
	// Metrics receives only fixed-cardinality counters. It is optional for
	// isolated executor tests; production composition supplies the Worker registry.
	Metrics *observability.Metrics
}

// Validate checks all budgets and installs a safe client when none is supplied.
func (o *HTTPOptions) Validate() error {
	if o == nil {
		return errors.New("HTTP execution options are required")
	}
	if o.Deadline <= 0 {
		return errors.New("HTTP execution deadline must be positive")
	}
	if o.HandshakeBudget <= 0 {
		return errors.New("HTTP handshake budget must be positive")
	}
	if o.PostProcessBudget <= 0 {
		return errors.New("HTTP post-processing budget must be positive")
	}
	if o.ShutdownReserve <= 0 {
		return errors.New("HTTP shutdown reserve must be positive")
	}
	if o.ResponseBodyLimit <= 0 {
		return errors.New("HTTP response body limit must be positive")
	}
	if _, ok := positiveHTTPBudgetSum(o.Deadline, o.HandshakeBudget, o.PostProcessBudget, o.ShutdownReserve); !ok {
		return errors.New("HTTP lease budget is too large")
	}
	if o.Client == nil {
		o.Client = defaultHTTPClient(*o)
	}
	return nil
}

// RequiredLeaseBudget is the minimum remaining lease required before HTTP may
// start. The caller must pass it to delivery.Store.PrepareHTTP.
func (o HTTPOptions) RequiredLeaseBudget() time.Duration {
	budget, _ := positiveHTTPBudgetSum(o.Deadline, o.HandshakeBudget, o.PostProcessBudget, o.ShutdownReserve)
	return budget
}

func positiveHTTPBudgetSum(parts ...time.Duration) (time.Duration, bool) {
	var total time.Duration
	for _, part := range parts {
		if part <= 0 || part > time.Duration(math.MaxInt64)-total {
			return 0, false
		}
		total += part
	}
	return total, true
}

// Executor performs one signed (or explicitly gated unsigned) HTTP delivery
// attempt.
type Executor struct {
	opts   HTTPOptions
	client *http.Client
	signer RequestSigner
	logger *slog.Logger
}

// NewExecutor validates options and constructs an executor. An injected client
// is copied so redirect handling can be secured without mutating the caller;
// production construction never injects one, so the approved destination
// transport is always used.
func NewExecutor(opts HTTPOptions, signer RequestSigner, logger *slog.Logger) (*Executor, error) {
	if err := opts.Validate(); err != nil {
		return nil, err
	}
	if signer == nil {
		return nil, errors.New("HTTP request signer is required")
	}
	if logger == nil {
		logger = slog.Default()
	}
	client := *opts.Client
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Executor{opts: opts, client: &client, signer: signer, logger: logger}, nil
}

func defaultHTTPClient(opts HTTPOptions) *http.Client {
	return &http.Client{
		Transport: destination.Transport(opts.Resolver, opts.HandshakeBudget),
		Timeout:   opts.Deadline,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

// prepareRequest applies the approved destination policy, parses the stored
// request headers and builds the request. Policy and header refusals set the
// outcome so Execute returns them as pre-HTTP failures.
func (e *Executor) prepareRequest(ctx context.Context, preparation delivery.HTTPPreparation, outcome *delivery.Outcome) (*http.Request, context.CancelFunc, error) {
	noop := func() {}
	// HTTPS only, port 443, no userinfo/fragment/zone, no ASCII whitespace or
	// control byte, and no prohibited literal address. A queued snapshot that
	// violates the policy fails closed.
	if _, err := destination.ParseURL(preparation.RequestURL); err != nil {
		return nil, noop, rejectDestination(outcome)
	}
	headers, headerErr := parseRequestHeaders(preparation.RequestHeaders)
	if headerErr != nil {
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "internal"
		outcome.ErrorMessage = "stored webhook request headers are invalid"
		return nil, noop, &delivery.PreHTTPError{
			Reason:  delivery.ReasonHeadersInvalid,
			Message: outcome.ErrorMessage,
		}
	}
	if filtered := filterCredentialHeaders(headers); filtered > 0 {
		e.opts.Metrics.Add(legacyCredentialHeadersFilteredMetric, int64(filtered))
	}
	executionCtx, cancel := context.WithTimeout(ctx, e.opts.Deadline)
	body := []byte(preparation.EventPayload)
	request, requestErr := http.NewRequestWithContext(executionCtx, http.MethodPost, preparation.RequestURL, bytes.NewReader(body))
	if requestErr != nil || request.URL.Scheme != "https" || request.URL.Host == "" {
		cancel()
		return nil, noop, rejectDestination(outcome)
	}
	request.Header = headers
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("X-HookFlow-Delivery-Id", preparation.DeliveryID)
	request.Header.Set("X-HookFlow-Event-Id", preparation.EventID)
	request.Header.Set("X-HookFlow-Dispatch-Sequence", strconv.Itoa(preparation.DispatchSeq))
	// NewRequest installs GetBody for bytes.Reader. Removing it prevents the
	// transport from replaying a POST that carries an Idempotency-Key header.
	request.GetBody = nil
	return request, cancel, nil
}

func rejectDestination(outcome *delivery.Outcome) error {
	outcome.Kind = delivery.OutcomeTerminalFailure
	outcome.ErrorClass = "connect"
	outcome.ErrorMessage = "destination is not permitted by policy"
	return &delivery.PreHTTPError{
		Reason:  delivery.ReasonDestinationDisallowed,
		Message: outcome.ErrorMessage,
	}
}

// Execute performs one real HTTP POST. Remote HTTP and network failures are
// represented in Outcome; errors are reserved for pre-execution refusals.
func (e *Executor) Execute(ctx context.Context, preparation delivery.HTTPPreparation, method string) (outcome delivery.Outcome, err error) {
	started := time.Now()
	outcome = delivery.Outcome{
		RequestMethod:   method,
		ResponseHeaders: "{}",
		StartedAt:       started,
	}
	defer func() {
		outcome.FinishedAt = time.Now()
		outcome.Duration = outcome.FinishedAt.Sub(outcome.StartedAt)
		if e != nil && e.logger != nil {
			e.logger.Debug("HTTP delivery execution finished",
				"delivery_id", preparation.DeliveryID,
				"outcome", outcome.Kind,
				"error_class", outcome.ErrorClass)
		}
	}()

	if e == nil || e.client == nil || e.signer == nil {
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "internal"
		outcome.ErrorMessage = errExecutorUnavailable.Error()
		return outcome, errExecutorUnavailable
	}
	if method != http.MethodPost {
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "internal"
		outcome.ErrorMessage = errHTTPMethodNotPOST.Error()
		return outcome, errHTTPMethodNotPOST
	}
	if preparation.Remaining < e.opts.RequiredLeaseBudget() {
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "internal"
		outcome.ErrorMessage = "HTTP delivery lease budget is insufficient"
		return outcome, delivery.ErrInsufficientLease
	}
	if ctx == nil {
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "internal"
		outcome.ErrorMessage = errHTTPContextRequired.Error()
		return outcome, errHTTPContextRequired
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		class, message := contextFailure(ctxErr)
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = class
		outcome.ErrorMessage = message
		return outcome, ctxErr
	}
	request, cancel, prepareErr := e.prepareRequest(ctx, preparation, &outcome)
	defer cancel()
	if prepareErr != nil {
		return outcome, prepareErr
	}

	audit, preHTTPSignErr := e.signRequest(preparation, request, []byte(preparation.EventPayload))
	if preHTTPSignErr != nil {
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "internal"
		outcome.ErrorMessage = "pre-HTTP request construction failed"
		return outcome, preHTTPSignErr
	}
	outcome.RequestHeaders = audit

	response, doErr := e.client.Do(request)
	if doErr != nil {
		if response != nil && response.Body != nil {
			_ = response.Body.Close()
		}
		return outcome, applyTransportError(&outcome, doErr)
	}
	defer func() { _ = response.Body.Close() }()

	status := response.StatusCode
	outcome.ResponseHeaders = safeResponseHeaders(response.Header)
	if status < 100 || status > 599 {
		// The request was executed and a response was observed, but this status
		// cannot be represented by the approved 100..599 database contract.
		// Persist one terminal attempt with a nil status rather than weakening
		// the constraint or turning a protocol violation into an uncertain write.
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "http_status"
		outcome.ErrorMessage = "HTTP endpoint returned an invalid response status"
		return outcome, nil
	}
	outcome.ResponseStatus = &status
	body, readErr := readResponseBody(response.Body, e.opts.ResponseBodyLimit)
	outcome.ResponseBodySnippet = responseSnippet(body)
	if readErr != nil {
		applyBodyReadFailure(&outcome, readErr)
		return outcome, nil
	}
	classifyResponseStatus(&outcome, status)
	return outcome, nil
}

// applyTransportError grades a transport-level failure. A destination-policy
// rejection is permanent and must never be mistaken for a retryable connect
// failure, so it returns the typed pre-HTTP error.
func applyTransportError(outcome *delivery.Outcome, doErr error) error {
	if errors.Is(doErr, destination.ErrProhibited) {
		return rejectDestination(outcome)
	}
	class, message, retryable := networkFailure(doErr)
	outcome.ErrorClass = class
	outcome.ErrorMessage = message
	if retryable {
		outcome.Kind = delivery.OutcomeRetryableFailure
	} else {
		outcome.Kind = delivery.OutcomeTerminalFailure
	}
	return nil
}

func applyBodyReadFailure(outcome *delivery.Outcome, readErr error) {
	class, message, retryable := networkFailure(readErr)
	outcome.ErrorClass = class
	outcome.ErrorMessage = "HTTP response body could not be read completely"
	if class == "internal" {
		outcome.ErrorMessage = message
	}
	if retryable {
		outcome.Kind = delivery.OutcomeRetryableFailure
	} else {
		outcome.Kind = delivery.OutcomeTerminalFailure
	}
}

func classifyResponseStatus(outcome *delivery.Outcome, status int) {
	switch {
	case status >= http.StatusOK && status < http.StatusMultipleChoices:
		outcome.Kind = delivery.OutcomeSuccess
	case status == http.StatusRequestTimeout || status == http.StatusTooManyRequests ||
		(status >= http.StatusInternalServerError && status <= 599):
		outcome.Kind = delivery.OutcomeRetryableFailure
		outcome.ErrorClass = "http_status"
		outcome.ErrorMessage = "HTTP endpoint returned a retryable status"
	default:
		outcome.Kind = delivery.OutcomeTerminalFailure
		outcome.ErrorClass = "http_status"
		outcome.ErrorMessage = "HTTP endpoint returned a terminal status"
	}
}

// signRequest is the single request-construction tail. It either installs a
// valid signature over the exact transmitted body or returns a typed pre-HTTP
// failure. The unsigned branch is reachable only for an empty stored secret,
// which the processor admits solely under the explicit development gate.
func (e *Executor) signRequest(preparation delivery.HTTPPreparation, request *http.Request, body []byte) (string, error) {
	if preparation.SecretCiphertext != "" && len(preparation.SecretPlaintext) == 0 {
		return "", &delivery.PreHTTPError{
			Reason:  delivery.ReasonSecretInvalid,
			Message: "stored webhook secret was not resolved before request construction",
		}
	}
	if preparation.SecretCiphertext == "" {
		if len(preparation.SecretPlaintext) != 0 {
			return "", &delivery.PreHTTPError{
				Reason:  delivery.ReasonSecretInvalid,
				Message: "resolved webhook secret exists without a stored envelope",
			}
		}
		// Never transmit stale protocol headers on the gated unsigned path.
		request.Header.Del(signing.HeaderID)
		request.Header.Del(signing.HeaderTimestamp)
		request.Header.Del(signing.HeaderSignature)
		return "", nil
	}
	signed, err := e.signer.Sign(preparation.DeliveryID, preparation.SecretPlaintext, body)
	if err != nil {
		return "", &delivery.PreHTTPError{
			Reason:  delivery.ReasonSigningInternal,
			Message: "webhook signing failed before HTTP",
		}
	}
	if signed.ID != preparation.DeliveryID || !validTimestampHeader(signed.Timestamp) ||
		!validSignatureHeader(signed.Signature) {
		return "", &delivery.PreHTTPError{
			Reason:  delivery.ReasonSigningInternal,
			Message: "webhook signer returned invalid protocol headers",
		}
	}
	request.Header.Set(signing.HeaderID, signed.ID)
	request.Header.Set(signing.HeaderTimestamp, signed.Timestamp)
	request.Header.Set(signing.HeaderSignature, signed.Signature)
	audit, err := json.Marshal(map[string]string{
		signing.HeaderID:        signed.ID,
		signing.HeaderTimestamp: signed.Timestamp,
		signing.HeaderSignature: signed.Signature,
	})
	if err != nil {
		return "", &delivery.PreHTTPError{
			Reason:  delivery.ReasonSigningInternal,
			Message: "transmitted webhook headers could not be encoded",
		}
	}
	return string(audit), nil
}

func validTimestampHeader(value string) bool {
	if value == "" || len(value) > 19 || (len(value) > 1 && value[0] == '0') {
		return false
	}
	for i := 0; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

func validSignatureHeader(value string) bool {
	return strings.HasPrefix(value, "v1,") && len(value) > 3 && len(value) <= 64
}

func filterCredentialHeaders(headers http.Header) int {
	filtered := 0
	for name := range headers {
		if endpoint.CredentialBearingHeader(name) {
			headers.Del(name)
			filtered++
		}
	}
	return filtered
}

func parseRequestHeaders(snapshot string) (http.Header, error) {
	trimmed := bytes.TrimSpace([]byte(snapshot))
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, errHTTPHeadersInvalid
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &values); err != nil || values == nil {
		return nil, errHTTPHeadersInvalid
	}
	headers := make(http.Header, len(values)+4)
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		raw := bytes.TrimSpace(values[name])
		if !validHeaderName(name) || len(raw) == 0 || raw[0] != '"' {
			return nil, errHTTPHeadersInvalid
		}
		var value string
		if err := json.Unmarshal(raw, &value); err != nil || !validHeaderValue(value) {
			return nil, errHTTPHeadersInvalid
		}
		if _, duplicate := headers[http.CanonicalHeaderKey(name)]; duplicate {
			return nil, errHTTPHeadersInvalid
		}
		headers.Set(name, value)
	}
	return headers, nil
}

func validHeaderName(name string) bool {
	if name == "" {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') ||
			strings.ContainsRune("!#$%&'*+-.^_`|~", rune(c)) {
			continue
		}
		return false
	}
	return true
}

func validHeaderValue(value string) bool {
	return !strings.ContainsAny(value, "\r\n\x00")
}

func readResponseBody(body io.Reader, limit int) ([]byte, error) {
	readLimit := int64(limit)
	if limit < math.MaxInt {
		readLimit++
	}
	data, err := io.ReadAll(io.LimitReader(body, readLimit))
	if len(data) > limit {
		data = data[:limit]
	}
	return data, err
}

func responseSnippet(body []byte) string {
	redacted := delivery.RedactDiagnostic(string(body))
	runes := []rune(redacted)
	if len(runes) > maxResponseSnippetRunes {
		return string(runes[:maxResponseSnippetRunes])
	}
	return redacted
}

func safeResponseHeaders(headers http.Header) string {
	safe := make(map[string]string, 3)
	for _, name := range []string{"Content-Type", "Retry-After", "Date"} {
		if values := headers.Values(name); len(values) > 0 {
			safe[name] = delivery.RedactDiagnostic(strings.Join(values, ", "))
		}
	}
	encoded, err := json.Marshal(safe)
	if err != nil {
		return "{}"
	}
	return string(encoded)
}

func contextFailure(err error) (class, message string) {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout", "HTTP execution deadline was exceeded before the request started"
	}
	return "internal", "HTTP execution context was canceled before the request started"
}

func networkFailure(err error) (class, message string, retryable bool) {
	if err == nil {
		return "internal", "HTTP request failed", false
	}
	var timeout interface{ Timeout() bool }
	if errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &timeout) && timeout.Timeout()) {
		return "timeout", "HTTP request timed out", true
	}
	// http.Client wraps RoundTripper errors in *url.Error, which itself
	// satisfies net.Error. Classify the underlying cause instead of treating
	// every arbitrary local error as a connection failure.
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
		if err == nil {
			return "internal", "HTTP request failed", false
		}
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return "dns", "HTTP DNS resolution failed", true
	}
	if isTLSFailure(err) {
		return "tls", "HTTP TLS negotiation failed", true
	}
	var networkErr net.Error
	if errors.As(err, &networkErr) {
		return "connect", "HTTP connection failed", true
	}
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return "connect", "HTTP connection failed", true
	}
	return "internal", "HTTP request failed", false
}

func isTLSFailure(err error) bool {
	var recordErr tls.RecordHeaderError
	var authorityErr x509.UnknownAuthorityError
	var hostnameErr x509.HostnameError
	var invalidErr x509.CertificateInvalidError
	var rootsErr x509.SystemRootsError
	if errors.As(err, &recordErr) || errors.As(err, &authorityErr) ||
		errors.As(err, &hostnameErr) || errors.As(err, &invalidErr) || errors.As(err, &rootsErr) {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "tls handshake") || strings.Contains(message, "x509:") ||
		strings.Contains(message, "certificate signed by unknown authority")
}
