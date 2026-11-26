// Package subscription implements endpoint_subscriptions routing rules:
// exact event-type matching and the row lifecycle used by management routes.
package subscription

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Errors returned by the repository.
var (
	ErrNotFound      = errors.New("subscription not found")
	ErrAlreadyActive = errors.New("subscription already active")
)

// MaxEventTypeLength is the approved routing-key bound.
const MaxEventTypeLength = 160

// ValidateEventType accepts only exact lowercase routing keys. No wildcard,
// glob or prefix syntax is representable.
func ValidateEventType(eventType string) bool {
	if len(eventType) == 0 || len(eventType) > MaxEventTypeLength {
		return false
	}
	for i := 0; i < len(eventType); i++ {
		b := eventType[i]
		if (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') || b == '_' || b == '.' || b == '-' {
			continue
		}
		return false
	}
	return true
}

// Record is one endpoint_subscriptions row.
type Record struct {
	ID             string
	OrganizationID string
	EndpointID     string
	EventType      string
	IsActive       bool
	CreatedAt      time.Time
}

// Representation is the public subscription document.
type Representation struct {
	ID         string `json:"id"`
	EndpointID string `json:"endpoint_id"`
	EventType  string `json:"event_type"`
	IsActive   bool   `json:"is_active"`
}

// Represent builds the public document.
func (r Record) Represent() Representation {
	return Representation{ID: r.ID, EndpointID: r.EndpointID, EventType: r.EventType, IsActive: r.IsActive}
}

// Querier is satisfied by pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const columns = `id::text, organization_id::text, endpoint_id::text, event_type, is_active, created_at`

// List returns every routing rule of one endpoint, active or not.
func List(ctx context.Context, q Querier, organizationID, endpointID string) ([]Record, error) {
	const query = `SELECT ` + columns + `
		FROM endpoint_subscriptions
		WHERE organization_id = $1::uuid AND endpoint_id = $2::uuid
		ORDER BY event_type`
	rows, err := q.Query(ctx, query, organizationID, endpointID)
	if err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0, 8)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan subscription: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list subscriptions: %w", err)
	}
	return records, nil
}

// CreateOrReactivate inserts an active rule or reactivates an inactive one.
// The conflict path takes the row lock. created is false on reactivation.
func CreateOrReactivate(ctx context.Context, q Querier, record Record) (Record, bool, error) {
	const statement = `INSERT INTO endpoint_subscriptions
		(id, organization_id, endpoint_id, event_type, is_active)
		VALUES ($1::uuid, $2::uuid, $3::uuid, $4, true)
		ON CONFLICT (endpoint_id, event_type) DO UPDATE
		SET is_active = true
		WHERE endpoint_subscriptions.is_active = false
		RETURNING ` + columns + `, (xmax = 0) AS inserted`
	result, created, err := scanRecordWithInserted(q.QueryRow(ctx, statement,
		record.ID, record.OrganizationID, record.EndpointID, record.EventType,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, ErrAlreadyActive
	}
	if err != nil {
		return Record{}, false, fmt.Errorf("create subscription: %w", err)
	}
	return result, created, nil
}

// Lock takes the approved subscription-row lock.
func Lock(ctx context.Context, q Querier, organizationID, endpointID, subscriptionID string) (Record, error) {
	const query = `SELECT ` + columns + `
		FROM endpoint_subscriptions
		WHERE organization_id = $1::uuid AND endpoint_id = $2::uuid AND id = $3::uuid
		FOR NO KEY UPDATE`
	record, err := scanRecord(q.QueryRow(ctx, query, organizationID, endpointID, subscriptionID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("lock subscription: %w", err)
	}
	return record, nil
}

// Deactivate turns the rule off. changed is false when it was already inactive.
func Deactivate(ctx context.Context, q Querier, organizationID, endpointID, subscriptionID string) (Record, bool, error) {
	const statement = `UPDATE endpoint_subscriptions
		SET is_active = false
		WHERE organization_id = $1::uuid AND endpoint_id = $2::uuid AND id = $3::uuid AND is_active
		RETURNING ` + columns
	record, err := scanRecord(q.QueryRow(ctx, statement, organizationID, endpointID, subscriptionID))
	if err == nil {
		return record, true, nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return Record{}, false, fmt.Errorf("deactivate subscription: %w", err)
	}
	existing, lockErr := Lock(ctx, q, organizationID, endpointID, subscriptionID)
	if lockErr != nil {
		return Record{}, false, lockErr
	}
	return existing, false, nil
}

func scanRecord(row pgx.Row) (Record, error) {
	var record Record
	err := row.Scan(&record.ID, &record.OrganizationID, &record.EndpointID,
		&record.EventType, &record.IsActive, &record.CreatedAt)
	if err != nil {
		return Record{}, err
	}
	return record, nil
}

func scanRecordWithInserted(row pgx.Row) (Record, bool, error) {
	var record Record
	var inserted pgtype.Bool
	err := row.Scan(&record.ID, &record.OrganizationID, &record.EndpointID,
		&record.EventType, &record.IsActive, &record.CreatedAt, &inserted)
	if err != nil {
		return Record{}, false, err
	}
	return record, inserted.Bool, nil
}
