package delivery

import (
	"fmt"

	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/messaging"
)

// BuildRetryMessage creates the approved identifiers-only retry outbox row.
func BuildRetryMessage(input RetryMessageInput) (Message, error) {
	messageID, err := id.NewV7()
	if err != nil {
		return Message{}, fmt.Errorf("generate retry outbox id: %w", err)
	}
	payload, err := messaging.BuildOutboxPayload(messageID, input.OrganizationID, input.DeliveryID, input.Sequence)
	if err != nil {
		return Message{}, err
	}
	return Message{
		ID: messageID, Sequence: input.Sequence, Kind: "delivery.retry",
		Topic: messaging.DispatchRoutingKey, Payload: payload,
		Headers: fmt.Sprintf(`{"x-hookflow-schema-version":"%d"}`, messaging.SchemaVersion),
	}, nil
}
