package history

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/platform/diagnostics"
)

// ErrNotFound reports a missing or foreign event or delivery. Callers render
// the same not-found response for both.
var ErrNotFound = errors.New("history resource not found")

// Querier is satisfied by pgxpool.Pool and pgx.Tx. History never begins its own
// transaction.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const eventColumns = `e.id::text, e.type, e.received_at, e.created_at,
	COALESCE(s.total, 0)::int, COALESCE(s.initial_total, 0)::int, COALESCE(s.retry_total, 0)::int,
	COALESCE(s.pending, 0)::int, COALESCE(s.retrying, 0)::int,
	COALESCE(s.succeeded, 0)::int, COALESCE(s.failed, 0)::int, COALESCE(s.expired, 0)::int`

const eventSummaryJoin = `
LEFT JOIN LATERAL (
	SELECT count(*) AS total,
		count(*) FILTER (WHERE d.generation = 1) AS initial_total,
		count(*) FILTER (WHERE d.generation > 1) AS retry_total,
		count(*) FILTER (WHERE d.state = 'pending') AS pending,
		count(*) FILTER (WHERE d.state = 'retrying') AS retrying,
		count(*) FILTER (WHERE d.state = 'succeeded') AS succeeded,
		count(*) FILTER (WHERE d.state = 'failed') AS failed,
		count(*) FILTER (WHERE d.state = 'expired') AS expired
	  FROM deliveries d
	 WHERE d.organization_id = e.organization_id AND d.event_id = e.id
) s ON true`

const eventFilterSQL = `
	 AND ($2::text IS NULL OR e.type = $2::text)
	 AND ($3::uuid IS NULL OR EXISTS (
		SELECT 1 FROM deliveries d
		 WHERE d.organization_id = e.organization_id AND d.event_id = e.id
		   AND d.endpoint_id = $3::uuid))
	 AND ($4::timestamptz IS NULL OR e.received_at >= $4::timestamptz)
	 AND ($5::timestamptz IS NULL OR e.received_at < $5::timestamptz)
	 AND ($6::timestamptz IS NULL OR (e.received_at, e.id) < ($6::timestamptz, $7::uuid))`

// ListEvents returns one newest-first page and whether more rows remain. The
// filter and the continuation position are always bounded to organizationID.
func ListEvents(ctx context.Context, q Querier, organizationID string, filter EventFilter, after *Position, limit int) ([]EventSummary, bool, error) {
	query := `SELECT ` + eventColumns + `
		FROM events e` + eventSummaryJoin + `
		WHERE e.organization_id = $1::uuid` + eventFilterSQL + `
		ORDER BY e.received_at DESC, e.id DESC
		LIMIT $8`
	rows, err := q.Query(ctx, query,
		organizationID, nullableString(filter.Type), nullableString(filter.EndpointID),
		filter.ReceivedFrom, filter.ReceivedTo, positionTime(after), positionID(after), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list events: %w", err)
	}
	defer rows.Close()
	records := make([]EventSummary, 0, limit+1)
	for rows.Next() {
		var record EventSummary
		if err := rows.Scan(
			&record.ID, &record.Type, &record.ReceivedAt, &record.CreatedAt,
			&record.DeliverySummary.Total, &record.DeliverySummary.InitialTotal,
			&record.DeliverySummary.RetryTotal, &record.DeliverySummary.Pending,
			&record.DeliverySummary.Retrying, &record.DeliverySummary.Succeeded,
			&record.DeliverySummary.Failed, &record.DeliverySummary.Expired,
		); err != nil {
			return nil, false, fmt.Errorf("scan event: %w", err)
		}
		record.ReceivedAt = record.ReceivedAt.UTC()
		record.CreatedAt = record.CreatedAt.UTC()
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list events: %w", err)
	}
	records, hasMore := trimPage(records, limit)
	return records, hasMore, nil
}

// GetEvent returns one organization-scoped event with its stored payload and
// delivery summary.
func GetEvent(ctx context.Context, q Querier, organizationID, eventID string) (EventDetail, error) {
	query := `SELECT ` + eventColumns + `, e.payload, e.destination_count
		FROM events e` + eventSummaryJoin + `
		WHERE e.organization_id = $1::uuid AND e.id = $2::uuid`
	var detail EventDetail
	var payload []byte
	err := q.QueryRow(ctx, query, organizationID, eventID).Scan(
		&detail.ID, &detail.Type, &detail.ReceivedAt, &detail.CreatedAt,
		&detail.DeliverySummary.Total, &detail.DeliverySummary.InitialTotal,
		&detail.DeliverySummary.RetryTotal, &detail.DeliverySummary.Pending,
		&detail.DeliverySummary.Retrying, &detail.DeliverySummary.Succeeded,
		&detail.DeliverySummary.Failed, &detail.DeliverySummary.Expired,
		&payload, &detail.DestinationCount,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return EventDetail{}, ErrNotFound
	}
	if err != nil {
		return EventDetail{}, fmt.Errorf("read event: %w", err)
	}
	detail.Payload = json.RawMessage(payload)
	detail.ReceivedAt = detail.ReceivedAt.UTC()
	detail.CreatedAt = detail.CreatedAt.UTC()
	return detail, nil
}

const deliveryColumns = `d.id::text, d.event_id::text, d.endpoint_id::text, d.state,
	d.attempt_count, (d.dispatch_lease_expires_at IS NOT NULL AND d.dispatch_lease_expires_at > now()),
	d.generation, d.parent_delivery_id::text,
	d.created_at, d.next_attempt_at, d.last_attempt_at, d.completed_at,
	d.last_response_status, d.dispatch_lease_expires_at, d.last_error`

const deliveryFilterSQL = `
	 AND ($2::uuid IS NULL OR d.endpoint_id = $2::uuid)
	 AND ($3::text IS NULL OR d.state = $3::text)
	 AND ($4::timestamptz IS NULL OR d.created_at >= $4::timestamptz)
	 AND ($5::timestamptz IS NULL OR d.created_at < $5::timestamptz)
	 AND ($6::timestamptz IS NULL OR (d.created_at, d.id) < ($6::timestamptz, $7::uuid))`

// ListDeliveries returns one newest-first page and whether more rows remain.
func ListDeliveries(ctx context.Context, q Querier, organizationID string, filter DeliveryFilter, after *Position, limit int) ([]DeliverySummary, bool, error) {
	query := `SELECT ` + deliveryColumns + `
		FROM deliveries d
		WHERE d.organization_id = $1::uuid` + deliveryFilterSQL + `
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $8`
	rows, err := q.Query(ctx, query,
		organizationID, nullableString(filter.EndpointID), nullableString(filter.State),
		filter.CreatedFrom, filter.CreatedTo, positionTime(after), positionID(after), limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list deliveries: %w", err)
	}
	defer rows.Close()
	records := make([]DeliverySummary, 0, limit+1)
	for rows.Next() {
		record, err := scanDelivery(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list deliveries: %w", err)
	}
	records, hasMore := trimPage(records, limit)
	return records, hasMore, nil
}

// GetDelivery returns one organization-scoped delivery with its event type.
func GetDelivery(ctx context.Context, q Querier, organizationID, deliveryID string) (DeliveryDetail, error) {
	query := `SELECT ` + deliveryColumns + `, d.updated_at, e.type
		FROM deliveries d
		JOIN events e ON e.organization_id = d.organization_id AND e.id = d.event_id
		WHERE d.organization_id = $1::uuid AND d.id = $2::uuid`
	var detail DeliveryDetail
	var lastError *string
	err := q.QueryRow(ctx, query, organizationID, deliveryID).Scan(
		&detail.ID, &detail.EventID, &detail.EndpointID, &detail.State,
		&detail.AttemptCount, &detail.InProgress, &detail.Generation, &detail.ParentDeliveryID,
		&detail.CreatedAt, &detail.NextAttemptAt, &detail.LastAttemptAt, &detail.CompletedAt,
		&detail.LastResponseStatus, &detail.LeaseExpiresAt, &lastError,
		&detail.UpdatedAt, &detail.EventType,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return DeliveryDetail{}, ErrNotFound
	}
	if err != nil {
		return DeliveryDetail{}, fmt.Errorf("read delivery: %w", err)
	}
	if lastError != nil {
		detail.Diagnostic = diagnostics.Bounded(*lastError, DiagnosticLimit)
	}
	finishDelivery(&detail.DeliverySummary, &detail.UpdatedAt)
	return detail, nil
}

// ListEventDeliveries returns the bounded, newest-first delivery summaries of
// one event. The second result reports whether the list was truncated.
func ListEventDeliveries(ctx context.Context, q Querier, organizationID, eventID string) ([]DeliverySummary, bool, error) {
	query := `SELECT ` + deliveryColumns + `
		FROM deliveries d
		WHERE d.organization_id = $1::uuid AND d.event_id = $2::uuid
		ORDER BY d.created_at DESC, d.id DESC
		LIMIT $3`
	rows, err := q.Query(ctx, query, organizationID, eventID, MaxEventDeliveries+1)
	if err != nil {
		return nil, false, fmt.Errorf("list event deliveries: %w", err)
	}
	defer rows.Close()
	records := make([]DeliverySummary, 0, MaxEventDeliveries+1)
	for rows.Next() {
		record, err := scanDelivery(rows)
		if err != nil {
			return nil, false, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list event deliveries: %w", err)
	}
	records, hasMore := trimPage(records, MaxEventDeliveries)
	return records, hasMore, nil
}

// DeliveryExists reports whether the delivery belongs to the organization.
func DeliveryExists(ctx context.Context, q Querier, organizationID, deliveryID string) (bool, error) {
	var exists bool
	err := q.QueryRow(ctx,
		`SELECT EXISTS (SELECT 1 FROM deliveries d
		 WHERE d.organization_id = $1::uuid AND d.id = $2::uuid)`,
		organizationID, deliveryID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check delivery: %w", err)
	}
	return exists, nil
}

// ListAttempts returns one oldest-first attempt page and whether more remain.
// The position is the last attempt_number already returned; zero starts at the
// first attempt.
func ListAttempts(ctx context.Context, q Querier, organizationID, deliveryID string, afterNumber, limit int) ([]Attempt, bool, error) {
	rows, err := q.Query(ctx,
		`SELECT attempt_number, outcome, response_status, error_class, error_message,
			duration_ms, started_at, finished_at
		 FROM delivery_attempts
		 WHERE organization_id = $1::uuid AND delivery_id = $2::uuid
		   AND ($3::int = 0 OR attempt_number > $3::int)
		 ORDER BY attempt_number ASC
		 LIMIT $4`,
		organizationID, deliveryID, afterNumber, limit+1)
	if err != nil {
		return nil, false, fmt.Errorf("list delivery attempts: %w", err)
	}
	defer rows.Close()
	records := make([]Attempt, 0, limit+1)
	for rows.Next() {
		var record Attempt
		var message *string
		if err := rows.Scan(
			&record.AttemptNumber, &record.Outcome, &record.ResponseStatus, &record.ErrorClass,
			&message, &record.DurationMS, &record.StartedAt, &record.FinishedAt,
		); err != nil {
			return nil, false, fmt.Errorf("scan delivery attempt: %w", err)
		}
		record.StartedAt = record.StartedAt.UTC()
		record.FinishedAt = record.FinishedAt.UTC()
		if message != nil {
			record.ErrorMessage = diagnostics.Bounded(*message, DiagnosticLimit)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, false, fmt.Errorf("list delivery attempts: %w", err)
	}
	records, hasMore := trimPage(records, limit)
	return records, hasMore, nil
}

func scanDelivery(row scanner) (DeliverySummary, error) {
	var record DeliverySummary
	var lastError *string
	err := row.Scan(
		&record.ID, &record.EventID, &record.EndpointID, &record.State,
		&record.AttemptCount, &record.InProgress, &record.Generation, &record.ParentDeliveryID,
		&record.CreatedAt, &record.NextAttemptAt, &record.LastAttemptAt, &record.CompletedAt,
		&record.LastResponseStatus, &record.LeaseExpiresAt, &lastError,
	)
	if err != nil {
		return DeliverySummary{}, fmt.Errorf("scan delivery: %w", err)
	}
	if lastError != nil {
		record.Diagnostic = diagnostics.Bounded(*lastError, DiagnosticLimit)
	}
	finishDelivery(&record, nil)
	return record, nil
}

// finishDelivery normalizes timestamps to UTC. InProgress is projected in SQL
// from the lease expiry so an expired lease is never reported as in progress,
// and the raw lease expiry is retained for operator context.
func finishDelivery(record *DeliverySummary, updatedAt *time.Time) {
	record.CreatedAt = record.CreatedAt.UTC()
	record.NextAttemptAt = utcOptional(record.NextAttemptAt)
	record.LastAttemptAt = utcOptional(record.LastAttemptAt)
	record.CompletedAt = utcOptional(record.CompletedAt)
	record.LeaseExpiresAt = utcOptional(record.LeaseExpiresAt)
	if updatedAt != nil {
		*updatedAt = updatedAt.UTC()
	}
}

type scanner interface {
	Scan(dest ...any) error
}

func trimPage[T any](records []T, limit int) ([]T, bool) {
	if len(records) > limit {
		return records[:limit], true
	}
	return records, false
}

func nullableString(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func positionTime(position *Position) any {
	if position == nil {
		return nil
	}
	return position.Time.UTC()
}

func positionID(position *Position) any {
	if position == nil {
		return nil
	}
	return position.ID
}

func utcOptional(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
