package apikey

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

func TestMetadataShapeExcludesAuthenticationMaterial(t *testing.T) {
	typ := reflect.TypeOf(Metadata{})
	for _, forbidden := range []string{"SecretHash", "Token", "RawToken", "Salt", "Verifier"} {
		if _, ok := typ.FieldByName(forbidden); ok {
			t.Fatalf("Metadata unexpectedly has field %q", forbidden)
		}
	}
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if field.Anonymous {
			t.Fatalf("Metadata must not embed %q", field.Name)
		}
		lower := strings.ToLower(field.Name)
		for _, forbidden := range []string{"secret", "token", "salt", "verifier"} {
			if strings.Contains(lower, forbidden) {
				t.Fatalf("Metadata field %q looks like authentication material", field.Name)
			}
		}
	}
	want := []string{
		"ID", "OrganizationID", "Name", "KeyPrefix", "HashAlgorithm",
		"Scopes", "LastUsedAt", "ExpiresAt", "RevokedAt", "CreatedAt",
	}
	if typ.NumField() != len(want) {
		t.Fatalf("Metadata has %d fields, want %d", typ.NumField(), len(want))
	}
	for i, name := range want {
		if typ.Field(i).Name != name {
			t.Fatalf("Metadata field %d = %q, want %q", i, typ.Field(i).Name, name)
		}
	}
}

func TestRecordMetadataExcludesVerifier(t *testing.T) {
	secret := "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$dmVyaWZpZXI"
	lastUsed := time.Date(2026, 11, 25, 8, 0, 0, 0, time.UTC)
	expires := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	revoked := time.Date(2026, 11, 25, 9, 0, 0, 0, time.UTC)
	created := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	record := Record{
		ID:             "key-id",
		OrganizationID: "org-id",
		Name:           "ci",
		KeyPrefix:      "abcd1234",
		SecretHash:     secret,
		HashAlgorithm:  Algorithm(),
		Scopes:         []string{ScopeEndpointsRead, ScopeEventsWrite},
		LastUsedAt:     &lastUsed,
		ExpiresAt:      &expires,
		RevokedAt:      &revoked,
		CreatedAt:      created,
	}
	got := record.Metadata()
	want := Metadata{
		ID:             "key-id",
		OrganizationID: "org-id",
		Name:           "ci",
		KeyPrefix:      "abcd1234",
		HashAlgorithm:  Algorithm(),
		Scopes:         []string{ScopeEndpointsRead, ScopeEventsWrite},
		LastUsedAt:     &lastUsed,
		ExpiresAt:      &expires,
		RevokedAt:      &revoked,
		CreatedAt:      created,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Metadata() = %+v, want %+v", got, want)
	}
	rendered := fmt.Sprintf("%+v", got)
	for _, forbidden := range []string{secret, "c2FsdA", "dmVyaWZpZXI"} {
		if strings.Contains(rendered, forbidden) {
			t.Fatal("Metadata rendering exposed verifier material")
		}
	}
}

func TestListReturnsMetadataWithoutVerifier(t *testing.T) {
	createdNew := time.Date(2026, 11, 25, 12, 0, 0, 0, time.UTC)
	createdOld := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	revokedAt := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	q := &testQuerier{rows: &testRows{rows: [][]any{
		metadataValues(Metadata{
			ID: "new-key", OrganizationID: "org-id", Name: "new", KeyPrefix: "newp0001",
			HashAlgorithm: Algorithm(), Scopes: []string{ScopeEventsWrite}, CreatedAt: createdNew,
		}),
		metadataValues(Metadata{
			ID: "old-key", OrganizationID: "org-id", Name: "old", KeyPrefix: "oldp0001",
			HashAlgorithm: Algorithm(), Scopes: []string{ScopeEventsWrite},
			RevokedAt: &revokedAt, CreatedAt: createdOld,
		}),
	}}}

	got, err := List(context.Background(), q, "org-id")
	if err != nil {
		t.Fatalf("List() = %v", err)
	}
	if len(got) != 2 || got[0].ID != "new-key" || got[1].ID != "old-key" {
		t.Fatalf("List() order = %+v", got)
	}
	if got[0].CreatedAt != createdNew || got[1].CreatedAt != createdOld {
		t.Fatal("List() did not preserve created_at ordering values")
	}
	if got[1].RevokedAt == nil || !got[1].RevokedAt.Equal(revokedAt) {
		t.Fatal("List() omitted a revoked key or its revoked_at")
	}
	for _, record := range got {
		if record.OrganizationID != "org-id" {
			t.Fatalf("List() crossed organization boundary: %+v", record)
		}
	}
	if !q.rows.(*testRows).closed {
		t.Fatal("List() did not close rows")
	}
	assertQueryHasNoSecretHash(t, q.queries[0])
	sql := q.queries[0]
	if !strings.Contains(sql, "WHERE k.organization_id = $1::uuid") {
		t.Fatal("List() query is not organization-scoped")
	}
	if !strings.Contains(sql, "ORDER BY k.created_at DESC, k.id DESC") {
		t.Fatal("List() query lost its ordering")
	}
	assertArgs(t, q.args[0], "org-id")
}

