package accesskeys

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/modules/apikey"
)

// panicDatabase fails the test if any query is attempted. The validation-only
// tests below must reject input before touching storage.
type panicDatabase struct{ t *testing.T }

func (p panicDatabase) fail() {
	p.t.Fatal("database access was attempted during input validation")
}

func (p panicDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	p.fail()
	return nil, errors.New("unreachable")
}

func (p panicDatabase) QueryRow(context.Context, string, ...any) pgx.Row {
	p.fail()
	return nil
}

func (p panicDatabase) Query(context.Context, string, ...any) (pgx.Rows, error) {
	p.fail()
	return nil, errors.New("unreachable")
}

func TestValidName(t *testing.T) {
	tests := []struct {
		name  string
		value string
		want  bool
	}{
		{"simple", "operator-key", true},
		{"unicode", "کلید اپراتور", true},
		{"max length", strings.Repeat("a", MaxNameLength), true},
		{"empty", "", false},
		{"too long", strings.Repeat("a", MaxNameLength+1), false},
		{"newline", "line\nbreak", false},
		{"tab", "tab\tname", false},
		{"delete", "delete\x7fname", false},
		{"invalid utf8", string([]byte{0xff, 0xfe}), false},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if got := validName(testCase.value); got != testCase.want {
				t.Fatalf("validName(%q) = %v, want %v", testCase.value, got, testCase.want)
			}
		})
	}
}

func TestNewServiceRequiresDependencies(t *testing.T) {
	if _, err := NewService(nil, strings.NewReader("")); err == nil {
		t.Fatal("NewService accepted a nil database")
	}
	if _, err := NewService(panicDatabase{t: t}, nil); err == nil {
		t.Fatal("NewService accepted a nil randomness source")
	}
}

func TestValidationErrorsCarryNoKeyMaterial(t *testing.T) {
	service, err := NewService(panicDatabase{t: t}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	ctx := context.Background()

	tests := []struct {
		name string
		run  func() error
		want error
	}{
		{"create organization", func() error {
			_, _, err := service.Create(ctx, "not-a-uuid", "operator", nil, nil)
			return err
		}, ErrOrganizationNotFound},
		{"create name", func() error {
			_, _, err := service.Create(ctx, "018f0000-0000-7000-8000-000000000001", "", nil, nil)
			return err
		}, ErrInvalidName},
		{"list organization", func() error {
			_, err := service.List(ctx, "not-a-uuid")
			return err
		}, ErrOrganizationNotFound},
		{"revoke organization", func() error {
			_, _, err := service.Revoke(ctx, "not-a-uuid", "018f0000-0000-7000-8000-000000000002")
			return err
		}, ErrOrganizationNotFound},
		{"revoke key", func() error {
			_, _, err := service.Revoke(ctx, "018f0000-0000-7000-8000-000000000001", "not-a-uuid")
			return err
		}, ErrKeyNotFound},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			got := testCase.run()
			if !errors.Is(got, testCase.want) {
				t.Fatalf("error = %v, want %v", got, testCase.want)
			}
			message := got.Error()
			for _, forbidden := range []string{"$argon2", "salt", "verifier", "whsec_"} {
				if strings.Contains(strings.ToLower(message), forbidden) {
					t.Fatalf("error carried sensitive material: %q", message)
				}
			}
		})
	}
}

func TestRevokeRejectsMalformedKeyBeforeStorage(t *testing.T) {
	service, err := NewService(panicDatabase{t: t}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	if _, _, err := service.Revoke(context.Background(),
		"018f0000-0000-7000-8000-000000000001", "not-a-uuid"); !errors.Is(err, ErrKeyNotFound) {
		t.Fatalf("Revoke malformed key = %v, want ErrKeyNotFound", err)
	}
}

func TestOperatorFailuresReturnNoMetadata(t *testing.T) {
	service, err := NewService(panicDatabase{t: t}, strings.NewReader(""))
	if err != nil {
		t.Fatalf("build service: %v", err)
	}
	ctx := context.Background()
	validOrganization := "018f0000-0000-7000-8000-000000000001"
	validKey := "018f0000-0000-7000-8000-000000000002"

	tests := []struct {
		name string
		run  func() error
	}{
		{"create organization", func() error {
			token, metadata, err := service.Create(ctx, "not-a-uuid", "operator", nil, nil)
			if token != "" || !reflect.DeepEqual(metadata, apikey.Metadata{}) {
				t.Fatalf("Create() returned token=%q metadata=%+v on failure", token, metadata)
			}
			return err
		}},
		{"create name", func() error {
			token, metadata, err := service.Create(ctx, validOrganization, "", nil, nil)
			if token != "" || !reflect.DeepEqual(metadata, apikey.Metadata{}) {
				t.Fatalf("Create() returned token=%q metadata=%+v on failure", token, metadata)
			}
			return err
		}},
		{"list organization", func() error {
			metadata, err := service.List(ctx, "not-a-uuid")
			if metadata != nil {
				t.Fatalf("List() returned metadata=%+v on failure", metadata)
			}
			return err
		}},
		{"revoke organization", func() error {
			metadata, changed, err := service.Revoke(ctx, "not-a-uuid", validKey)
			if changed || !reflect.DeepEqual(metadata, apikey.Metadata{}) {
				t.Fatalf("Revoke() returned changed=%v metadata=%+v on failure", changed, metadata)
			}
			return err
		}},
		{"revoke key", func() error {
			metadata, changed, err := service.Revoke(ctx, validOrganization, "not-a-uuid")
			if changed || !reflect.DeepEqual(metadata, apikey.Metadata{}) {
				t.Fatalf("Revoke() returned changed=%v metadata=%+v on failure", changed, metadata)
			}
			return err
		}},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.run(); err == nil {
				t.Fatal("operation unexpectedly succeeded")
			}
		})
	}
}
