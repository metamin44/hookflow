// Package audit writes the append-only administrative trail.
//
// Audit rows always commit inside the transaction of the mutation they record.
// Metadata is restricted to non-sensitive values: field names, event types and
// status labels. URLs, header values, secrets, verifiers and raw tokens must
// never be placed in this package's input.
package audit

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Actor types accepted by the audit_logs constraint.
const (
	ActorUser    = "user"
	ActorAPIKey  = "api_key"
	ActorSystem  = "system"
	ActorService = "service"
)

// Target types used by the management and billing APIs.
const (
	TargetEndpoint                 = "endpoint"
	TargetSubscription             = "endpoint_subscription"
	TargetOrganizationSubscription = "organization_subscription"
	TargetAPIKey                   = "api_key"
)

// Record is one audit_logs row. ActorID and TargetID are optional.
type Record struct {
	ID             string
	OrganizationID string
	ActorType      string
	ActorID        *string
	Action         string
	TargetType     string
	TargetID       *string
	IP             *string
	UserAgent      *string
	Metadata       map[string]any
}

// Querier is satisfied by pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Insert writes one audit row in the caller's transaction.
func Insert(ctx context.Context, q Querier, record Record) error {
	metadata := record.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	encoded, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("encode audit metadata: %w", err)
	}
	const statement = `INSERT INTO audit_logs
		(id, organization_id, actor_type, actor_id, action, target_type, target_id, ip, user_agent, metadata)
		VALUES ($1::uuid, $2::uuid, $3, $4::uuid, $5, $6, $7::uuid, $8::inet, $9, $10::jsonb)
		RETURNING id`
	var inserted string
	if err := q.QueryRow(ctx, statement,
		record.ID, record.OrganizationID, record.ActorType, record.ActorID,
		record.Action, record.TargetType, record.TargetID, record.IP,
		record.UserAgent, encoded,
	).Scan(&inserted); err != nil {
		return fmt.Errorf("insert audit row: %w", err)
	}
	return nil
}
