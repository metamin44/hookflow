// Package orgsubscription persists the organization-level subscription history
// defined by database-schema.md §4.11. It stores periods; it never prices,
// bills, provisions entitlements or talks to a payment provider.
package orgsubscription

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Plan values accepted by chk_org_subs_plan.
const (
	PlanFree       = "free"
	PlanPro        = "pro"
	PlanEnterprise = "enterprise"
)

// Status values accepted by chk_org_subs_status. Canceled is terminal and is
// reached only through cancellation, never through a create or replace.
const (
	StatusTrialing   = "trialing"
	StatusActive     = "active"
	StatusPastDue    = "past_due"
	StatusCanceled   = "canceled"
	StatusIncomplete = "incomplete"
)

// ErrNotFound reports that the organization has no open subscription.
var ErrNotFound = errors.New("organization subscription not found")

// ErrOpenExists reports a concurrent open subscription. The partial unique
// index uq_org_subs_open is the final authority.
var ErrOpenExists = errors.New("organization already has an open subscription")

// Record is one stored subscription period.
type Record struct {
	ID                 string
	OrganizationID     string
	Plan               string
	Status             string
	CurrentPeriodStart *time.Time
	CurrentPeriodEnd   *time.Time
	TrialEndsAt        *time.Time
	CanceledAt         *time.Time
	CreatedAt          time.Time
	UpdatedAt          time.Time
}

// Representation is the API document and the input to the Contract A-style
// strong validator. Timestamps are normalized to UTC.
type Representation struct {
	ID                 string     `json:"id"`
	Plan               string     `json:"plan"`
	Status             string     `json:"status"`
	CurrentPeriodStart *time.Time `json:"current_period_start"`
	CurrentPeriodEnd   *time.Time `json:"current_period_end"`
	TrialEndsAt        *time.Time `json:"trial_ends_at"`
	CanceledAt         *time.Time `json:"canceled_at"`
	CreatedAt          time.Time  `json:"created_at"`
	UpdatedAt          time.Time  `json:"updated_at"`
}

// Represent builds the public document from a stored row.
func (r Record) Represent() Representation {
	return Representation{
		ID:                 r.ID,
		Plan:               r.Plan,
		Status:             r.Status,
		CurrentPeriodStart: utcOptional(r.CurrentPeriodStart),
		CurrentPeriodEnd:   utcOptional(r.CurrentPeriodEnd),
		TrialEndsAt:        utcOptional(r.TrialEndsAt),
		CanceledAt:         utcOptional(r.CanceledAt),
		CreatedAt:          r.CreatedAt.UTC(),
		UpdatedAt:          r.UpdatedAt.UTC(),
	}
}

// IsPlan reports whether value is a schema-approved plan.
func IsPlan(value string) bool {
	switch value {
	case PlanFree, PlanPro, PlanEnterprise:
		return true
	default:
		return false
	}
}

// IsOpenStatus reports whether value is a non-terminal lifecycle status.
func IsOpenStatus(value string) bool {
	switch value {
	case StatusTrialing, StatusActive, StatusPastDue, StatusIncomplete:
		return true
	default:
		return false
	}
}

// Querier is satisfied by both pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const columns = `id::text, organization_id::text, plan, status,
	current_period_start, current_period_end, trial_ends_at, canceled_at,
	created_at, updated_at`

// Current returns the single open subscription or ErrNotFound.
func Current(ctx context.Context, q Querier, organizationID string) (Record, error) {
	return scanOne(q.QueryRow(ctx,
		`SELECT `+columns+` FROM organization_subscriptions
		 WHERE organization_id = $1 AND status <> 'canceled'`, organizationID))
}

// List returns every period newest first, including canceled history.
func List(ctx context.Context, q Querier, organizationID string) ([]Record, error) {
	rows, err := q.Query(ctx,
		`SELECT `+columns+` FROM organization_subscriptions
		 WHERE organization_id = $1
		 ORDER BY created_at DESC, id DESC`, organizationID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	records := make([]Record, 0, 4)
	for rows.Next() {
		record, err := scan(rows)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

// LockCurrent locks the open subscription row for the caller's transaction.
func LockCurrent(ctx context.Context, tx pgx.Tx, organizationID string) (Record, error) {
	return scanOne(tx.QueryRow(ctx,
		`SELECT `+columns+` FROM organization_subscriptions
		 WHERE organization_id = $1 AND status <> 'canceled'
		 FOR NO KEY UPDATE`, organizationID))
}

// Insert appends one open period.
func Insert(ctx context.Context, q Querier, record Record) (Record, error) {
	return scanOne(q.QueryRow(ctx,
		`INSERT INTO organization_subscriptions
			(id, organization_id, plan, status, current_period_start,
			 current_period_end, trial_ends_at)
		 VALUES ($1, $2, $3, $4, $5, $6, $7)
		 RETURNING `+columns,
		record.ID, record.OrganizationID, record.Plan, record.Status,
		record.CurrentPeriodStart, record.CurrentPeriodEnd, record.TrialEndsAt))
}

// Update rewrites the mutable fields of one open period without appending a
// new row. It is used for same-plan lifecycle and period changes.
func Update(ctx context.Context, q Querier, record Record) (Record, error) {
	return scanOne(q.QueryRow(ctx,
		`UPDATE organization_subscriptions
		 SET plan = $3, status = $4, current_period_start = $5,
			 current_period_end = $6, trial_ends_at = $7, canceled_at = NULL,
			 updated_at = now()
		 WHERE organization_id = $1 AND id = $2
		 RETURNING `+columns,
		record.OrganizationID, record.ID, record.Plan, record.Status,
		record.CurrentPeriodStart, record.CurrentPeriodEnd, record.TrialEndsAt))
}

// Cancel closes one open period. It leaves canceled history intact.
func Cancel(ctx context.Context, q Querier, organizationID, subscriptionID string, canceledAt time.Time) (Record, error) {
	return scanOne(q.QueryRow(ctx,
		`UPDATE organization_subscriptions
		 SET status = 'canceled', canceled_at = $3, updated_at = now()
		 WHERE organization_id = $1 AND id = $2 AND status <> 'canceled'
		 RETURNING `+columns,
		organizationID, subscriptionID, canceledAt))
}

// IsOpenConflict reports a violation of uq_org_subs_open.
func IsOpenConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) &&
		pgErr.Code == "23505" &&
		pgErr.ConstraintName == "uq_org_subs_open"
}

func scanOne(row pgx.Row) (Record, error) {
	record, err := scan(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	return record, err
}

type scanner interface {
	Scan(dest ...any) error
}

func scan(row scanner) (Record, error) {
	var record Record
	err := row.Scan(
		&record.ID,
		&record.OrganizationID,
		&record.Plan,
		&record.Status,
		&record.CurrentPeriodStart,
		&record.CurrentPeriodEnd,
		&record.TrialEndsAt,
		&record.CanceledAt,
		&record.CreatedAt,
		&record.UpdatedAt,
	)
	return record, err
}

func utcOptional(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	normalized := value.UTC()
	return &normalized
}
