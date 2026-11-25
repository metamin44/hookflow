package accesskeys

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metamin44/hookflow/internal/modules/apikey"
)

const testKeyID = "018f0000-0000-7000-8000-000000000010"

type serviceDatabase struct {
	tx       *serviceTx
	beginErr error
}

func (d serviceDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	if d.beginErr != nil {
		return nil, d.beginErr
	}
	if d.tx == nil {
		return nil, errors.New("test transaction is not configured")
	}
	return d.tx, nil
}

func (d serviceDatabase) QueryRow(context.Context, string, ...any) pgx.Row {
	return serviceRow{err: errors.New("unexpected database QueryRow")}
}

func (d serviceDatabase) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected database Query")
}

type serviceTx struct {
	queryRow      func(sql string, args ...any) pgx.Row
	commitErr     error
	commitCount   int
	rollbackCount int
}

func (tx *serviceTx) Begin(context.Context) (pgx.Tx, error) {
	return nil, errors.New("nested transactions are not supported")
}

func (tx *serviceTx) Commit(context.Context) error {
	tx.commitCount++
	return tx.commitErr
}

func (tx *serviceTx) Rollback(context.Context) error {
	if tx.commitCount > 0 {
		return pgx.ErrTxClosed
	}
	tx.rollbackCount++
	return nil
}

func (tx *serviceTx) CopyFrom(context.Context, pgx.Identifier, []string, pgx.CopyFromSource) (int64, error) {
	return 0, errors.New("unexpected CopyFrom")
}

func (tx *serviceTx) SendBatch(context.Context, *pgx.Batch) pgx.BatchResults {
	return nil
}

func (tx *serviceTx) LargeObjects() pgx.LargeObjects {
	return pgx.LargeObjects{}
}

func (tx *serviceTx) Prepare(context.Context, string, string) (*pgconn.StatementDescription, error) {
	return nil, errors.New("unexpected Prepare")
}

func (tx *serviceTx) Exec(context.Context, string, ...any) (pgconn.CommandTag, error) {
	return pgconn.CommandTag{}, errors.New("unexpected Exec")
}

