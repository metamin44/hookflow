package management

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/modules/organization"
	"github.com/metamin44/hookflow/internal/modules/subscription"
	"github.com/metamin44/hookflow/internal/platform/kek"
)

type stubDatabase struct{}

func (stubDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, errors.New("unused")
}

func (stubDatabase) QueryRow(context.Context, string, ...any) pgx.Row { return nil }

func (stubDatabase) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unused")
}

type stubProvider struct{}

func (stubProvider) Wrap(context.Context, []byte, kek.Context) (kek.Wrapped, error) {
	return kek.Wrapped{}, nil
}

func (stubProvider) Unwrap(context.Context, kek.Wrapped, kek.Context) ([]byte, error) {
	return nil, nil
}

func (stubProvider) CurrentKID() string { return "dev-1" }

var (
	_ Database     = stubDatabase{}
	_ kek.Provider = stubProvider{}
)

func stringPtr(value string) *string { return &value }

func storedEndpoint() endpoint.Record {
	return endpoint.Record{
		ID:              "0198c0de-0000-7000-8000-000000000001",
		OrganizationID:  "0198c0de-0000-7000-8000-000000000002",
		Name:            "orders",
		Description:     stringPtr("primary receiver"),
		URL:             "https://example.test/hooks",
		SecretEncrypted: "hfse1.payload",
		SigningScheme:   endpoint.SigningScheme,
		HTTPHeaders:     map[string]string{"x-tenant": "acme"},
		Status:          endpoint.StatusActive,
	}
}

func TestNewServiceRejectsIncompleteConfiguration(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := NewService(nil, stubProvider{}, 10, logger, strings.NewReader("")); err == nil {
		t.Fatal("NewService() accepted a nil pool")
	}
	if _, err := NewService(stubDatabase{}, nil, 10, logger, strings.NewReader("")); err == nil {
		t.Fatal("NewService() accepted a nil provider")
	}
	if _, err := NewService(stubDatabase{}, stubProvider{}, 0, logger, strings.NewReader("")); err == nil {
		t.Fatal("NewService() accepted a zero ceiling")
	}
	if _, err := NewService(stubDatabase{}, stubProvider{}, 10, logger, nil); err == nil {
		t.Fatal("NewService() accepted a nil randomness source")
	}
}

func TestValidatePatchInputRejectsInvalidValues(t *testing.T) {
	long := strings.Repeat("a", endpoint.MaxNameLength+1)
	cases := []struct {
		name  string
		patch PatchInput
		code  string
		field string
	}{
		{name: "empty name", patch: PatchInput{Name: stringPtr("")}, code: "invalid_name", field: "name"},
		{name: "oversized name", patch: PatchInput{Name: &long}, code: "invalid_name", field: "name"},
		{name: "control in description", patch: PatchInput{Description: stringPtr("bad\x00"), DescriptionSet: true}, code: "invalid_description", field: "description"},
		{name: "http url", patch: PatchInput{URL: stringPtr("http://example.test")}, code: "invalid_url", field: "url"},
		{name: "relative url", patch: PatchInput{URL: stringPtr("/hooks")}, code: "invalid_url", field: "url"},
		{name: "userinfo url", patch: PatchInput{URL: stringPtr("https://user@example.test")}, code: "invalid_url", field: "url"},
		{name: "fragment url", patch: PatchInput{URL: stringPtr("https://example.test/#a")}, code: "invalid_url", field: "url"},
		{name: "deleted status", patch: PatchInput{Status: stringPtr("deleted")}, code: "invalid_status", field: "status"},
		{name: "reserved header", patch: PatchInput{Headers: map[string]string{"Authorization": "x"}, HeadersSet: true}, code: "invalid_headers", field: "http_headers"},
		{name: "credential token header", patch: PatchInput{Headers: map[string]string{"X-Auth-Token": "do-not-echo"}, HeadersSet: true}, code: "invalid_headers", field: "http_headers"},
		{name: "credential secret header", patch: PatchInput{Headers: map[string]string{"X-Client-Secret": "do-not-echo"}, HeadersSet: true}, code: "invalid_headers", field: "http_headers"},
		{name: "case duplicate header", patch: PatchInput{Headers: map[string]string{"X-A": "1", "x-a": "2"}, HeadersSet: true}, code: "invalid_headers", field: "http_headers"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := validatePatchInput(testCase.patch)
			if err == nil {
				t.Fatal("validatePatchInput() accepted an invalid patch")
			}
			if err.Status != http.StatusBadRequest || err.Code != testCase.code || err.Field != testCase.field {
				t.Fatalf("error = %d/%s/%s, want 400/%s/%s", err.Status, err.Code, err.Field, testCase.code, testCase.field)
			}
		})
	}
}

