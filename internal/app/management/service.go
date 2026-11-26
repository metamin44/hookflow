package management

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sort"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/modules/audit"
	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/modules/organization"
	"github.com/metamin44/hookflow/internal/modules/subscription"
	"github.com/metamin44/hookflow/internal/platform/destination"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/kek"
)

// Identity is the authenticated tenant principal.
type Identity struct {
	APIKeyID           string
	OrganizationID     string
	OrganizationStatus string
}

// RequestMeta carries the safe request provenance written to audit rows.
type RequestMeta struct {
	IP        *string
	UserAgent *string
}

// CreateInput is a validated endpoint-create document.
type CreateInput struct {
	Name        string
	Description *string
	URL         string
	Headers     map[string]string
}

// PatchInput is a validated endpoint-patch document. Presence flags are
// significant: a null description is not the same as an absent one.
type PatchInput struct {
	Name           *string
	Description    *string
	DescriptionSet bool
	URL            *string
	Headers        map[string]string
	HeadersSet     bool
	Status         *string
}

// PatchResult reports the resulting row and whether a real change happened.
type PatchResult struct {
	Record  endpoint.Record
	Changed bool
	Fields  []string
}

// Database is the pool surface the management service needs. It is satisfied
// by *pgxpool.Pool.
type Database interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Service owns the Phase 6 management use cases and their transactions.
type Service struct {
	pool      Database
	provider  kek.Provider
	maxActive int
	logger    *slog.Logger
	random    io.Reader
	newID     func() (string, error)
	// resolver drives the advisory endpoint create/update DNS preflight. A nil
	// resolver selects net.DefaultResolver.
	resolver destination.Resolver
}

// NewService builds the management service.
func NewService(pool Database, provider kek.Provider, maxActive int, logger *slog.Logger, random io.Reader) (*Service, error) {
	if pool == nil {
		return nil, errors.New("management: database pool is required")
	}
	if provider == nil {
		return nil, errors.New("management: secret provider is required")
	}
	if maxActive <= 0 {
		return nil, errors.New("management: active endpoint ceiling must be positive")
	}
	if logger == nil {
		logger = slog.Default()
	}
	if random == nil {
		return nil, errors.New("management: randomness source is required")
	}
	return &Service{
		pool:      pool,
		provider:  provider,
		maxActive: maxActive,
		logger:    logger,
		random:    random,
		newID:     id.NewV7,
	}, nil
}

// preflightDestination applies the advisory destination DNS check after URL
// syntax validation. A prohibited answer rejects the write; a DNS failure
// allows it because delivery-time enforcement remains authoritative.
func (s *Service) preflightDestination(ctx context.Context, raw string) *endpoint.FieldError {
	if err := destination.Preflight(ctx, s.resolver, raw); err != nil {
		return &endpoint.FieldError{
			Field:  "url",
			Code:   "invalid_url",
			Detail: "url resolves to a prohibited address",
		}
	}
	return nil
}

