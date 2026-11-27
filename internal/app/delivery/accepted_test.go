package delivery

import (
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

const (
	testID = "01920000-0000-7000-8000-000000000001"
)

func validAccepted() Accepted {
	return Accepted{
		ID:              testID,
		OrganizationID:  "01920000-0000-7000-8000-000000000002",
		EventID:         "01920000-0000-7000-8000-000000000003",
		EndpointID:      "01920000-0000-7000-8000-000000000004",
		SubscriptionID:  "01920000-0000-7000-8000-000000000005",
		URL:             "https://example.test/hook",
		Headers:         `{"content-type":"application/json"}`,
		SecretEncrypted: "",
		NextAttemptAt:   time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC),
	}
}

func TestAppendAcceptedQueuesOnePendingInsert(t *testing.T) {
	batch := &pgx.Batch{}
	if err := AppendAccepted(batch, validAccepted()); err != nil {
		t.Fatalf("AppendAccepted: %v", err)
	}
	if batch.Len() != 1 {
		t.Fatalf("queued %d statements, want 1", batch.Len())
	}
	query := batch.QueuedQueries[0]
	if !strings.Contains(query.SQL, "INSERT INTO deliveries") {
		t.Errorf("unexpected snapshot statement: %s", query.SQL)
	}
	if !strings.Contains(query.SQL, "'pending', 1") {
		t.Errorf("initial state/sequence drift: %s", query.SQL)
	}
	if !strings.Contains(query.SQL, "generation") ||
		!strings.Contains(query.SQL, "ON CONFLICT (event_id, endpoint_id, generation)") {
		t.Errorf("initial generation conflict target drift: %s", query.SQL)
	}
	if len(query.Arguments) != 9 {
		t.Fatalf("queued %d arguments, want 9", len(query.Arguments))
	}
	if got := query.Arguments[7]; got != "" {
		t.Errorf("secret ciphertext argument = %v, want empty string", got)
	}
}

func TestAppendAcceptedRejectsInvalidSnapshots(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*Accepted)
		want   string
	}{
		{name: "missing id", mutate: func(a *Accepted) { a.ID = "" }, want: "accepted delivery id is not a UUID"},
		{name: "bad organization", mutate: func(a *Accepted) { a.OrganizationID = "org" }, want: "accepted delivery organization id is not a UUID"},
		{name: "bad event", mutate: func(a *Accepted) { a.EventID = "event" }, want: "accepted delivery event id is not a UUID"},
		{name: "bad endpoint", mutate: func(a *Accepted) { a.EndpointID = " " }, want: "accepted delivery endpoint id is not a UUID"},
		{name: "bad subscription", mutate: func(a *Accepted) { a.SubscriptionID = "sub" }, want: "accepted delivery subscription id is not a UUID"},
		{name: "empty url", mutate: func(a *Accepted) { a.URL = "  " }, want: "accepted delivery url is required"},
		{name: "non-object headers", mutate: func(a *Accepted) { a.Headers = `["content-type"]` }, want: "accepted delivery headers are not a JSON object"},
		{name: "invalid headers", mutate: func(a *Accepted) { a.Headers = `{` }, want: "accepted delivery headers are not a JSON object"},
		{name: "zero time", mutate: func(a *Accepted) { a.NextAttemptAt = time.Time{} }, want: "accepted delivery next attempt time is required"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			accepted := validAccepted()
			tc.mutate(&accepted)
			batch := &pgx.Batch{}
			err := AppendAccepted(batch, accepted)
			if err == nil {
				t.Fatal("expected rejection, got nil")
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err, tc.want)
			}
			if batch.Len() != 0 {
				t.Fatalf("rejected snapshot queued %d statements", batch.Len())
			}
		})
	}
}
