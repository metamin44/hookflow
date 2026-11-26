package management

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/metamin44/hookflow/internal/modules/history"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// EventListQuery is the validated event history query.
type EventListQuery struct {
	Type         string
	EndpointID   string
	ReceivedFrom *time.Time
	ReceivedTo   *time.Time
	Cursor       string
	PageSize     int
}

// EventListResult is one event page and its continuation cursor.
type EventListResult struct {
	Events     []history.EventSummary
	NextCursor string
}

// DeliveryListQuery is the validated delivery history query.
type DeliveryListQuery struct {
	EndpointID  string
	State       string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
	Cursor      string
	PageSize    int
}

// DeliveryListResult is one delivery page and its continuation cursor.
type DeliveryListResult struct {
	Deliveries []history.DeliverySummary
	NextCursor string
}

// AttemptListResult is one attempt page and its continuation cursor.
type AttemptListResult struct {
	Attempts   []history.Attempt
	NextCursor string
}

// ListEvents returns one newest-first event page scoped to the organization.
func (s *Service) ListEvents(ctx context.Context, organizationID string, query EventListQuery, cursorKey []byte) (EventListResult, error) {
	pageSize, appErr := pageSizeOf(query.PageSize)
	if appErr != nil {
		return EventListResult{}, appErr
	}
	codec, appErr := cursorCodec(cursorKey)
	if appErr != nil {
		return EventListResult{}, appErr
	}
	filter := history.EventFilter{
		Type:         query.Type,
		EndpointID:   query.EndpointID,
		ReceivedFrom: query.ReceivedFrom,
		ReceivedTo:   query.ReceivedTo,
	}
	after, appErr := decodePosition(codec, query.Cursor, history.CursorEvents, organizationID, "", filter.Fingerprint())
	if appErr != nil {
		return EventListResult{}, appErr
	}
	records, hasMore, err := history.ListEvents(ctx, s.pool, organizationID, filter, after, pageSize)
	if err != nil {
		return EventListResult{}, err
	}
	result := EventListResult{Events: records}
	if hasMore {
		last := records[len(records)-1]
		cursor, err := codec.Encode(history.Cursor{
			Kind:           history.CursorEvents,
			OrganizationID: organizationID,
			Filters:        filter.Fingerprint(),
			Time:           last.ReceivedAt,
			ID:             last.ID,
		})
		if err != nil {
			return EventListResult{}, internalError()
		}
		result.NextCursor = cursor
	}
	return result, nil
}

// GetEvent returns one organization-scoped event with its payload and bounded
// delivery summaries.
func (s *Service) GetEvent(ctx context.Context, organizationID, eventID string) (history.EventDetail, error) {
	detail, err := history.GetEvent(ctx, s.pool, organizationID, eventID)
	if errors.Is(err, history.ErrNotFound) {
		return history.EventDetail{}, notFound()
	}
	if err != nil {
		return history.EventDetail{}, err
	}
	deliveries, truncated, err := history.ListEventDeliveries(ctx, s.pool, organizationID, eventID)
	if err != nil {
		return history.EventDetail{}, err
	}
	detail.Deliveries = deliveries
	detail.DeliveriesTruncated = truncated
	return detail, nil
}

// ListDeliveries returns one newest-first delivery page scoped to the
// organization.
func (s *Service) ListDeliveries(ctx context.Context, organizationID string, query DeliveryListQuery, cursorKey []byte) (DeliveryListResult, error) {
	pageSize, appErr := pageSizeOf(query.PageSize)
	if appErr != nil {
		return DeliveryListResult{}, appErr
	}
	codec, appErr := cursorCodec(cursorKey)
	if appErr != nil {
		return DeliveryListResult{}, appErr
	}
	filter := history.DeliveryFilter{
		EndpointID:  query.EndpointID,
		State:       query.State,
		CreatedFrom: query.CreatedFrom,
		CreatedTo:   query.CreatedTo,
	}
	after, appErr := decodePosition(codec, query.Cursor, history.CursorDeliveries, organizationID, "", filter.Fingerprint())
	if appErr != nil {
		return DeliveryListResult{}, appErr
	}
	records, hasMore, err := history.ListDeliveries(ctx, s.pool, organizationID, filter, after, pageSize)
	if err != nil {
		return DeliveryListResult{}, err
	}
	result := DeliveryListResult{Deliveries: records}
	if hasMore {
		last := records[len(records)-1]
		cursor, err := codec.Encode(history.Cursor{
			Kind:           history.CursorDeliveries,
			OrganizationID: organizationID,
			Filters:        filter.Fingerprint(),
			Time:           last.CreatedAt,
			ID:             last.ID,
		})
		if err != nil {
			return DeliveryListResult{}, internalError()
		}
		result.NextCursor = cursor
	}
	return result, nil
}