func (tx *serviceTx) Query(context.Context, string, ...any) (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (tx *serviceTx) QueryRow(_ context.Context, sql string, args ...any) pgx.Row {
	if tx.queryRow == nil {
		return serviceRow{err: errors.New("unexpected transaction QueryRow")}
	}
	return tx.queryRow(sql, args...)
}

func (tx *serviceTx) Conn() *pgx.Conn {
	return nil
}

var _ pgx.Tx = (*serviceTx)(nil)

type serviceRow struct {
	values []any
	err    error
}

func (r serviceRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	if len(dest) != len(r.values) {
		return fmt.Errorf("scan destinations = %d, values = %d", len(dest), len(r.values))
	}
	for i := range dest {
		target := reflect.ValueOf(dest[i])
		if target.Kind() != reflect.Pointer || target.IsNil() {
			return errors.New("scan destination is not a non-nil pointer")
		}
		elem := target.Elem()
		if r.values[i] == nil {
			elem.Set(reflect.Zero(elem.Type()))
			continue
		}
		source := reflect.ValueOf(r.values[i])
		if !source.Type().AssignableTo(elem.Type()) {
			return fmt.Errorf("%s is not assignable to %s", source.Type(), elem.Type())
		}
		elem.Set(source)
	}
	return nil
}

func newBoundaryService(t *testing.T, db Database) (*Service, string, string) {
	t.Helper()
	payload := bytes.Repeat([]byte{0x42}, apikey.TokenBytes)
	wantToken, wantPrefix, err := apikey.Generate(bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("generate expected token: %v", err)
	}
	service, err := NewService(db, bytes.NewReader(payload))
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	service.newID = func() (string, error) { return testKeyID, nil }
	return service, wantToken, wantPrefix
}

func createTestTx(createdAt time.Time, insertErr error) (*serviceTx, *[]any, *int) {
	var insertArgs []any
	auditCalls := 0
	tx := &serviceTx{queryRow: func(sql string, args ...any) pgx.Row {
		switch {
		case strings.Contains(sql, "FROM organizations"):
			return serviceRow{values: []any{"active"}}
		case strings.Contains(sql, "INSERT INTO api_keys"):
			insertArgs = append([]any(nil), args...)
			if insertErr != nil {
				return serviceRow{err: insertErr}
			}
			return serviceRow{values: []any{testKeyID, createdAt}}
		case strings.Contains(sql, "INSERT INTO audit_logs"):
			auditCalls++
			return serviceRow{values: []any{"audit-id"}}
		default:
			return serviceRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
		}
	}}
	return tx, &insertArgs, &auditCalls
}

func TestCreateReturnsPersistedMetadataOnly(t *testing.T) {
	createdAt := time.Date(2026, 11, 25, 10, 11, 12, 0, time.UTC)
	expiresAt := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)
	tx, insertArgs, auditCalls := createTestTx(createdAt, nil)
	service, wantToken, wantPrefix := newBoundaryService(t, serviceDatabase{tx: tx})

	token, metadata, err := service.Create(context.Background(), "018f0000-0000-7000-8000-000000000001",
		"operator", []string{apikey.ScopeEventsWrite, apikey.ScopeEndpointsRead}, &expiresAt)
	if err != nil {
		t.Fatalf("Create() = %v", err)
	}
	if token != wantToken {
		t.Fatal("Create() did not return the raw one-time token")
	}
	if metadata.ID != testKeyID || metadata.OrganizationID != "018f0000-0000-7000-8000-000000000001" ||
		metadata.Name != "operator" || metadata.KeyPrefix != wantPrefix ||
		metadata.HashAlgorithm != apikey.Algorithm() {
		t.Fatalf("Create() metadata = %+v", metadata)
	}
	if !reflect.DeepEqual(metadata.Scopes, []string{apikey.ScopeEndpointsRead, apikey.ScopeEventsWrite}) {
		t.Fatalf("Create() scopes = %v", metadata.Scopes)
	}
	if metadata.ExpiresAt == nil || !metadata.ExpiresAt.Equal(expiresAt) {
		t.Fatalf("Create() expiry = %v", metadata.ExpiresAt)
	}
	if !metadata.CreatedAt.Equal(createdAt) {
		t.Fatalf("Create() CreatedAt = %v, want persisted %v", metadata.CreatedAt, createdAt)
	}
	if metadata.RevokedAt != nil || metadata.LastUsedAt != nil {
		t.Fatalf("Create() returned unexpected lifecycle fields: %+v", metadata)
	}

	if len(*insertArgs) != 8 {
		t.Fatalf("Insert() args = %d, want 8", len(*insertArgs))
	}
	verifier, ok := (*insertArgs)[4].(string)
	if !ok || !strings.HasPrefix(verifier, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Fatal("Insert() did not receive the Argon2id verifier")
	}
	if strings.Contains(verifier, wantToken) {
		t.Fatal("Insert() received a verifier containing the raw token")
	}
	if storedExpiry, ok := (*insertArgs)[7].(*time.Time); !ok || !storedExpiry.Equal(expiresAt) {
		t.Fatal("Insert() did not receive the requested expiry")
	}

	if tx.commitCount != 1 {
		t.Fatalf("commit count = %d, want 1", tx.commitCount)
	}
	if *auditCalls != 1 {
		t.Fatalf("audit calls = %d, want 1", *auditCalls)
	}
	rendered := fmt.Sprintf("%+v", metadata)
	for _, forbidden := range []string{verifier, wantToken, "$argon2", "secret"} {
		if strings.Contains(strings.ToLower(rendered), strings.ToLower(forbidden)) {
			t.Fatal("Create() metadata exposed sensitive material")
		}
	}
}

func TestCreateFailuresReturnEmptyResult(t *testing.T) {
	createdAt := time.Date(2026, 11, 25, 10, 11, 12, 0, time.UTC)
	tests := []struct {
		name       string
		db         Database
		wantCommit int
	}{
		{
			name: "begin",
			db:   serviceDatabase{beginErr: errors.New("begin failed")},
		},
		{
			name: "insert",
			db: func() Database {
				tx, _, _ := createTestTx(createdAt, errors.New("insert failed"))
				return serviceDatabase{tx: tx}
			}(),
		},
		{
			name: "audit",
			db: func() Database {
				tx, _, _ := createTestTx(createdAt, nil)
				baseQueryRow := tx.queryRow
				tx.queryRow = func(sql string, args ...any) pgx.Row {
					if strings.Contains(sql, "INSERT INTO audit_logs") {
						return serviceRow{err: errors.New("audit failed")}
					}
					return baseQueryRow(sql, args...)
				}
				return serviceDatabase{tx: tx}
			}(),
		},
		{
			name: "commit",
			db: func() Database {
				tx, _, _ := createTestTx(createdAt, nil)
				tx.commitErr = errors.New("commit failed")
				return serviceDatabase{tx: tx}
			}(),
			wantCommit: 1,
		},
		{
			name: "archived",
			db: func() Database {
				tx := &serviceTx{queryRow: func(sql string, _ ...any) pgx.Row {
					if strings.Contains(sql, "FROM organizations") {
						return serviceRow{values: []any{"archived"}}
					}
					return serviceRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
				}}
				return serviceDatabase{tx: tx}
			}(),
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service, _, _ := newBoundaryService(t, testCase.db)
			token, metadata, err := service.Create(context.Background(),
				"018f0000-0000-7000-8000-000000000001", "operator", nil, nil)
			if err == nil {
				t.Fatal("Create() unexpectedly succeeded")
			}
			if token != "" || !reflect.DeepEqual(metadata, apikey.Metadata{}) {
				t.Fatalf("Create() returned token=%q metadata=%+v on failure", token, metadata)
			}
			if tx := transactionOf(testCase.db); tx != nil {
				if tx.commitCount != testCase.wantCommit {
					t.Fatalf("commit count = %d, want %d", tx.commitCount, testCase.wantCommit)
				}
			}
		})
	}
}