// CreateEndpoint creates an active endpoint and returns its one-time secret.
func (s *Service) CreateEndpoint(ctx context.Context, identity Identity, meta RequestMeta, input CreateInput) (endpoint.Record, string, error) {
	if field := endpoint.ValidateName(input.Name); field != nil {
		return endpoint.Record{}, "", fieldRejection(field.Field, field.Code, field.Detail)
	}
	if field := endpoint.ValidateDescription(input.Description); field != nil {
		return endpoint.Record{}, "", fieldRejection(field.Field, field.Code, field.Detail)
	}
	if field := endpoint.ValidateURL(input.URL); field != nil {
		return endpoint.Record{}, "", fieldRejection(field.Field, field.Code, field.Detail)
	}
	headers, field := endpoint.CanonicalHeaders(input.Headers)
	if field != nil {
		return endpoint.Record{}, "", fieldRejection(field.Field, field.Code, field.Detail)
	}
	if field := s.preflightDestination(ctx, input.URL); field != nil {
		return endpoint.Record{}, "", fieldRejection(field.Field, field.Code, field.Detail)
	}
	endpointID, err := s.newID()
	if err != nil {
		return endpoint.Record{}, "", internalError()
	}
	current, err := s.readStatus(ctx, identity.OrganizationID)
	if err != nil {
		return endpoint.Record{}, "", err
	}
	if current == organization.StatusArchived {
		return endpoint.Record{}, "", forbidden("organization_archived", "archived organizations reject management writes")
	}
	if current == organization.StatusSuspended {
		return endpoint.Record{}, "", forbidden("organization_suspended", "suspended organizations cannot create endpoints")
	}
	material, err := s.newSecret(ctx, identity.OrganizationID, endpointID)
	if err != nil {
		s.logProviderFailure(err)
		return endpoint.Record{}, "", providerHTTPError(err)
	}

	var created endpoint.Record
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		if status == organization.StatusSuspended {
			return forbidden("organization_suspended", "suspended organizations cannot create endpoints")
		}
		count, err := endpoint.CountActive(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if count >= s.maxActive {
			return conflict("active_endpoint_cap", "the organization has reached its active endpoint limit")
		}
		created, err = endpoint.Insert(ctx, tx, endpoint.Record{
			ID:              endpointID,
			OrganizationID:  identity.OrganizationID,
			Name:            input.Name,
			Description:     input.Description,
			URL:             input.URL,
			SecretEncrypted: material.Envelope,
			HTTPHeaders:     headers,
			Status:          endpoint.StatusActive,
		})
		if err != nil {
			if endpoint.IsDuplicateName(err) {
				return conflict("endpoint_name_conflict", "a live endpoint with this name already exists")
			}
			return err
		}
		return s.writeAudit(ctx, tx, identity, meta, "endpoint.created", audit.TargetEndpoint, &created.ID, nil)
	})
	if txErr != nil {
		return endpoint.Record{}, "", txErr
	}
	return created, material.Token, nil
}

// ListEndpoints returns the live endpoints of the organization.
func (s *Service) ListEndpoints(ctx context.Context, organizationID string) ([]endpoint.Record, error) {
	return endpoint.List(ctx, s.pool, organizationID)
}

// GetEndpoint returns one live endpoint.
func (s *Service) GetEndpoint(ctx context.Context, organizationID, endpointID string) (endpoint.Record, error) {
	record, err := endpoint.Get(ctx, s.pool, organizationID, endpointID)
	if errors.Is(err, endpoint.ErrNotFound) {
		return endpoint.Record{}, notFound()
	}
	if err != nil {
		return endpoint.Record{}, err
	}
	return record, nil
}

// PatchEndpoint applies the approved PATCH ordering: organization lock,
// endpoint lock, suspended policy, If-Match, change detection, cap check, write.
func (s *Service) PatchEndpoint(ctx context.Context, identity Identity, meta RequestMeta, endpointID string, patch PatchInput, ifMatch string) (PatchResult, error) {
	validated, appErr := validatePatchInput(patch)
	if appErr != nil {
		return PatchResult{}, appErr
	}
	patch = validated
	if patch.URL != nil {
		if field := s.preflightDestination(ctx, *patch.URL); field != nil {
			return PatchResult{}, fieldRejection(field.Field, field.Code, field.Detail)
		}
	}
	var result PatchResult
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		current, err := endpoint.Lock(ctx, tx, identity.OrganizationID, endpointID)
		if err != nil {
			return err
		}
		if current.Deleted() {
			return notFound()
		}
		if status == organization.StatusSuspended && suspendedDenied(patch) {
			return forbidden("organization_suspended", "the request contains a field that a suspended organization cannot change")
		}
		currentTag := endpoint.StrongTag(current.Represent(), current.SecretEncrypted)
		if err := evaluateIfMatch(ifMatch, currentTag); err != nil {
			return ifMatchHTTPError(err, currentTag)
		}
		next, changedFields := applyPatch(current, patch)
		if len(changedFields) == 0 {
			result = PatchResult{Record: current, Changed: false}
			return nil
		}
		if current.Status != endpoint.StatusActive && next.Status == endpoint.StatusActive {
			count, err := endpoint.CountActive(ctx, tx, identity.OrganizationID)
			if err != nil {
				return err
			}
			if count >= s.maxActive {
				return conflict("active_endpoint_cap", "the organization has reached its active endpoint limit")
			}
		}
		var updated endpoint.Record
		if patch.HeadersSet {
			updated, err = endpoint.Update(ctx, tx, next)
		} else {
			updated, err = endpoint.UpdateMetadata(ctx, tx, next)
		}
		if err != nil {
			if endpoint.IsDuplicateName(err) {
				return conflict("endpoint_name_conflict", "a live endpoint with this name already exists")
			}
			return err
		}
		action := auditAction(current.Status, updated.Status)
		metadata := map[string]any{"fields": changedFields}
		if err := s.writeAudit(ctx, tx, identity, meta, action, audit.TargetEndpoint, &updated.ID, metadata); err != nil {
			return err
		}
		result = PatchResult{Record: updated, Changed: true, Fields: changedFields}
		return nil
	})
	if txErr != nil {
		return PatchResult{}, txErr
	}
	return result, nil
}

