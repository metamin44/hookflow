package eventapp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/metamin44/hookflow/internal/app/delivery"
	"github.com/metamin44/hookflow/internal/app/idempotency"
	"github.com/metamin44/hookflow/internal/app/outbox"
	"github.com/metamin44/hookflow/internal/modules/endpoint"
	"github.com/metamin44/hookflow/internal/modules/event"
	"github.com/metamin44/hookflow/internal/modules/organization"
)

// AcceptInput is one authenticated, in-limit ingestion request. The HTTP layer
// has already validated the header syntax, scope, body size and organization
// status; this layer owns every database decision.
type AcceptInput struct {
	OrganizationID string
	IdempotencyKey string
	Body           []byte
}

// AcceptEvent runs the approved ingestion pipeline and commits it.
//
// Order: parse/validate body -> fingerprint -> begin transaction -> organization
// share lock -> state recheck -> claim or resolve idempotency -> single sample
// time -> destination match -> Event + link + Delivery + Outbox in one batch ->
// commit. The result is safe to report only after commit succeeds.
func (s *Service) AcceptEvent(ctx context.Context, input AcceptInput) (Result, error) {
	if input.OrganizationID == "" {
		return Result{}, errors.New("eventapp: organization id is required")
	}
	if input.IdempotencyKey == "" {
		return Result{}, errors.New("eventapp: idempotency key is required")
	}
	document, err := event.Parse(input.Body)
	if err != nil {
		return Result{}, err
	}
	fingerprint, err := document.Fingerprint()
	if err != nil {
		return Result{}, fmt.Errorf("fingerprint accepted body: %w", err)
	}
	result, err := s.accept(ctx, input, document, fingerprint)
	if err != nil {
		return Result{}, err
	}
	if !result.Replayed && document.InexactNumbers > 0 {
		s.options.Metrics.Add(event.InexactNumberMetric, int64(document.InexactNumbers))
	}
	return result, nil
}

