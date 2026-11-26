package eventapp

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/modules/event"
)

type forbiddenDatabase struct {
	begins int
}

func (d *forbiddenDatabase) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	d.begins++
	return nil, errors.New("begin must not be called")
}

func TestNewServiceRequiresDatabase(t *testing.T) {
	if _, err := NewService(nil, Options{}); err == nil {
		t.Fatal("nil database accepted")
	}
	db := &forbiddenDatabase{}
	service, err := NewService(db, Options{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	if service.options.NewID == nil {
		t.Error("default UUIDv7 generator not installed")
	}
	if service.logger == nil {
		t.Error("default logger not installed")
	}
}

// TestAcceptEventRejectsBeforeTransaction proves body validation happens
// before any database work: a parse rejection must not open a transaction.
func TestAcceptEventRejectsBeforeTransaction(t *testing.T) {
	cases := []struct {
		name     string
		input    AcceptInput
		wantCode string
	}{
		{
			name:  "missing organization",
			input: AcceptInput{IdempotencyKey: "key", Body: []byte(`{"event_type":"a.b","payload":{}}`)},
		},
		{
			name:  "missing idempotency key",
			input: AcceptInput{OrganizationID: "01920000-0000-7000-8000-000000000001", Body: []byte(`{"event_type":"a.b","payload":{}}`)},
		},
		{
			name: "non-object document",
			input: AcceptInput{
				OrganizationID: "01920000-0000-7000-8000-000000000001",
				IdempotencyKey: "key",
				Body:           []byte(`[]`),
			},
			wantCode: event.CodeInvalidDocument,
		},
		{
			name: "numeric event type",
			input: AcceptInput{
				OrganizationID: "01920000-0000-7000-8000-000000000001",
				IdempotencyKey: "key",
				Body:           []byte(`{"event_type":1,"payload":{}}`),
			},
			wantCode: event.CodeInvalidEventType,
		},
		{
			name: "non-object payload",
			input: AcceptInput{
				OrganizationID: "01920000-0000-7000-8000-000000000001",
				IdempotencyKey: "key",
				Body:           []byte(`{"event_type":"a.b","payload":[]}`),
			},
			wantCode: event.CodeInvalidDocument,
		},
		{
			name: "duplicate key",
			input: AcceptInput{
				OrganizationID: "01920000-0000-7000-8000-000000000001",
				IdempotencyKey: "key",
				Body:           []byte(`{"event_type":"a.b","payload":{},"payload":{}}`),
			},
			wantCode: event.CodeDuplicateKey,
		},
	}
	db := &forbiddenDatabase{}
	service, err := NewService(db, Options{})
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := service.AcceptEvent(context.Background(), tc.input)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if tc.wantCode == "" {
				return
			}
			var rejection *event.Rejection
			if !errors.As(err, &rejection) {
				t.Fatalf("error %v is not a *event.Rejection", err)
			}
			if rejection.Code != tc.wantCode {
				t.Errorf("code = %q, want %q", rejection.Code, tc.wantCode)
			}
		})
	}
	if db.begins != 0 {
		t.Fatalf("rejections opened %d transactions, want 0", db.begins)
	}
}