// GetDelivery returns one organization-scoped delivery.
func (s *Service) GetDelivery(ctx context.Context, organizationID, deliveryID string) (history.DeliveryDetail, error) {
	detail, err := history.GetDelivery(ctx, s.pool, organizationID, deliveryID)
	if errors.Is(err, history.ErrNotFound) {
		return history.DeliveryDetail{}, notFound()
	}
	if err != nil {
		return history.DeliveryDetail{}, err
	}
	return detail, nil
}

// ListDeliveryAttempts returns one oldest-first attempt page. A delivery that
// does not belong to the organization is reported as not found before any
// attempt is read.
func (s *Service) ListDeliveryAttempts(ctx context.Context, organizationID, deliveryID, cursor string, pageSizeValue int, cursorKey []byte) (AttemptListResult, error) {
	pageSize, appErr := pageSizeOf(pageSizeValue)
	if appErr != nil {
		return AttemptListResult{}, appErr
	}
	codec, appErr := cursorCodec(cursorKey)
	if appErr != nil {
		return AttemptListResult{}, appErr
	}
	exists, err := history.DeliveryExists(ctx, s.pool, organizationID, deliveryID)
	if err != nil {
		return AttemptListResult{}, err
	}
	if !exists {
		return AttemptListResult{}, notFound()
	}
	afterNumber := 0
	if cursor != "" {
		decoded, err := codec.Decode(cursor)
		if err != nil || decoded.Kind != history.CursorAttempts ||
			decoded.OrganizationID != organizationID || decoded.ResourceID != deliveryID ||
			decoded.Number < 1 {
			return AttemptListResult{}, cursorRejection()
		}
		afterNumber = decoded.Number
	}
	records, hasMore, err := history.ListAttempts(ctx, s.pool, organizationID, deliveryID, afterNumber, pageSize)
	if err != nil {
		return AttemptListResult{}, err
	}
	result := AttemptListResult{Attempts: records}
	if hasMore {
		last := records[len(records)-1]
		cursor, err := codec.Encode(history.Cursor{
			Kind:           history.CursorAttempts,
			OrganizationID: organizationID,
			ResourceID:     deliveryID,
			Number:         last.AttemptNumber,
		})
		if err != nil {
			return AttemptListResult{}, internalError()
		}
		result.NextCursor = cursor
	}
	return result, nil
}

func cursorCodec(key []byte) (*history.Codec, *HTTPError) {
	codec, err := history.NewCodec(key)
	if err != nil {
		return nil, internalError()
	}
	return codec, nil
}

func decodePosition(codec *history.Codec, value, kind, organizationID, resourceID, filter string) (*history.Position, *HTTPError) {
	if value == "" {
		return nil, nil
	}
	decoded, err := codec.Decode(value)
	if err != nil {
		return nil, cursorRejection()
	}
	if decoded.Kind != kind || decoded.OrganizationID != organizationID ||
		decoded.ResourceID != resourceID || decoded.Filters != filter ||
		decoded.Time.IsZero() || !canonicalUUID(decoded.ID) {
		return nil, cursorRejection()
	}
	return &history.Position{Time: decoded.Time, ID: decoded.ID}, nil
}

func cursorRejection() *HTTPError {
	return fieldRejection("cursor", "invalid_cursor",
		"the cursor is invalid or does not belong to this query")
}

func pageSizeOf(value int) (int, *HTTPError) {
	if value == 0 {
		return history.DefaultPageSize, nil
	}
	if value < 1 || value > history.MaxPageSize {
		return 0, fieldRejection("page_size", "invalid_page_size",
			"page_size must be between 1 and 100")
	}
	return value, nil
}

func canonicalUUID(value string) bool {
	if _, err := secrets.ParseUUID(value); err != nil {
		return false
	}
	return value == strings.ToLower(value)
}
