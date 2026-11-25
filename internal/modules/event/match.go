package event

import (
	"context"
	"fmt"
)

// matchDestinationsSQL is the approved exact-match fan-out query (U16): one
// organization, one exact event_type, live subscriptions and live active
// endpoints. No wildcard, prefix or cross-organization match is representable.
const matchDestinationsSQL = `
SELECT s.id::text, e.id::text, e.url, e.http_headers::text, e.secret_encrypted
  FROM endpoint_subscriptions s
  JOIN endpoints e ON e.id = s.endpoint_id
 WHERE s.organization_id = $1::uuid
   AND s.event_type = $2
   AND s.is_active
   AND e.deleted_at IS NULL
   AND e.status = 'active'
 ORDER BY s.id`

// MatchDestinations returns the immutable snapshot inputs for every live
// destination of one event type. Zero rows is a valid accepted outcome (U36).
func MatchDestinations(ctx context.Context, q Querier, organizationID, eventType string) ([]Destination, error) {
	rows, err := q.Query(ctx, matchDestinationsSQL, organizationID, eventType)
	if err != nil {
		return nil, fmt.Errorf("match event destinations: %w", err)
	}
	defer rows.Close()

	destinations := make([]Destination, 0, 4)
	for rows.Next() {
		var destination Destination
		if err := rows.Scan(
			&destination.SubscriptionID, &destination.EndpointID,
			&destination.URL, &destination.Headers, &destination.SecretEncrypted,
		); err != nil {
			return nil, fmt.Errorf("scan matched destination: %w", err)
		}
		destinations = append(destinations, destination)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read matched destinations: %w", err)
	}
	return destinations, nil
}
