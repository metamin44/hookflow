package management

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/modules/audit"
	"github.com/metamin44/hookflow/internal/modules/organization"
	"github.com/metamin44/hookflow/internal/modules/orgsubscription"
)

// OrganizationSubscriptionInput is a validated full replacement of the open
// subscription. Canceled is not accepted here; cancellation has its own route.
type OrganizationSubscriptionInput struct {
	Plan               string
	Status             string
	CurrentPeriodStart *time.Time
	CurrentPeriodEnd   *time.Time
	TrialEndsAt        *time.Time
}

// OrganizationSubscriptionResult reports the current period, whether it was
// appended, whether anything changed, and the changed field names.
type OrganizationSubscriptionResult struct {
	Record  orgsubscription.Record
	Created bool
	Changed bool
	Fields  []string
}

// GetOrganizationSubscription returns the single open period.
func (s *Service) GetOrganizationSubscription(ctx context.Context, organizationID string) (orgsubscription.Record, error) {
	record, err := orgsubscription.Current(ctx, s.pool, organizationID)
	if errors.Is(err, orgsubscription.ErrNotFound) {
		return orgsubscription.Record{}, notFound()
	}
	if err != nil {
		return orgsubscription.Record{}, err
	}
	return record, nil
}

// ListOrganizationSubscriptions returns every period newest first, including
// canceled history.
func (s *Service) ListOrganizationSubscriptions(ctx context.Context, organizationID string) ([]orgsubscription.Record, error) {
	return orgsubscription.List(ctx, s.pool, organizationID)
}

// PutOrganizationSubscription creates the first open period or fully replaces
// the current one. A plan change appends a period and cancels its predecessor;
// a same-plan change rewrites the open row. An existing period requires
// If-Match, and a missing period rejects any If-Match.
func (s *Service) PutOrganizationSubscription(ctx context.Context, identity Identity, meta RequestMeta, input OrganizationSubscriptionInput, ifMatch string) (OrganizationSubscriptionResult, error) {
	if appErr := validateOrganizationSubscription(input); appErr != nil {
		return OrganizationSubscriptionResult{}, appErr
	}
	var result OrganizationSubscriptionResult
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		if status == organization.StatusSuspended {
			return forbidden("organization_suspended", "suspended organizations cannot change the subscription")
		}
		current, err := orgsubscription.LockCurrent(ctx, tx, identity.OrganizationID)
		if errors.Is(err, orgsubscription.ErrNotFound) {
			if ifMatch != "" {
				return preconditionFailed("")
			}
			return s.createOrganizationSubscription(ctx, tx, identity, meta, input, &result)
		}
		if err != nil {
			return err
		}
		currentTag := orgsubscription.StrongTag(current.Represent())
		if err := evaluateIfMatch(ifMatch, currentTag); err != nil {
			return ifMatchHTTPError(err, currentTag)
		}
		next := orgsubscription.Record{
			ID:                 current.ID,
			OrganizationID:     identity.OrganizationID,
			Plan:               input.Plan,
			Status:             input.Status,
			CurrentPeriodStart: input.CurrentPeriodStart,
			CurrentPeriodEnd:   input.CurrentPeriodEnd,
			TrialEndsAt:        input.TrialEndsAt,
		}
		fields := changedSubscriptionFields(current, next)
		if len(fields) == 0 {
			result = OrganizationSubscriptionResult{Record: current}
			return nil
		}
		if input.Plan != current.Plan {
			return s.replaceOrganizationPlan(ctx, tx, identity, meta, current, next, fields, &result)
		}
		updated, err := orgsubscription.Update(ctx, tx, next)
		if err != nil {
			return mapOrganizationSubscriptionError(err)
		}
		if err := s.writeAudit(ctx, tx, identity, meta,
			"organization_subscription.updated", audit.TargetOrganizationSubscription,
			&updated.ID, map[string]any{
				"fields":          fields,
				"plan":            updated.Plan,
				"status":          updated.Status,
				"previous_status": current.Status,
			}); err != nil {
			return err
		}
		result = OrganizationSubscriptionResult{Record: updated, Changed: true, Fields: fields}
		return nil
	})
	if txErr != nil {
		return OrganizationSubscriptionResult{}, txErr
	}
	return result, nil
}

// CancelOrganizationSubscription closes the open period. Canceled history is
// retained, and a second cancellation is a 404 because no open period remains.
func (s *Service) CancelOrganizationSubscription(ctx context.Context, identity Identity, meta RequestMeta) (orgsubscription.Record, error) {
	var canceled orgsubscription.Record
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		current, err := orgsubscription.LockCurrent(ctx, tx, identity.OrganizationID)
		if errors.Is(err, orgsubscription.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		canceled, err = orgsubscription.Cancel(ctx, tx, identity.OrganizationID, current.ID, time.Now().UTC())
		if errors.Is(err, orgsubscription.ErrNotFound) {
			return notFound()
		}
		if err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, identity, meta,
			"organization_subscription.canceled", audit.TargetOrganizationSubscription,
			&canceled.ID, map[string]any{
				"plan":            current.Plan,
				"previous_status": current.Status,
			})
	})
	if txErr != nil {
		return orgsubscription.Record{}, txErr
	}
	return canceled, nil
}