// DeleteEndpoint soft-deletes a live endpoint without touching its secret or
// any subscription row.
func (s *Service) DeleteEndpoint(ctx context.Context, identity Identity, meta RequestMeta, endpointID string) error {
	return s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		current, err := endpoint.Lock(ctx, tx, identity.OrganizationID, endpointID)
		if err != nil {
			return err
		}
		if current.Deleted() {
			return notFound()
		}
		if _, err := endpoint.SoftDelete(ctx, tx, identity.OrganizationID, endpointID); err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, identity, meta, "endpoint.deleted", audit.TargetEndpoint, &endpointID, nil)
	})
}

// RotateSecret performs the two-phase provider boundary: read and validate,
// wrap with no lock held, then re-lock, revalidate and update atomically.
func (s *Service) RotateSecret(ctx context.Context, identity Identity, meta RequestMeta, endpointID, ifMatch string) (endpoint.Record, string, error) {
	status, err := s.readStatus(ctx, identity.OrganizationID)
	if err != nil {
		return endpoint.Record{}, "", err
	}
	if status == organization.StatusArchived {
		return endpoint.Record{}, "", forbidden("organization_archived", "archived organizations reject management writes")
	}
	current, err := s.GetEndpoint(ctx, identity.OrganizationID, endpointID)
	if err != nil {
		return endpoint.Record{}, "", err
	}
	firstTag := endpoint.StrongTag(current.Represent(), current.SecretEncrypted)
	if err := evaluateIfMatch(ifMatch, firstTag); err != nil {
		return endpoint.Record{}, "", ifMatchHTTPError(err, firstTag)
	}
	material, err := s.newSecret(ctx, identity.OrganizationID, endpointID)
	if err != nil {
		s.logProviderFailure(err)
		return endpoint.Record{}, "", providerHTTPError(err)
	}

	var updated endpoint.Record
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		locked, err := endpoint.Lock(ctx, tx, identity.OrganizationID, endpointID)
		if err != nil {
			return err
		}
		if locked.Deleted() {
			return notFound()
		}
		currentTag := endpoint.StrongTag(locked.Represent(), locked.SecretEncrypted)
		if err := evaluateIfMatch(ifMatch, currentTag); err != nil {
			return ifMatchHTTPError(err, currentTag)
		}
		updated, err = endpoint.UpdateSecret(ctx, tx, identity.OrganizationID, endpointID, material.Envelope)
		if err != nil {
			return err
		}
		return s.writeAudit(ctx, tx, identity, meta, "endpoint.secret_rotated", audit.TargetEndpoint, &endpointID, nil)
	})
	if txErr != nil {
		return endpoint.Record{}, "", txErr
	}
	return updated, material.Token, nil
}