func TestRevokePostStorageFailuresReturnEmptyResult(t *testing.T) {
	revokedAt := time.Date(2026, 11, 25, 11, 0, 0, 0, time.UTC)
	createdAt := time.Date(2026, 11, 24, 12, 0, 0, 0, time.UTC)
	revokedMetadata := apiKeyMetadataValues(apikey.Metadata{
		ID:             "key-id",
		OrganizationID: "018f0000-0000-7000-8000-000000000001",
		Name:           "operator",
		KeyPrefix:      "abcd1234",
		HashAlgorithm:  apikey.Algorithm(),
		Scopes:         []string{apikey.ScopeEventsWrite},
		RevokedAt:      &revokedAt,
		CreatedAt:      createdAt,
	})

	tests := []struct {
		name       string
		db         Database
		wantCommit int
	}{
		{
			name: "begin",
			db:   serviceDatabase{beginErr: errors.New("begin failed")},
		},
		{
			name: "update",
			db: func() Database {
				tx := &serviceTx{queryRow: func(sql string, _ ...any) pgx.Row {
					switch {
					case strings.Contains(sql, "FROM organizations"):
						return serviceRow{values: []any{"active"}}
					case strings.Contains(sql, "UPDATE api_keys"):
						return serviceRow{err: errors.New("update failed")}
					default:
						return serviceRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
					}
				}}
				return serviceDatabase{tx: tx}
			}(),
		},
		{
			name: "audit",
			db: func() Database {
				tx := &serviceTx{queryRow: func(sql string, _ ...any) pgx.Row {
					switch {
					case strings.Contains(sql, "FROM organizations"):
						return serviceRow{values: []any{"active"}}
					case strings.Contains(sql, "UPDATE api_keys"):
						return serviceRow{values: revokedMetadata}
					case strings.Contains(sql, "INSERT INTO audit_logs"):
						return serviceRow{err: errors.New("audit failed")}
					default:
						return serviceRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
					}
				}}
				return serviceDatabase{tx: tx}
			}(),
		},
		{
			name: "commit",
			db: func() Database {
				tx := &serviceTx{queryRow: func(sql string, _ ...any) pgx.Row {
					switch {
					case strings.Contains(sql, "FROM organizations"):
						return serviceRow{values: []any{"active"}}
					case strings.Contains(sql, "UPDATE api_keys"):
						return serviceRow{values: revokedMetadata}
					case strings.Contains(sql, "INSERT INTO audit_logs"):
						return serviceRow{values: []any{"audit-id"}}
					default:
						return serviceRow{err: fmt.Errorf("unexpected SQL: %s", sql)}
					}
				}}
				tx.commitErr = errors.New("commit failed")
				return serviceDatabase{tx: tx}
			}(),
			wantCommit: 1,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			service, _, _ := newBoundaryService(t, testCase.db)
			metadata, changed, err := service.Revoke(context.Background(),
				"018f0000-0000-7000-8000-000000000001", testKeyID)
			if err == nil {
				t.Fatal("Revoke() unexpectedly succeeded")
			}
			if changed || !reflect.DeepEqual(metadata, apikey.Metadata{}) {
				t.Fatalf("Revoke() returned changed=%v metadata=%+v on failure", changed, metadata)
			}
			if tx := transactionOf(testCase.db); tx != nil && tx.commitCount != testCase.wantCommit {
				t.Fatalf("commit count = %d, want %d", tx.commitCount, testCase.wantCommit)
			}
		})
	}
}

func transactionOf(db Database) *serviceTx {
	if database, ok := db.(serviceDatabase); ok {
		return database.tx
	}
	return nil
}

func apiKeyMetadataValues(record apikey.Metadata) []any {
	return []any{
		record.ID, record.OrganizationID, record.Name, record.KeyPrefix,
		record.HashAlgorithm, record.Scopes, record.LastUsedAt,
		record.ExpiresAt, record.RevokedAt, record.CreatedAt,
	}
}