func (s *Service) createOrganizationSubscription(ctx context.Context, tx pgx.Tx, identity Identity, meta RequestMeta, input OrganizationSubscriptionInput, result *OrganizationSubscriptionResult) error {
	newID, err := s.newID()
	if err != nil {
		return internalError()
	}
	created, err := orgsubscription.Insert(ctx, tx, orgsubscription.Record{
		ID:                 newID,
		OrganizationID:     identity.OrganizationID,
		Plan:               input.Plan,
		Status:             input.Status,
		CurrentPeriodStart: input.CurrentPeriodStart,
		CurrentPeriodEnd:   input.CurrentPeriodEnd,
		TrialEndsAt:        input.TrialEndsAt,
	})
	if err != nil {
		return mapOrganizationSubscriptionError(err)
	}
	if err := s.writeAudit(ctx, tx, identity, meta,
		"organization_subscription.created", audit.TargetOrganizationSubscription,
		&created.ID, map[string]any{
			"plan":   created.Plan,
			"status": created.Status,
		}); err != nil {
		return err
	}
	*result = OrganizationSubscriptionResult{Record: created, Created: true, Changed: true}
	return nil
}

func (s *Service) replaceOrganizationPlan(ctx context.Context, tx pgx.Tx, identity Identity, meta RequestMeta, current, next orgsubscription.Record, fields []string, result *OrganizationSubscriptionResult) error {
	now := time.Now().UTC()
	if _, err := orgsubscription.Cancel(ctx, tx, identity.OrganizationID, current.ID, now); err != nil {
		return mapOrganizationSubscriptionError(err)
	}
	newID, err := s.newID()
	if err != nil {
		return internalError()
	}
	next.ID = newID
	created, err := orgsubscription.Insert(ctx, tx, next)
	if err != nil {
		return mapOrganizationSubscriptionError(err)
	}
	if err := s.writeAudit(ctx, tx, identity, meta,
		"organization_subscription.plan_changed", audit.TargetOrganizationSubscription,
		&created.ID, map[string]any{
			"fields":        fields,
			"plan":          created.Plan,
			"previous_plan": current.Plan,
			"status":        created.Status,
		}); err != nil {
		return err
	}
	*result = OrganizationSubscriptionResult{Record: created, Changed: true, Fields: fields}
	return nil
}

func validateOrganizationSubscription(input OrganizationSubscriptionInput) *HTTPError {
	if !orgsubscription.IsPlan(input.Plan) {
		return fieldRejection("plan", "invalid_plan", "plan must be one of free, pro, enterprise")
	}
	if !orgsubscription.IsOpenStatus(input.Status) {
		return fieldRejection("status", "invalid_status",
			"status must be one of trialing, active, past_due, incomplete; cancel the subscription to close it")
	}
	if input.CurrentPeriodStart == nil && input.CurrentPeriodEnd != nil {
		return fieldRejection("current_period_end", "invalid_period",
			"current_period_end requires current_period_start")
	}
	if input.CurrentPeriodStart != nil && input.CurrentPeriodEnd != nil &&
		!input.CurrentPeriodEnd.After(*input.CurrentPeriodStart) {
		return fieldRejection("current_period_end", "invalid_period",
			"current_period_end must be after current_period_start")
	}
	return nil
}

func changedSubscriptionFields(current, next orgsubscription.Record) []string {
	fields := make([]string, 0, 5)
	if current.Plan != next.Plan {
		fields = append(fields, "plan")
	}
	if current.Status != next.Status {
		fields = append(fields, "status")
	}
	if !sameTime(current.CurrentPeriodStart, next.CurrentPeriodStart) {
		fields = append(fields, "current_period_start")
	}
	if !sameTime(current.CurrentPeriodEnd, next.CurrentPeriodEnd) {
		fields = append(fields, "current_period_end")
	}
	if !sameTime(current.TrialEndsAt, next.TrialEndsAt) {
		fields = append(fields, "trial_ends_at")
	}
	sort.Strings(fields)
	return fields
}

func sameTime(a, b *time.Time) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return a.Equal(*b)
}

func mapOrganizationSubscriptionError(err error) error {
	if orgsubscription.IsOpenConflict(err) {
		return conflict("subscription_exists", "the organization already has an open subscription")
	}
	if errors.Is(err, orgsubscription.ErrNotFound) {
		return notFound()
	}
	return err
}