// ListSubscriptions returns every routing rule of one live endpoint.
func (s *Service) ListSubscriptions(ctx context.Context, organizationID, endpointID string) ([]subscription.Record, error) {
	if _, err := s.GetEndpoint(ctx, organizationID, endpointID); err != nil {
		return nil, err
	}
	return subscription.List(ctx, s.pool, organizationID, endpointID)
}

// CreateSubscription creates or reactivates one exact event-type rule.
func (s *Service) CreateSubscription(ctx context.Context, identity Identity, meta RequestMeta, endpointID, eventType string) (subscription.Record, bool, error) {
	var record subscription.Record
	var created bool
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		if status == organization.StatusSuspended {
			return forbidden("organization_suspended", "suspended organizations cannot create or reactivate subscriptions")
		}
		locked, err := endpoint.Lock(ctx, tx, identity.OrganizationID, endpointID)
		if err != nil {
			return err
		}
		if locked.Deleted() {
			return notFound()
		}
		newID, err := s.newID()
		if err != nil {
			return internalError()
		}
		record, created, err = subscription.CreateOrReactivate(ctx, tx, subscription.Record{
			ID:             newID,
			OrganizationID: identity.OrganizationID,
			EndpointID:     endpointID,
			EventType:      eventType,
		})
		if errors.Is(err, subscription.ErrAlreadyActive) {
			return conflict("subscription_exists", "the endpoint already subscribes to this event type")
		}
		if err != nil {
			return err
		}
		action := "subscription.reactivated"
		if created {
			action = "subscription.created"
		}
		return s.writeAudit(ctx, tx, identity, meta, action, audit.TargetSubscription, &record.ID,
			map[string]any{"event_type": eventType})
	})
	if txErr != nil {
		return subscription.Record{}, false, txErr
	}
	return record, created, nil
}

// DeactivateSubscription turns a routing rule off; repeating it is a no-op.
func (s *Service) DeactivateSubscription(ctx context.Context, identity Identity, meta RequestMeta, endpointID, subscriptionID string) (subscription.Record, bool, error) {
	var record subscription.Record
	var changed bool
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		locked, err := endpoint.Lock(ctx, tx, identity.OrganizationID, endpointID)
		if err != nil {
			return err
		}
		if locked.Deleted() {
			return notFound()
		}
		if _, err := subscription.Lock(ctx, tx, identity.OrganizationID, endpointID, subscriptionID); err != nil {
			if errors.Is(err, subscription.ErrNotFound) {
				return notFound()
			}
			return err
		}
		record, changed, err = subscription.Deactivate(ctx, tx, identity.OrganizationID, endpointID, subscriptionID)
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return s.writeAudit(ctx, tx, identity, meta, "subscription.deactivated", audit.TargetSubscription, &subscriptionID,
			map[string]any{"event_type": record.EventType})
	})
	if txErr != nil {
		return subscription.Record{}, false, txErr
	}
	return record, changed, nil
}

func (s *Service) readStatus(ctx context.Context, organizationID string) (string, error) {
	status, err := organization.Read(ctx, s.pool, organizationID)
	if errors.Is(err, organization.ErrNotFound) {
		return "", notFound()
	}
	if err != nil {
		return "", err
	}
	return status, nil
}

func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin management transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return mapStoreError(err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit management transaction: %w", err)
	}
	return nil
}

func (s *Service) writeAudit(ctx context.Context, tx pgx.Tx, identity Identity, meta RequestMeta, action, targetType string, targetID *string, metadata map[string]any) error {
	auditID, err := s.newID()
	if err != nil {
		return internalError()
	}
	actorID := identity.APIKeyID
	return audit.Insert(ctx, tx, audit.Record{
		ID:             auditID,
		OrganizationID: identity.OrganizationID,
		ActorType:      audit.ActorAPIKey,
		ActorID:        &actorID,
		Action:         action,
		TargetType:     targetType,
		TargetID:       targetID,
		IP:             meta.IP,
		UserAgent:      meta.UserAgent,
		Metadata:       metadata,
	})
}