func (s *Service) accept(
	ctx context.Context,
	input AcceptInput,
	document event.Document,
	fingerprint [32]byte,
) (Result, error) {
	tx, err := s.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Result{}, fmt.Errorf("begin ingestion transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := s.lockActiveOrganization(ctx, tx, input.OrganizationID); err != nil {
		return Result{}, err
	}

	recordID, err := s.options.NewID()
	if err != nil {
		return Result{}, fmt.Errorf("generate idempotency record id: %w", err)
	}
	resolution, err := idempotency.Claim(ctx, tx, idempotency.ClaimInput{
		RecordID:       recordID,
		OrganizationID: input.OrganizationID,
		Key:            input.IdempotencyKey,
		Hash:           fingerprint[:],
	})
	if err != nil {
		if errors.Is(err, idempotency.ErrConflict) {
			return Result{}, ErrIdempotencyConflict
		}
		return Result{}, fmt.Errorf("resolve idempotency claim: %w", err)
	}
	if !resolution.Owner {
		return s.replay(ctx, tx, input.OrganizationID, resolution.ReplayEventID)
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return Result{}, err
	}
	destinations, err := event.MatchDestinations(ctx, tx, input.OrganizationID, document.EventType)
	if err != nil {
		return Result{}, fmt.Errorf("match destinations: %w", err)
	}
	eventID, err := s.options.NewID()
	if err != nil {
		return Result{}, fmt.Errorf("generate event id: %w", err)
	}
	batch, err := s.buildBatch(input.OrganizationID, eventID, document, destinations, t)
	if err != nil {
		return Result{}, err
	}
	idempotency.AppendLink(batch, resolution.RecordID, eventID)
	receivedAt, err := s.executeBatch(ctx, tx, batch, len(destinations))
	if err != nil {
		return Result{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit ingestion transaction: %w", err)
	}
	s.logger.InfoContext(ctx, "event accepted",
		"event_id", eventID,
		"organization_id", input.OrganizationID,
		"destination_count", len(destinations),
	)
	return Result{
		EventID:          eventID,
		DestinationCount: len(destinations),
		ReceivedAt:       receivedAt,
	}, nil
}

// replay returns the stored outcome for an unexpired key with the same body.
// The transaction is read-only apart from the idempotency row lock.
func (s *Service) replay(
	ctx context.Context,
	tx pgx.Tx,
	organizationID string,
	eventID string,
) (Result, error) {
	record, err := event.Load(ctx, tx, organizationID, eventID)
	if err != nil {
		if errors.Is(err, event.ErrNotFound) {
			return Result{}, fmt.Errorf("%w: %s", errLinkedEventMissing, eventID)
		}
		return Result{}, fmt.Errorf("load replayed event: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return Result{}, fmt.Errorf("commit replay transaction: %w", err)
	}
	return Result{
		EventID:          record.ID,
		DestinationCount: record.DestinationCount,
		ReceivedAt:       record.ReceivedAt,
		Replayed:         true,
	}, nil
}

func (s *Service) lockActiveOrganization(ctx context.Context, tx pgx.Tx, organizationID string) error {
	status, err := organization.LockShare(ctx, tx, organizationID)
	if err != nil {
		if errors.Is(err, organization.ErrNotFound) {
			return &InactiveOrganizationError{Status: "missing"}
		}
		return fmt.Errorf("lock organization for ingestion: %w", err)
	}
	if status != organization.StatusActive {
		return &InactiveOrganizationError{Status: status}
	}
	return nil
}

// buildBatch queues the Event insert first, then one Delivery and one Outbox
// row per matched destination. Identifiers are application-generated UUIDv7.
func (s *Service) buildBatch(
	organizationID string,
	eventID string,
	document event.Document,
	destinations []event.Destination,
	t time.Time,
) (*pgx.Batch, error) {
	batch := &pgx.Batch{}
	event.AppendInsert(batch, event.Record{
		ID:               eventID,
		OrganizationID:   organizationID,
		Type:             document.EventType,
		Payload:          document.Payload,
		DestinationCount: len(destinations),
	})
	for _, destination := range destinations {
		deliveryID, err := s.options.NewID()
		if err != nil {
			return nil, fmt.Errorf("generate delivery id: %w", err)
		}
		outboxID, err := s.options.NewID()
		if err != nil {
			return nil, fmt.Errorf("generate outbox id: %w", err)
		}
		var storedHeaders map[string]string
		if err := json.Unmarshal([]byte(destination.Headers), &storedHeaders); err != nil {
			return nil, fmt.Errorf("decode destination headers: %w", err)
		}
		snapshotHeaders, err := json.Marshal(endpoint.SafeHeaders(storedHeaders))
		if err != nil {
			return nil, fmt.Errorf("encode delivery snapshot headers: %w", err)
		}
		if err := delivery.AppendAccepted(batch, delivery.Accepted{
			ID:              deliveryID,
			OrganizationID:  organizationID,
			EventID:         eventID,
			EndpointID:      destination.EndpointID,
			SubscriptionID:  destination.SubscriptionID,
			URL:             destination.URL,
			Headers:         string(snapshotHeaders),
			SecretEncrypted: destination.SecretEncrypted,
			NextAttemptAt:   t,
		}); err != nil {
			return nil, fmt.Errorf("queue delivery snapshot: %w", err)
		}
		if err := outbox.AppendAccepted(batch, outbox.Accepted{
			ID:             outboxID,
			OrganizationID: organizationID,
			DeliveryID:     deliveryID,
			AvailableAt:    t,
		}); err != nil {
			return nil, fmt.Errorf("queue initial outbox row: %w", err)
		}
	}
	return batch, nil
}

// executeBatch consumes the queued results in order and enforces the exact row
// counts: one Event, one link, and one Delivery plus one Outbox row per
// destination. Any mismatch rolls the whole transaction back.
func (s *Service) executeBatch(ctx context.Context, tx pgx.Tx, batch *pgx.Batch, destinations int) (time.Time, error) {
	results := tx.SendBatch(ctx, batch)
	defer func() { _ = results.Close() }()

	var receivedAt time.Time
	if err := results.QueryRow().Scan(&receivedAt); err != nil {
		return time.Time{}, fmt.Errorf("insert event: %w", err)
	}
	tag, err := results.Exec()
	if err != nil {
		return time.Time{}, fmt.Errorf("link idempotency record: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return time.Time{}, fmt.Errorf("idempotency link updated %d rows, want 1", tag.RowsAffected())
	}
	for i := 0; i < destinations; i++ {
		tag, err := results.Exec()
		if err != nil {
			return time.Time{}, fmt.Errorf("insert delivery snapshot: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return time.Time{}, fmt.Errorf("delivery snapshot inserted %d rows, want 1", tag.RowsAffected())
		}
		tag, err = results.Exec()
		if err != nil {
			return time.Time{}, fmt.Errorf("insert initial outbox row: %w", err)
		}
		if tag.RowsAffected() != 1 {
			return time.Time{}, fmt.Errorf("initial outbox row inserted %d rows, want 1", tag.RowsAffected())
		}
	}
	if err := results.Close(); err != nil {
		return time.Time{}, fmt.Errorf("finish ingestion batch: %w", err)
	}
	return receivedAt, nil
}

// sampleTime reads the single database timestamp shared by every initial
// delivery and outbox row in this transaction.
func sampleTime(ctx context.Context, tx pgx.Tx) (time.Time, error) {
	var t time.Time
	if err := tx.QueryRow(ctx, `SELECT clock_timestamp()`).Scan(&t); err != nil {
		return time.Time{}, fmt.Errorf("sample ingestion time: %w", err)
	}
	return t, nil
}