func TestRevokeReturnsMetadataWithoutVerifier(t *testing.T) {
	revokedAt := time.Date(2026, 11, 25, 13, 0, 0, 0, time.UTC)
	created := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	q := &testQuerier{rowResults: []pgx.Row{testRow{values: metadataValues(Metadata{
		ID: "key-id", OrganizationID: "org-id", Name: "ci", KeyPrefix: "abcd1234",
		HashAlgorithm: Algorithm(), Scopes: []string{ScopeEndpointsWrite},
		RevokedAt: &revokedAt, CreatedAt: created,
	})}}}

	got, changed, err := Revoke(context.Background(), q, "org-id", "key-id")
	if err != nil {
		t.Fatalf("Revoke() = %v", err)
	}
	if !changed {
		t.Fatal("Revoke() did not report a mutation")
	}
	if got.ID != "key-id" || got.Name != "ci" || got.RevokedAt == nil || !got.RevokedAt.Equal(revokedAt) {
		t.Fatalf("Revoke() metadata = %+v", got)
	}
	if len(q.queries) != 1 {
		t.Fatalf("Revoke() queries = %d, want 1", len(q.queries))
	}
	assertQueryHasNoSecretHash(t, q.queries[0])
	if !strings.Contains(q.queries[0], "RETURNING") {
		t.Fatal("Revoke() query lost its RETURNING projection")
	}
	assertArgs(t, q.args[0], "org-id", "key-id")
}

func TestRevokeNoOpAndMissingReturnMetadataOnly(t *testing.T) {
	created := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	t.Run("already revoked", func(t *testing.T) {
		q := &testQuerier{rowResults: []pgx.Row{
			testRow{err: pgx.ErrNoRows},
			testRow{values: metadataValues(Metadata{
				ID: "key-id", OrganizationID: "org-id", Name: "ci", KeyPrefix: "abcd1234",
				HashAlgorithm: Algorithm(), Scopes: []string{ScopeEventsWrite}, CreatedAt: created,
			})},
		}}
		got, changed, err := Revoke(context.Background(), q, "org-id", "key-id")
		if err != nil {
			t.Fatalf("Revoke() = %v", err)
		}
		if changed {
			t.Fatal("Revoke() reported a mutation for an already-revoked key")
		}
		if got.ID != "key-id" {
			t.Fatalf("Revoke() metadata = %+v", got)
		}
		if len(q.queries) != 2 {
			t.Fatalf("Revoke() queries = %d, want update plus fallback lookup", len(q.queries))
		}
		for _, query := range q.queries {
			assertQueryHasNoSecretHash(t, query)
		}
	})
	t.Run("missing key", func(t *testing.T) {
		q := &testQuerier{rowResults: []pgx.Row{
			testRow{err: pgx.ErrNoRows},
			testRow{err: pgx.ErrNoRows},
		}}
		if _, _, err := Revoke(context.Background(), q, "org-id", "key-id"); !errors.Is(err, ErrNotFound) {
			t.Fatalf("Revoke() error = %v, want ErrNotFound", err)
		}
		if len(q.queries) != 2 {
			t.Fatalf("Revoke() queries = %d, want update plus lookup", len(q.queries))
		}
		for _, query := range q.queries {
			assertQueryHasNoSecretHash(t, query)
		}
	})
}

func TestFindForAuthStillRetrievesVerifier(t *testing.T) {
	secret := "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$dmVyaWZpZXI"
	created := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	q := &testQuerier{rowResults: []pgx.Row{testRow{values: []any{
		"key-id", "org-id", "ci", "abcd1234", secret, Algorithm(),
		[]string{ScopeEventsWrite}, nil, nil, nil, created, "active",
	}}}}

	got, err := FindForAuth(context.Background(), q, "abcd1234")
	if err != nil {
		t.Fatalf("FindForAuth() = %v", err)
	}
	if got.SecretHash != secret || got.OrganizationStatus != "active" || got.ID != "key-id" {
		t.Fatalf("FindForAuth() = %+v", got)
	}
	if !strings.Contains(q.queries[0], "secret_hash") {
		t.Fatal("FindForAuth() no longer selects the verifier")
	}
	assertArgs(t, q.args[0], "abcd1234")
}

