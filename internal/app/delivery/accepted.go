package delivery

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Accepted is the immutable snapshot written for one matched destination at
// ingestion. URL, headers and ciphertext are copied from the endpoint row; the
// secret is never decrypted, re-encrypted or rewrapped here.
type Accepted struct {
	ID              string
	OrganizationID  string
	EventID         string
	EndpointID      string
	SubscriptionID  string
	URL             string
	Headers         string
	SecretEncrypted string
	NextAttemptAt   time.Time
}

func (a Accepted) validate() error {
	for _, identifier := range []struct {
		name  string
		value string
	}{
		{name: "id", value: a.ID},
		{name: "organization id", value: a.OrganizationID},
		{name: "event id", value: a.EventID},
		{name: "endpoint id", value: a.EndpointID},
		{name: "subscription id", value: a.SubscriptionID},
	} {
		if !validUUID(identifier.value) {
			return errors.New("accepted delivery " + identifier.name + " is not a UUID")
		}
	}
	if strings.TrimSpace(a.URL) == "" {
		return errors.New("accepted delivery url is required")
	}
	if !json.Valid([]byte(a.Headers)) || !strings.HasPrefix(strings.TrimSpace(a.Headers), "{") {
		return errors.New("accepted delivery headers are not a JSON object")
	}
	if a.NextAttemptAt.IsZero() {
		return errors.New("accepted delivery next attempt time is required")
	}
	return nil
}

// insertAcceptedSQL writes the initial pending generation. The unique
// (event_id, endpoint_id) constraint plus DO NOTHING makes an unexpected
// duplicate visible as a missing row, which the caller treats as partial
// fan-out and rolls back.
const insertAcceptedSQL = `
INSERT INTO deliveries
    (id, organization_id, event_id, endpoint_id, endpoint_subscription_id,
     url, http_headers, secret_encrypted, state, dispatch_seq, next_attempt_at,
     created_at, updated_at)
VALUES ($1::uuid, $2::uuid, $3::uuid, $4::uuid, $5::uuid,
        $6, $7::jsonb, $8, 'pending', 1, $9::timestamptz, now(), now())
ON CONFLICT (event_id, endpoint_id) DO NOTHING`

// AppendAccepted queues one pending Delivery snapshot with dispatch sequence 1.
// No attempt row, attempt counter or lease is written at ingestion.
func AppendAccepted(batch *pgx.Batch, accepted Accepted) error {
	if err := accepted.validate(); err != nil {
		return err
	}
	batch.Queue(insertAcceptedSQL,
		accepted.ID, accepted.OrganizationID, accepted.EventID, accepted.EndpointID,
		accepted.SubscriptionID, accepted.URL, accepted.Headers, accepted.SecretEncrypted,
		accepted.NextAttemptAt,
	)
	return nil
}