func (s *Service) logProviderFailure(err error) {
	class := "permanent"
	if kek.IsTransient(err) {
		class = "transient"
	}
	s.logger.Warn("secret provider failure", "class", class)
}

func providerHTTPError(err error) *HTTPError {
	if kek.IsTransient(err) {
		return providerUnavailable()
	}
	return internalError()
}

func ifMatchHTTPError(err error, currentTag string) *HTTPError {
	switch {
	case errors.Is(err, errMissingIfMatch):
		return preconditionRequired()
	case errors.Is(err, errInvalidIfMatch):
		return badRequest("invalid_if_match", "If-Match must be a strong entity-tag list or *")
	case errors.Is(err, errStaleIfMatch):
		return preconditionFailed(currentTag)
	default:
		return internalError()
	}
}

func suspendedDenied(patch PatchInput) bool {
	if patch.URL != nil || patch.HeadersSet {
		return true
	}
	return patch.Status != nil && *patch.Status == endpoint.StatusActive
}

// validatePatchInput enforces field values before any lock is taken, matching
// the approved "400 before locks" step. Headers are canonicalized here so the
// no-op comparison sees the stored form.
func validatePatchInput(patch PatchInput) (PatchInput, *HTTPError) {
	if patch.Name != nil {
		if field := endpoint.ValidateName(*patch.Name); field != nil {
			return PatchInput{}, fieldRejection(field.Field, field.Code, field.Detail)
		}
	}
	if patch.DescriptionSet {
		if field := endpoint.ValidateDescription(patch.Description); field != nil {
			return PatchInput{}, fieldRejection(field.Field, field.Code, field.Detail)
		}
	}
	if patch.URL != nil {
		if field := endpoint.ValidateURL(*patch.URL); field != nil {
			return PatchInput{}, fieldRejection(field.Field, field.Code, field.Detail)
		}
	}
	if patch.Status != nil {
		if field := endpoint.ValidateStatus(*patch.Status); field != nil {
			return PatchInput{}, fieldRejection(field.Field, field.Code, field.Detail)
		}
	}
	if patch.HeadersSet {
		canonical, field := endpoint.CanonicalHeaders(patch.Headers)
		if field != nil {
			return PatchInput{}, fieldRejection(field.Field, field.Code, field.Detail)
		}
		patch.Headers = canonical
	}
	return patch, nil
}

func applyPatch(current endpoint.Record, patch PatchInput) (endpoint.Record, []string) {
	next := current
	var changed []string
	if patch.Name != nil && *patch.Name != current.Name {
		next.Name = *patch.Name
		changed = append(changed, "name")
	}
	if patch.DescriptionSet && !sameOptional(current.Description, patch.Description) {
		next.Description = patch.Description
		changed = append(changed, "description")
	}
	if patch.URL != nil && *patch.URL != current.URL {
		next.URL = *patch.URL
		changed = append(changed, "url")
	}
	if patch.HeadersSet && !sameHeaders(current.HTTPHeaders, patch.Headers) {
		next.HTTPHeaders = patch.Headers
		changed = append(changed, "http_headers")
	}
	if patch.Status != nil && *patch.Status != current.Status {
		next.Status = *patch.Status
		changed = append(changed, "status")
	}
	sort.Strings(changed)
	return next, changed
}

func sameOptional(a, b *string) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

func sameHeaders(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for name, value := range a {
		other, ok := b[name]
		if !ok || other != value {
			return false
		}
	}
	return true
}

func auditAction(oldStatus, newStatus string) string {
	switch {
	case oldStatus == newStatus:
		return "endpoint.updated"
	case newStatus == endpoint.StatusPaused:
		return "endpoint.paused"
	default:
		return "endpoint.activated"
	}
}

func mapStoreError(err error) error {
	var httpErr *HTTPError
	if errors.As(err, &httpErr) {
		return err
	}
	if errors.Is(err, endpoint.ErrNotFound) ||
		errors.Is(err, subscription.ErrNotFound) ||
		errors.Is(err, organization.ErrNotFound) {
		return notFound()
	}
	return err
}