func TestInsertReturnsDatabaseCreatedAt(t *testing.T) {
	secret := "$argon2id$v=19$m=19456,t=2,p=1$c2FsdA$dmVyaWZpZXI"
	created := time.Date(2026, 11, 25, 10, 11, 12, 0, time.UTC)
	q := &testQuerier{rowResults: []pgx.Row{testRow{values: []any{"key-id", created}}}}

	got, err := Insert(context.Background(), q, Record{
		ID:             "key-id",
		OrganizationID: "org-id",
		Name:           "ci",
		KeyPrefix:      "abcd1234",
		SecretHash:     secret,
		HashAlgorithm:  Algorithm(),
		Scopes:         []string{ScopeEventsWrite},
		CreatedAt:      time.Time{},
	})
	if err != nil {
		t.Fatalf("Insert() = %v", err)
	}
	if !got.Equal(created) {
		t.Fatalf("Insert() created_at = %v, want %v", got, created)
	}
	if len(q.queries) != 1 {
		t.Fatalf("Insert() queries = %d, want 1", len(q.queries))
	}
	if !strings.Contains(q.queries[0], "RETURNING id, created_at") {
		t.Fatal("Insert() does not return the database created_at")
	}
	if !strings.Contains(q.queries[0], "secret_hash") {
		t.Fatal("Insert() no longer persists the verifier")
	}
	if len(q.args[0]) != 8 || q.args[0][4] != secret {
		t.Fatalf("Insert() args = %v, want the verifier at position 4", q.args[0])
	}
}

func metadataValues(record Metadata) []any {
	return []any{
		record.ID, record.OrganizationID, record.Name, record.KeyPrefix,
		record.HashAlgorithm, record.Scopes, record.LastUsedAt,
		record.ExpiresAt, record.RevokedAt, record.CreatedAt,
	}
}

func assertQueryHasNoSecretHash(t *testing.T, query string) {
	t.Helper()
	if strings.Contains(strings.ToLower(query), "secret_hash") {
		t.Fatalf("query selects verifier material: %s", query)
	}
}

func assertArgs(t *testing.T, got []any, want ...string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("query args = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("query args = %v, want %v", got, want)
		}
	}
}

type testQuerier struct {
	queries    []string
	args       [][]any
	rows       pgx.Rows
	rowsErr    error
	rowResults []pgx.Row
	rowIndex   int
}

func (q *testQuerier) Query(_ context.Context, sql string, args ...any) (pgx.Rows, error) {
	q.queries = append(q.queries, sql)
	q.args = append(q.args, append([]any(nil), args...))
	return q.rows, q.rowsErr
}

func (q *testQuerier) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	q.queries = append(q.queries, sql)
	q.args = append(q.args, append([]any(nil), args...))
	if q.rowIndex >= len(q.rowResults) {
		return testRow{err: pgx.ErrNoRows}
	}
	row := q.rowResults[q.rowIndex]
	q.rowIndex++
	return row
}

type testRow struct {
	values []any
	err    error
}

func (r testRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	return assignRow(dest, r.values)
}

type testRows struct {
	rows    [][]any
	index   int
	current []any
	iterErr error
	closed  bool
}

func (r *testRows) Close()                                       { r.closed = true }
func (r *testRows) Err() error                                   { return r.iterErr }
func (r *testRows) CommandTag() pgconn.CommandTag                { return pgconn.CommandTag{} }
func (r *testRows) FieldDescriptions() []pgconn.FieldDescription { return nil }
func (r *testRows) RawValues() [][]byte                          { return nil }
func (r *testRows) Conn() *pgx.Conn                              { return nil }
func (r *testRows) TypeMap() *pgtype.Map                         { return nil }

func (r *testRows) Next() bool {
	if r.index >= len(r.rows) {
		return false
	}
	r.current = r.rows[r.index]
	r.index++
	return true
}

func (r *testRows) Scan(dest ...any) error {
	return assignRow(dest, r.current)
}

func (r *testRows) Values() ([]any, error) {
	return append([]any(nil), r.current...), nil
}

func assignRow(dest []any, values []any) error {
	if len(dest) != len(values) {
		return fmt.Errorf("scan destinations = %d, values = %d", len(dest), len(values))
	}
	for i := range dest {
		if err := assignValue(dest[i], values[i]); err != nil {
			return fmt.Errorf("column %d: %w", i, err)
		}
	}
	return nil
}

func assignValue(dest any, value any) error {
	target := reflect.ValueOf(dest)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return errors.New("scan destination is not a non-nil pointer")
	}
	elem := target.Elem()
	if value == nil {
		elem.Set(reflect.Zero(elem.Type()))
		return nil
	}
	source := reflect.ValueOf(value)
	if source.Type().AssignableTo(elem.Type()) {
		elem.Set(source)
		return nil
	}
	if source.Type().ConvertibleTo(elem.Type()) {
		elem.Set(source.Convert(elem.Type()))
		return nil
	}
	return fmt.Errorf("%s is not assignable to %s", source.Type(), elem.Type())
}
