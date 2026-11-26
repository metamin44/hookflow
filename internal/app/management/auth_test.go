package management

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/metamin44/hookflow/internal/modules/apikey"
)

type fakeKeyStore struct {
	record apikey.AuthRecord
	err    error
	calls  int
}

func (f *fakeKeyStore) QueryRow(context.Context, string, ...any) pgx.Row {
	f.calls++
	values := []any{
		f.record.ID, f.record.OrganizationID, f.record.Name, f.record.KeyPrefix,
		f.record.SecretHash, f.record.HashAlgorithm, f.record.Scopes,
		f.record.LastUsedAt, f.record.ExpiresAt, f.record.RevokedAt, f.record.CreatedAt,
		f.record.OrganizationStatus,
	}
	return fakeRow{values: values, err: f.err}
}

func (f *fakeKeyStore) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected query")
}

type fakeRow struct {
	values []any
	err    error
}

func (r fakeRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	for i := range dest {
		target := reflect.ValueOf(dest[i]).Elem()
		value := r.values[i]
		if value == nil {
			target.Set(reflect.Zero(target.Type()))
			continue
		}
		source := reflect.ValueOf(value)
		if !source.Type().AssignableTo(target.Type()) {
			source = source.Convert(target.Type())
		}
		target.Set(source)
	}
	return nil
}

var _ apikey.Querier = (*fakeKeyStore)(nil)

// Postgres-only import guard: the concrete pool must satisfy the authenticator.
var _ apikey.Querier = (*pgxpool.Pool)(nil)

func authenticatorFixture(t *testing.T, record apikey.AuthRecord, _ string) (*Authenticator, *fakeKeyStore) {
	t.Helper()
	store := &fakeKeyStore{record: record}
	authenticator, err := NewAuthenticator(store, apikey.NewVerifier())
	if err != nil {
		t.Fatalf("NewAuthenticator() = %v", err)
	}
	fixed := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	authenticator.now = func() time.Time { return fixed }
	return authenticator, store
}

func hashedToken(t *testing.T, token string) string {
	t.Helper()
	encoded, err := apikey.HashWithReader(token, strings.NewReader(strings.Repeat("s", apikey.ArgonSaltSize)))
	if err != nil {
		t.Fatalf("HashWithReader() = %v", err)
	}
	return encoded
}

func tokenFixture(t *testing.T) (string, string) {
	t.Helper()
	token, prefix, err := apikey.Generate(strings.NewReader(strings.Repeat("t", apikey.TokenBytes)))
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	return token, prefix
}

