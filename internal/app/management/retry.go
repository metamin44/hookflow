package management

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/modules/audit"
	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/modules/organization"
	"github.com/metamin44/hookflow/internal/modules/subscription"
)

// retryRequestWindow is the approved caller-facing idempotency lifetime for a
// manual delivery retry. It is independent of the 90-day event retention.
const retryRequestWindow = 72 * time.Hour

// RetryResult is the safe outcome of a created or replayed retry generation.
type RetryResult struct {
	ID               string
	EventID          string
	EndpointID       string
	Generation       int
	ParentDeliveryID *string
	State            string
	AttemptCount     int
	CreatedAt        time.Time
	Replayed         bool
}

// RetryDelivery creates a new Delivery generation from one terminal failed
// source. It owns one transaction and follows the approved lock order:
// organization -> endpoint -> source delivery.
func (s *Service) RetryDelivery(ctx context.Context, identity Identity, meta RequestMeta, sourceID, key string) (RetryResult, error) {
	if !canonicalUUID(sourceID) {
		return RetryResult{}, notFound()
	}
	hash := retryRequestHash(sourceID)
	var result RetryResult
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, identity.OrganizationID)
		if err != nil {
			if errors.Is(err, organization.ErrNotFound) {
				return notFound()
			}
			return err
		}
		if status == organization.StatusArchived {
			return forbidden("organization_archived", "archived organizations reject management writes")
		}
		if status == organization.StatusSuspended {
			return forbidden("organization_suspended", "suspended organizations cannot retry deliveries")
		}
		now, err := retryNow(ctx, tx)
		if err != nil {
			return err
		}
		replayed, found, err := retryReplay(ctx, tx, identity.OrganizationID, sourceID, key, hash, now)
		if err != nil {
			return err
		}
		if found {
			result = replayed
			return nil
		}
		created, err := s.createRetryGeneration(ctx, tx, identity, meta, sourceID, key, hash, now)
		if err != nil {
			return err
		}
		result = created
		return nil
	})
	if txErr != nil {
		return RetryResult{}, txErr
	}
	return result, nil
}

// retryReplay resolves an existing key before any retry eligibility check.
// An expired claim is discarded regardless of its hash, matching the approved
// 72-hour idempotency semantics. A live claim must match source and hash.
func retryReplay(ctx context.Context, tx pgx.Tx, organizationID, sourceID, key string, hash []byte, now time.Time) (RetryResult, bool, error) {
	request, err := delivery.FindRetryRequest(ctx, tx, organizationID, key)
	if errors.Is(err, delivery.ErrRetryRequestNotFound) {
		return RetryResult{}, false, nil
	}
	if err != nil {
		return RetryResult{}, false, err
	}
	if !request.ExpiresAt.After(now) {
		if err := delivery.DeleteRetryRequest(ctx, tx, organizationID, request.ID); err != nil {
			return RetryResult{}, false, err
		}
		return RetryResult{}, false, nil
	}
	if request.SourceDeliveryID != sourceID || !bytes.Equal(request.RequestHash, hash) {
		return RetryResult{}, false, conflict("idempotency_key_conflict",
			"the Idempotency-Key was already used for a different retry request")
	}
	view, err := delivery.ReadRetryDelivery(ctx, tx, organizationID, request.NewDeliveryID)
	if errors.Is(err, delivery.ErrRetrySourceNotFound) {
		return RetryResult{}, false, internalError()
	}
	if err != nil {
		return RetryResult{}, false, err
	}
	return retryResultOf(view, true), true, nil
}

func (s *Service) createRetryGeneration(ctx context.Context, tx pgx.Tx, identity Identity, meta RequestMeta, sourceID, key string, hash []byte, now time.Time) (RetryResult, error) {
	lockedEndpoint, source, err := s.lockRetrySource(ctx, tx, identity.OrganizationID, sourceID, now)
	if err != nil {
		return RetryResult{}, err
	}
	headers, err := retryHeaders(lockedEndpoint)
	if err != nil {
		return RetryResult{}, err
	}
	subscriptionID, err := subscription.ActiveMatchingID(ctx, tx,
		identity.OrganizationID, source.EndpointID, source.EventType)
	if err != nil {
		return RetryResult{}, err
	}
	generation, err := delivery.AllocateGeneration(ctx, tx,
		identity.OrganizationID, source.EventID, source.EndpointID)
	if err != nil {
		return RetryResult{}, err
	}
	snapshot, err := s.newRetrySnapshot(identity.OrganizationID, source, lockedEndpoint, headers, subscriptionID, generation, now)
	if err != nil {
		return RetryResult{}, err
	}
	if _, err := delivery.InsertRetry(ctx, tx, snapshot); err != nil {
		return RetryResult{}, err
	}
	outboxID, err := s.newID()
	if err != nil {
		return RetryResult{}, internalError()
	}
	if err := outbox.InsertAccepted(ctx, tx, outbox.Accepted{
		ID: outboxID, OrganizationID: identity.OrganizationID,
		DeliveryID: snapshot.ID, AvailableAt: now,
	}); err != nil {
		return RetryResult{}, err
	}
	requestID, err := s.newID()
	if err != nil {
		return RetryResult{}, internalError()
	}
	err = delivery.InsertRetryRequest(ctx, tx, delivery.RetryRequest{
		ID: requestID, OrganizationID: identity.OrganizationID,
		IdempotencyKey: key, RequestHash: hash,
		SourceDeliveryID: source.DeliveryID, NewDeliveryID: snapshot.ID,
		CreatedAt: now, ExpiresAt: now.Add(retryRequestWindow),
	})
	if errors.Is(err, delivery.ErrRetryRequestConflict) {
		return RetryResult{}, conflict("idempotency_key_conflict",
			"the Idempotency-Key was already used for a different retry request")
	}
	if err != nil {
		return RetryResult{}, err
	}
	parent := source.DeliveryID
	if err := s.writeAudit(ctx, tx, identity, meta, "delivery.retried", audit.TargetDelivery, &snapshot.ID,
		map[string]any{
			"source_delivery_id": source.DeliveryID,
			"new_delivery_id":    snapshot.ID,
			"generation":         generation,
			"endpoint_id":        source.EndpointID,
		}); err != nil {
		return RetryResult{}, err
	}
	return RetryResult{
		ID: snapshot.ID, EventID: snapshot.EventID, EndpointID: snapshot.EndpointID,
		Generation: snapshot.Generation, ParentDeliveryID: &parent,
		State: "pending", AttemptCount: 0, CreatedAt: now,
	}, nil
}