func TestValidatePatchInputCanonicalizesHeaders(t *testing.T) {
	validated, err := validatePatchInput(PatchInput{
		Headers:    map[string]string{"X-Tenant": "acme"},
		HeadersSet: true,
	})
	if err != nil {
		t.Fatalf("validatePatchInput() = %v", err)
	}
	if got := validated.Headers["x-tenant"]; got != "acme" {
		t.Fatalf("canonical headers = %v, want lowercase x-tenant", validated.Headers)
	}
	if _, present := validated.Headers["X-Tenant"]; present {
		t.Fatalf("canonical headers kept the original case: %v", validated.Headers)
	}
}

func TestValidatePatchInputAcceptsNullDescription(t *testing.T) {
	validated, err := validatePatchInput(PatchInput{DescriptionSet: true})
	if err != nil {
		t.Fatalf("validatePatchInput() = %v", err)
	}
	if !validated.DescriptionSet || validated.Description != nil {
		t.Fatalf("validated = %+v, want an explicit null description", validated)
	}
}

func TestApplyPatchDetectsNoOp(t *testing.T) {
	current := storedEndpoint()
	cases := []struct {
		name  string
		patch PatchInput
	}{
		{name: "identical name", patch: PatchInput{Name: stringPtr("orders")}},
		{name: "identical description", patch: PatchInput{Description: stringPtr("primary receiver"), DescriptionSet: true}},
		{name: "identical url", patch: PatchInput{URL: stringPtr("https://example.test/hooks")}},
		{name: "identical headers different case", patch: PatchInput{Headers: map[string]string{"X-Tenant": "acme"}, HeadersSet: true}},
		{name: "identical status", patch: PatchInput{Status: stringPtr(endpoint.StatusActive)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			patch := testCase.patch
			if patch.HeadersSet {
				canonical, field := endpoint.CanonicalHeaders(patch.Headers)
				if field != nil {
					t.Fatalf("CanonicalHeaders() = %v", field)
				}
				patch.Headers = canonical
			}
			_, changed := applyPatch(current, patch)
			if len(changed) != 0 {
				t.Fatalf("applyPatch() changed = %v, want a no-op", changed)
			}
		})
	}
}

func TestApplyPatchReportsChangedFieldsSorted(t *testing.T) {
	current := storedEndpoint()
	patch := PatchInput{
		Name:       stringPtr("orders-v2"),
		URL:        stringPtr("https://example.test/v2"),
		Headers:    map[string]string{"x-tenant": "beta"},
		Status:     stringPtr(endpoint.StatusPaused),
		HeadersSet: true,
	}
	next, changed := applyPatch(current, patch)
	if want := []string{"http_headers", "name", "status", "url"}; !equalStrings(changed, want) {
		t.Fatalf("changed = %v, want %v", changed, want)
	}
	if next.Name != "orders-v2" || next.URL != "https://example.test/v2" || next.Status != endpoint.StatusPaused {
		t.Fatalf("next = %+v", next)
	}
	if next.HTTPHeaders["x-tenant"] != "beta" {
		t.Fatalf("next headers = %v", next.HTTPHeaders)
	}
	if next.Description == nil || *next.Description != "primary receiver" {
		t.Fatal("applyPatch() dropped an untouched description")
	}
	if next.SecretEncrypted != current.SecretEncrypted || next.ID != current.ID {
		t.Fatal("applyPatch() mutated identity fields")
	}
}

func TestApplyPatchSetsAndClearsDescription(t *testing.T) {
	current := storedEndpoint()
	next, changed := applyPatch(current, PatchInput{DescriptionSet: true})
	if len(changed) != 1 || changed[0] != "description" || next.Description != nil {
		t.Fatalf("clear description: changed=%v description=%v", changed, next.Description)
	}

	bare := current
	bare.Description = nil
	next, changed = applyPatch(bare, PatchInput{Description: stringPtr("new"), DescriptionSet: true})
	if len(changed) != 1 || next.Description == nil || *next.Description != "new" {
		t.Fatalf("set description: changed=%v description=%v", changed, next.Description)
	}

	next, changed = applyPatch(bare, PatchInput{Name: stringPtr("orders")})
	if len(changed) != 0 || next.Description != nil {
		t.Fatalf("absent description must stay absent: changed=%v description=%v", changed, next.Description)
	}
}

