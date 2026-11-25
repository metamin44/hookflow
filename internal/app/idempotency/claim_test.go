package idempotency

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type countingQuerier struct {
	calls int
}

func (q *countingQuerier) QueryRow(context.Context, string, ...any) pgx.Row {
	q.calls++
	return failingRow{}
}

type failingRow struct{}

func (failingRow) Scan(...any) error { return errors.New("unexpected query") }

func TestClaimRejectsWrongHashLength(t *testing.T) {
	for _, size := range []int{0, 31, 33} {
		querier := &countingQuerier{}
		_, err := Claim(context.Background(), querier, ClaimInput{
			RecordID:       "01920000-0000-7000-8000-000000000001",
			OrganizationID: "01920000-0000-7000-8000-000000000002",
			Key:            "key",
			Hash:           make([]byte, size),
		})
		if err == nil {
			t.Fatalf("%d-byte hash accepted", size)
		}
		if !strings.Contains(err.Error(), "must be 32 bytes") {
			t.Errorf("%d-byte hash error = %v", size, err)
		}
		if querier.calls != 0 {
			t.Errorf("%d-byte hash executed %d queries, want 0", size, querier.calls)
		}
	}
}
