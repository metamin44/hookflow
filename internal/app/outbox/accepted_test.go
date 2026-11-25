package outbox

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/platform/messaging"
)

func TestAppendAcceptedQueuesInitialDispatch(t *testing.T) {
	availableAt := time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)
	batch := &pgx.Batch{}
	err := AppendAccepted(batch, Accepted{
		ID:             "01920000-0000-7000-8000-000000000011",
		OrganizationID: "01920000-0000-7000-8000-000000000012",
		DeliveryID:     "01920000-0000-7000-8000-000000000013",
		AvailableAt:    availableAt,
	})
	if err != nil {
		t.Fatalf("AppendAccepted: %v", err)
	}
	if batch.Len() != 1 {
		t.Fatalf("queued %d statements, want 1", batch.Len())
	}
	query := batch.QueuedQueries[0]
	if query.SQL == "" {
		t.Fatal("empty SQL")
	}
	args := query.Arguments
	if len(args) != 7 {
		t.Fatalf("queued %d arguments, want 7", len(args))
	}
	if args[0] != "01920000-0000-7000-8000-000000000011" {
		t.Errorf("outbox id argument = %v", args[0])
	}
	if args[3] != messaging.DispatchRoutingKey {
		t.Errorf("topic argument = %v, want %q", args[3], messaging.DispatchRoutingKey)
	}
	payload, ok := args[4].(string)
	if !ok {
		t.Fatalf("payload argument type = %T, want string", args[4])
	}
	var decoded map[string]any
	if err := json.Unmarshal([]byte(payload), &decoded); err != nil {
		t.Fatalf("payload is not JSON: %v", err)
	}
	if _, ok := decoded["delivery_id"]; !ok {
		t.Errorf("payload lacks identifiers: %s", payload)
	}
	headers, ok := args[5].(string)
	if !ok {
		t.Fatalf("headers argument type = %T, want string", args[5])
	}
	var headerObject map[string]string
	if err := json.Unmarshal([]byte(headers), &headerObject); err != nil {
		t.Fatalf("headers are not a JSON object: %v", err)
	}
	if headerObject["x-hookflow-schema-version"] != "1" {
		t.Errorf("schema version header = %q, want \"1\"", headerObject["x-hookflow-schema-version"])
	}
	if got, ok := args[6].(time.Time); !ok || !got.Equal(availableAt) {
		t.Errorf("available_at argument = %v, want %v", args[6], availableAt)
	}
}

func TestAppendAcceptedRejectsZeroAvailability(t *testing.T) {
	batch := &pgx.Batch{}
	err := AppendAccepted(batch, Accepted{
		ID:             "01920000-0000-7000-8000-000000000011",
		OrganizationID: "01920000-0000-7000-8000-000000000012",
		DeliveryID:     "01920000-0000-7000-8000-000000000013",
	})
	if err == nil {
		t.Fatal("expected rejection, got nil")
	}
	if batch.Len() != 0 {
		t.Fatalf("rejected row queued %d statements", batch.Len())
	}
}
