package management

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type staticDestinationResolver struct {
	addrs []netip.Addr
	err   error
}

func (r staticDestinationResolver) LookupNetIP(context.Context, string, string) ([]netip.Addr, error) {
	return r.addrs, r.err
}

type failingRow struct{}

func (failingRow) Scan(...any) error { return pgx.ErrNoRows }

type failingRowDatabase struct{ stubDatabase }

func (failingRowDatabase) QueryRow(context.Context, string, ...any) pgx.Row { return failingRow{} }

func newPreflightService(t *testing.T, resolver staticDestinationResolver) *Service {
	t.Helper()
	service, err := NewService(failingRowDatabase{}, stubProvider{}, 10, discardLogger(), strings.NewReader(""))
	if err != nil {
		t.Fatal(err)
	}
	service.resolver = resolver
	return service
}

func TestCreateEndpointRejectsProhibitedDNSAnswer(t *testing.T) {
	t.Parallel()
	service := newPreflightService(t, staticDestinationResolver{
		addrs: []netip.Addr{netip.MustParseAddr("127.0.0.1")},
	})
	_, _, err := service.CreateEndpoint(context.Background(), Identity{OrganizationID: "0198c0de-0000-7000-8000-000000000002"}, RequestMeta{},
		CreateInput{Name: "orders", URL: "https://hooks.example.test/hook"})
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 400 || httpErr.Code != "invalid_url" || httpErr.Field != "url" {
		t.Fatalf("error = %#v, want 400 invalid_url on url", err)
	}
}

func TestCreateEndpointAllowsDNSFailure(t *testing.T) {
	t.Parallel()
	service := newPreflightService(t, staticDestinationResolver{err: errors.New("lookup failed")})
	// Reaching the database layer proves the advisory preflight allowed the
	// syntactically valid endpoint.
	_, _, err := service.CreateEndpoint(context.Background(), Identity{OrganizationID: "0198c0de-0000-7000-8000-000000000002"}, RequestMeta{},
		CreateInput{Name: "orders", URL: "https://hooks.example.test/hook"})
	var httpErr *HTTPError
	if errors.As(err, &httpErr) && httpErr.Code == "invalid_url" {
		t.Fatalf("DNS failure rejected the endpoint: %v", err)
	}
}

func TestPatchEndpointRejectsProhibitedDNSAnswer(t *testing.T) {
	t.Parallel()
	service := newPreflightService(t, staticDestinationResolver{
		addrs: []netip.Addr{netip.MustParseAddr("10.0.0.1")},
	})
	raw := "https://hooks.example.test/hook"
	_, err := service.PatchEndpoint(context.Background(), Identity{OrganizationID: "0198c0de-0000-7000-8000-000000000002"}, RequestMeta{},
		"0198c0de-0000-7000-8000-000000000001", PatchInput{URL: &raw}, "")
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) || httpErr.Status != 400 || httpErr.Code != "invalid_url" || httpErr.Field != "url" {
		t.Fatalf("error = %#v, want 400 invalid_url on url", err)
	}
}
