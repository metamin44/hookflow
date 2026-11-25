package outbox

import (
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/platform/messaging"
)

// Accepted is one initial dispatch row written at ingestion. The payload is
// identifiers-only; the Outbox row carries no delivery data or secret.
type Accepted struct {
	ID             string
	OrganizationID string
	DeliveryID     string
	AvailableAt    time.Time
}

// initialHeaders mirrors the schema-version header every other dispatch writer
// stores; the Publisher does not require it.
func initialHeaders() string {
	return fmt.Sprintf(`{"x-hookflow-schema-version":"%d"}`, messaging.SchemaVersion)
}

// insertAcceptedSQL writes the initial pending generation with an empty lease,
// zero attempts and the same scheduling instant as the Delivery.
const insertAcceptedSQL = `
INSERT INTO outbox_messages
    (id, organization_id, delivery_id, sequence, kind, topic, payload, headers,
     status, attempts, available_at, created_at, updated_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, 1, 'delivery.dispatch', $4, $5::jsonb, $6::jsonb,
        'pending', 0, $7::timestamptz, now(), now())`

// AppendAccepted queues the initial delivery.dispatch row for one new Delivery.
func AppendAccepted(batch *pgx.Batch, accepted Accepted) error {
	if accepted.AvailableAt.IsZero() {
		return errors.New("initial outbox available time is required")
	}
	payload, err := messaging.BuildOutboxPayload(accepted.ID, accepted.OrganizationID, accepted.DeliveryID, 1)
	if err != nil {
		return fmt.Errorf("build initial outbox payload: %w", err)
	}
	batch.Queue(insertAcceptedSQL,
		accepted.ID, accepted.OrganizationID, accepted.DeliveryID,
		messaging.DispatchRoutingKey, payload, initialHeaders(), accepted.AvailableAt,
	)
	return nil
}