func baseRecord(prefix, hash string) apikey.AuthRecord {
	return apikey.AuthRecord{
		Record: apikey.Record{
			ID:             "0198c0de-0000-7000-8000-000000000010",
			OrganizationID: "0198c0de-0000-7000-8000-000000000020",
			Name:           "ci",
			KeyPrefix:      prefix,
			SecretHash:     hash,
			HashAlgorithm:  apikey.Algorithm(),
			Scopes:         []string{"endpoints:write"},
			CreatedAt:      time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		OrganizationStatus: "active",
	}
}

func TestAuthenticateValidKey(t *testing.T) {
	token, prefix := tokenFixture(t)
	authenticator, store := authenticatorFixture(t, baseRecord(prefix, hashedToken(t, token)), token)
	identity, scopes, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
	if err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
	if identity.OrganizationID != "0198c0de-0000-7000-8000-000000000020" || identity.APIKeyID == "" {
		t.Fatalf("identity = %+v", identity)
	}
	if !scopes.AllowsEndpointWrite() || !scopes.AllowsEndpointRead() {
		t.Fatalf("scopes = %+v, want endpoints:write implication", scopes)
	}
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
}

func TestAuthenticateRejectsWrongSecret(t *testing.T) {
	token, prefix := tokenFixture(t)
	other := strings.Repeat("u", apikey.TokenBytes)
	otherToken, _, err := apikey.Generate(strings.NewReader(other))
	if err != nil {
		t.Fatalf("Generate() = %v", err)
	}
	authenticator, _ := authenticatorFixture(t, baseRecord(prefix, hashedToken(t, otherToken)), token)
	_, _, authErr := authenticator.Authenticate(context.Background(), "Bearer "+token)
	assertUnauthorized(t, authErr, "invalid_api_key")
}

func TestAuthenticateRejectsRevokedAndExpired(t *testing.T) {
	token, prefix := tokenFixture(t)
	t.Run("revoked", func(t *testing.T) {
		record := baseRecord(prefix, hashedToken(t, token))
		revoked := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
		record.RevokedAt = &revoked
		authenticator, _ := authenticatorFixture(t, record, token)
		_, _, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
		assertUnauthorized(t, err, "invalid_api_key")
	})
	t.Run("expired", func(t *testing.T) {
		record := baseRecord(prefix, hashedToken(t, token))
		expired := time.Date(2026, 11, 24, 11, 59, 59, 0, time.UTC)
		record.ExpiresAt = &expired
		authenticator, _ := authenticatorFixture(t, record, token)
		_, _, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
		assertUnauthorized(t, err, "invalid_api_key")
	})
	t.Run("expires in the future", func(t *testing.T) {
		record := baseRecord(prefix, hashedToken(t, token))
		future := time.Date(2026, 11, 24, 12, 0, 1, 0, time.UTC)
		record.ExpiresAt = &future
		authenticator, _ := authenticatorFixture(t, record, token)
		if _, _, err := authenticator.Authenticate(context.Background(), "Bearer "+token); err != nil {
			t.Fatalf("Authenticate() = %v", err)
		}
	})
}

func TestAuthenticateUnknownPrefixPerformsDummyVerification(t *testing.T) {
	token, _ := tokenFixture(t)
	authenticator, store := authenticatorFixture(t, apikey.AuthRecord{}, token)
	store.err = pgx.ErrNoRows
	_, _, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
	assertUnauthorized(t, err, "invalid_api_key")
	if store.calls != 1 {
		t.Fatalf("store calls = %d, want 1", store.calls)
	}
}

func TestAuthenticateMalformedTokenSkipsStore(t *testing.T) {
	authenticator, store := authenticatorFixture(t, apikey.AuthRecord{}, "")
	for _, header := range []string{"", "Bearer", "Bearer short", "Basic abc", "bearer", "Bearer a b"} {
		_, _, err := authenticator.Authenticate(context.Background(), header)
		assertUnauthorized(t, err, "invalid_api_key")
	}
	if store.calls != 0 {
		t.Fatalf("store calls = %d, want 0", store.calls)
	}
}

func TestAuthenticateBearerSchemeIsCaseInsensitive(t *testing.T) {
	token, prefix := tokenFixture(t)
	authenticator, _ := authenticatorFixture(t, baseRecord(prefix, hashedToken(t, token)), token)
	if _, _, err := authenticator.Authenticate(context.Background(), "bEaReR "+token); err != nil {
		t.Fatalf("Authenticate() = %v", err)
	}
}

func TestAuthenticatePropagatesOrganizationState(t *testing.T) {
	token, prefix := tokenFixture(t)
	hash := hashedToken(t, token)
	for _, status := range []string{"suspended", "archived"} {
		t.Run(status, func(t *testing.T) {
			record := baseRecord(prefix, hash)
			record.OrganizationStatus = status
			authenticator, _ := authenticatorFixture(t, record, token)
			identity, _, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
			if err != nil {
				t.Fatalf("Authenticate() = %v, want success for read-only enforcement", err)
			}
			if identity.OrganizationStatus != status {
				t.Fatalf("status = %q, want %q", identity.OrganizationStatus, status)
			}
		})
	}
}

func TestAuthenticateInvalidStoredVerifier(t *testing.T) {
	token, prefix := tokenFixture(t)
	authenticator, _ := authenticatorFixture(t, baseRecord(prefix, "not-a-phc-value"), token)
	_, _, err := authenticator.Authenticate(context.Background(), "Bearer "+token)
	assertUnauthorized(t, err, "invalid_api_key")
}

func assertUnauthorized(t *testing.T, err error, code string) {
	t.Helper()
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("error = %v, want an HTTPError", err)
	}
	if httpErr.Status != 401 || httpErr.Code != code {
		t.Fatalf("error = %d/%s, want 401/%s", httpErr.Status, httpErr.Code, code)
	}
	if got := httpErr.Headers["WWW-Authenticate"]; got != "Bearer" {
		t.Fatalf("WWW-Authenticate = %q, want %q", got, "Bearer")
	}
}
