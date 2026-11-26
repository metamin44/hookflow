package recovery

import (
	"fmt"

	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

// BuildMessage creates a fresh identifiers-only Outbox row for repair or stale
// redispatch. The caller supplies the already-revalidated generation.
func BuildMessage(delivery Delivery) (Message, error) {
	messageID, err := id.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate recovery outbox id: %w", err)
	}
	payload, err := messaging.BuildOutboxPayload(messageID, delivery.OrganizationID, delivery.DeliveryID, delivery.DispatchSeq)
	if err != nil {
		return Message{}, err
	}
	kind := "delivery.dispatch"
	if delivery.DispatchSeq > 1 {
		kind = "delivery.retry"
	}
	return Message{
		ID: messageID, Sequence: delivery.DispatchSeq, Kind: kind,
		Topic: messaging.DispatchRoutingKey, Payload: payload,
		Headers: fmt.Sprintf(`{"x-hookflow-schema-version":"%d"}`, messaging.SchemaVersion),
	}, nil
}