func TestSuspendedDenied(t *testing.T) {
	cases := []struct {
		name   string
		patch  PatchInput
		denied bool
	}{
		{name: "metadata only", patch: PatchInput{Name: stringPtr("x"), Description: stringPtr("y"), DescriptionSet: true}},
		{name: "url", patch: PatchInput{URL: stringPtr("https://example.test")}, denied: true},
		{name: "headers", patch: PatchInput{Headers: map[string]string{}, HeadersSet: true}, denied: true},
		{name: "activate", patch: PatchInput{Status: stringPtr(endpoint.StatusActive)}, denied: true},
		{name: "pause", patch: PatchInput{Status: stringPtr(endpoint.StatusPaused)}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := suspendedDenied(testCase.patch); got != testCase.denied {
				t.Fatalf("suspendedDenied() = %v, want %v", got, testCase.denied)
			}
		})
	}
}

func TestAuditAction(t *testing.T) {
	cases := []struct {
		old, new string
		action   string
	}{
		{endpoint.StatusActive, endpoint.StatusActive, "endpoint.updated"},
		{endpoint.StatusActive, endpoint.StatusPaused, "endpoint.paused"},
		{endpoint.StatusPaused, endpoint.StatusActive, "endpoint.activated"},
	}
	for _, testCase := range cases {
		if got := auditAction(testCase.old, testCase.new); got != testCase.action {
			t.Fatalf("auditAction(%q, %q) = %q, want %q", testCase.old, testCase.new, got, testCase.action)
		}
	}
}

func TestMapStoreError(t *testing.T) {
	httpErr := conflict("x", "y")
	if got := mapStoreError(httpErr); !errors.Is(got, httpErr) {
		t.Fatal("mapStoreError() replaced an HTTPError")
	}
	for _, err := range []error{endpoint.ErrNotFound, subscription.ErrNotFound, organization.ErrNotFound} {
		mapped := mapStoreError(errors.Join(errors.New("wrap"), err))
		var got *HTTPError
		if !errors.As(mapped, &got) || got.Status != http.StatusNotFound {
			t.Fatalf("mapStoreError(%v) = %v, want 404", err, mapped)
		}
	}
	other := errors.New("boom")
	if got := mapStoreError(other); !errors.Is(got, other) {
		t.Fatal("mapStoreError() replaced an unknown error")
	}
}

func TestIfMatchHTTPError(t *testing.T) {
	cases := []struct {
		name   string
		err    error
		status int
		code   string
	}{
		{name: "missing", err: errMissingIfMatch, status: http.StatusPreconditionRequired, code: "precondition_required"},
		{name: "invalid", err: errInvalidIfMatch, status: http.StatusBadRequest, code: "invalid_if_match"},
		{name: "stale", err: errStaleIfMatch, status: http.StatusPreconditionFailed, code: "precondition_failed"},
		{name: "unknown", err: errors.New("other"), status: http.StatusInternalServerError, code: "internal_error"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			got := ifMatchHTTPError(testCase.err, `"hf1-current"`)
			if got.Status != testCase.status || got.Code != testCase.code {
				t.Fatalf("error = %d/%s, want %d/%s", got.Status, got.Code, testCase.status, testCase.code)
			}
			if testCase.status == http.StatusPreconditionFailed && got.ETag != `"hf1-current"` {
				t.Fatalf("precondition ETag = %q", got.ETag)
			}
		})
	}
}

func TestProviderHTTPError(t *testing.T) {
	if got := providerHTTPError(errors.Join(errors.New("wrap"), kek.ErrTransient)); got.Status != http.StatusServiceUnavailable {
		t.Fatalf("transient provider error = %d, want 503", got.Status)
	}
	if got := providerHTTPError(errors.New("permanent")); got.Status != http.StatusInternalServerError {
		t.Fatalf("permanent provider error = %d, want 500", got.Status)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