// lockRetrySource observes the source endpoint without a delivery lock, then
// takes the endpoint lock and finally the source delivery lock. Every
// eligibility rule is revalidated after those locks.
func (s *Service) lockRetrySource(ctx context.Context, tx pgx.Tx, organizationID, sourceID string, now time.Time) (endpoint.Record, delivery.RetrySource, error) {
	located, err := delivery.ReadRetrySourceIdentity(ctx, tx, organizationID, sourceID)
	if errors.Is(err, delivery.ErrRetrySourceNotFound) {
		return endpoint.Record{}, delivery.RetrySource{}, notFound()
	}
	if err != nil {
		return endpoint.Record{}, delivery.RetrySource{}, err
	}
	lockedEndpoint, err := endpoint.Lock(ctx, tx, organizationID, located.EndpointID)
	if errors.Is(err, endpoint.ErrNotFound) {
		return endpoint.Record{}, delivery.RetrySource{}, notFound()
	}
	if err != nil {
		return endpoint.Record{}, delivery.RetrySource{}, err
	}
	if lockedEndpoint.Deleted() || lockedEndpoint.Status != endpoint.StatusActive {
		return endpoint.Record{}, delivery.RetrySource{}, conflict("endpoint_ineligible",
			"the endpoint is deleted, paused or otherwise ineligible")
	}
	source, err := delivery.LockRetrySource(ctx, tx, organizationID, sourceID)
	if errors.Is(err, delivery.ErrRetrySourceNotFound) {
		return endpoint.Record{}, delivery.RetrySource{}, notFound()
	}
	if err != nil {
		return endpoint.Record{}, delivery.RetrySource{}, err
	}
	if source.EndpointID != located.EndpointID || source.EventID != located.EventID {
		return endpoint.Record{}, delivery.RetrySource{}, internalError()
	}
	if source.State != "failed" {
		return endpoint.Record{}, delivery.RetrySource{}, conflict("delivery_not_retryable",
			"only a terminal failed delivery can be retried")
	}
	if source.LeaseToken != "" && source.LeaseExpiresAt.Valid && source.LeaseExpiresAt.Time.After(now) {
		return endpoint.Record{}, delivery.RetrySource{}, conflict("delivery_leased",
			"the source delivery is still leased by a worker")
	}
	if !source.EventExpiresAt.After(now) {
		return endpoint.Record{}, delivery.RetrySource{}, conflict("event_expired",
			"the event delivery window has expired")
	}
	return lockedEndpoint, source, nil
}

func (s *Service) newRetrySnapshot(organizationID string, source delivery.RetrySource, locked endpoint.Record, headers string, subscriptionID *string, generation int, now time.Time) (delivery.RetrySnapshot, error) {
	deliveryID, err := s.newID()
	if err != nil {
		return delivery.RetrySnapshot{}, internalError()
	}
	subscriptionValue := ""
	if subscriptionID != nil {
		subscriptionValue = *subscriptionID
	}
	return delivery.RetrySnapshot{
		ID: deliveryID, OrganizationID: organizationID, EventID: source.EventID,
		EndpointID: source.EndpointID, SubscriptionID: subscriptionValue,
		URL: locked.URL, Headers: headers, SecretEncrypted: locked.SecretEncrypted,
		Generation: generation, ParentID: source.DeliveryID,
		NextAttemptAt: now, CreatedAt: now,
	}, nil
}

func retryHeaders(locked endpoint.Record) (string, error) {
	encoded, err := json.Marshal(endpoint.SafeHeaders(locked.HTTPHeaders))
	if err != nil {
		return "", internalError()
	}
	return string(encoded), nil
}

func retryNow(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var now time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return time.Time{}, internalError()
	}
	return now, nil
}

func retryRequestHash(sourceID string) []byte {
	sum := sha256.Sum256([]byte("hookflow.delivery.retry.v1:" + sourceID))
	return sum[:]
}

func retryResultOf(view delivery.RetryDeliveryView, replayed bool) RetryResult {
	return RetryResult{
		ID: view.ID, EventID: view.EventID, EndpointID: view.EndpointID,
		Generation: view.Generation, ParentDeliveryID: view.ParentDeliveryID,
		State: view.State, AttemptCount: view.AttemptCount, CreatedAt: view.CreatedAt,
		Replayed: replayed,
	}
}
