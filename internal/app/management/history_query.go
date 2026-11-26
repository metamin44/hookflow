package management

import (
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/metamin44/hookflow/internal/modules/history"
	"github.com/metamin44/hookflow/internal/modules/subscription"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// maxCursorBytes bounds the opaque cursor before decoding.
const maxCursorBytes = 2048

var eventFilterKeys = map[string]bool{
	"type":          true,
	"endpoint_id":   true,
	"received_from": true,
	"received_to":   true,
	"cursor":        true,
	"page_size":     true,
}

var deliveryFilterKeys = map[string]bool{
	"endpoint_id":  true,
	"state":        true,
	"created_from": true,
	"created_to":   true,
	"cursor":       true,
	"page_size":    true,
}

var attemptFilterKeys = map[string]bool{
	"cursor":    true,
	"page_size": true,
}

func decodeEventListQuery(values url.Values) (EventListQuery, *HTTPError) {
	var query EventListQuery
	if appErr := validateQueryKeys(values, eventFilterKeys); appErr != nil {
		return query, appErr
	}
	query.Type = values.Get("type")
	if values.Has("type") && !subscription.ValidateEventType(query.Type) {
		return query, fieldRejection("type", "invalid_event_type",
			"type must be 1 to 160 lowercase characters from a-z, 0-9, '_', '.' and '-'")
	}
	var appErr *HTTPError
	if query.EndpointID, appErr = decodeUUIDFilter(values, "endpoint_id"); appErr != nil {
		return query, appErr
	}
	if query.ReceivedFrom, appErr = decodeTimeFilter(values, "received_from"); appErr != nil {
		return query, appErr
	}
	if query.ReceivedTo, appErr = decodeTimeFilter(values, "received_to"); appErr != nil {
		return query, appErr
	}
	if appErr = validateWindow(query.ReceivedFrom, query.ReceivedTo, "received_from", "received_to"); appErr != nil {
		return query, appErr
	}
	if query.Cursor, appErr = decodeCursorFilter(values); appErr != nil {
		return query, appErr
	}
	query.PageSize, appErr = decodePageSize(values)
	if appErr != nil {
		return query, appErr
	}
	return query, nil
}

func decodeDeliveryListQuery(values url.Values) (DeliveryListQuery, *HTTPError) {
	var query DeliveryListQuery
	if appErr := validateQueryKeys(values, deliveryFilterKeys); appErr != nil {
		return query, appErr
	}
	var appErr *HTTPError
	if query.EndpointID, appErr = decodeUUIDFilter(values, "endpoint_id"); appErr != nil {
		return query, appErr
	}
	query.State = values.Get("state")
	if values.Has("state") && !history.IsState(query.State) {
		return query, fieldRejection("state", "invalid_state",
			"state must be one of pending, retrying, succeeded, failed, expired")
	}
	if query.CreatedFrom, appErr = decodeTimeFilter(values, "created_from"); appErr != nil {
		return query, appErr
	}
	if query.CreatedTo, appErr = decodeTimeFilter(values, "created_to"); appErr != nil {
		return query, appErr
	}
	if appErr = validateWindow(query.CreatedFrom, query.CreatedTo, "created_from", "created_to"); appErr != nil {
		return query, appErr
	}
	if query.Cursor, appErr = decodeCursorFilter(values); appErr != nil {
		return query, appErr
	}
	query.PageSize, appErr = decodePageSize(values)
	if appErr != nil {
		return query, appErr
	}
	return query, nil
}

func decodeAttemptListQuery(values url.Values) (string, int, *HTTPError) {
	if appErr := validateQueryKeys(values, attemptFilterKeys); appErr != nil {
		return "", 0, appErr
	}
	cursor, appErr := decodeCursorFilter(values)
	if appErr != nil {
		return "", 0, appErr
	}
	pageSize, appErr := decodePageSize(values)
	if appErr != nil {
		return "", 0, appErr
	}
	return cursor, pageSize, nil
}

func validateQueryKeys(values url.Values, allowed map[string]bool) *HTTPError {
	for key, entries := range values {
		if !allowed[key] {
			return fieldRejection(key, "unknown_filter", "the filter is not supported by this route")
		}
		if len(entries) > 1 {
			return fieldRejection(key, "invalid_filter", "the filter may appear only once")
		}
	}
	return nil
}

func decodeUUIDFilter(values url.Values, field string) (string, *HTTPError) {
	if !values.Has(field) {
		return "", nil
	}
	value := values.Get(field)
	if _, err := secrets.ParseUUID(value); err != nil {
		return "", fieldRejection(field, "invalid_"+field, field+" must be a canonical UUID")
	}
	if value != "" && value != strings.ToLower(value) {
		return "", fieldRejection(field, "invalid_"+field, field+" must be a canonical UUID")
	}
	return value, nil
}

func decodeTimeFilter(values url.Values, field string) (*time.Time, *HTTPError) {
	if !values.Has(field) {
		return nil, nil
	}
	value := values.Get(field)
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return nil, fieldRejection(field, "invalid_time_window",
			field+" must be an RFC 3339 timestamp")
	}
	parsed = parsed.UTC().Truncate(time.Microsecond)
	return &parsed, nil
}

func validateWindow(from, to *time.Time, fromField, toField string) *HTTPError {
	if from == nil || to == nil {
		return nil
	}
	if !from.Before(*to) {
		return fieldRejection(toField, "invalid_time_window",
			fromField+" must be earlier than "+toField)
	}
	return nil
}

func decodeCursorFilter(values url.Values) (string, *HTTPError) {
	if !values.Has("cursor") {
		return "", nil
	}
	value := values.Get("cursor")
	if value == "" || len(value) > maxCursorBytes {
		return "", cursorRejection()
	}
	return value, nil
}

func decodePageSize(values url.Values) (int, *HTTPError) {
	if !values.Has("page_size") {
		return history.DefaultPageSize, nil
	}
	value := values.Get("page_size")
	size, err := strconv.Atoi(value)
	if err != nil || size < 1 || size > history.MaxPageSize {
		return 0, fieldRejection("page_size", "invalid_page_size",
			"page_size must be an integer between 1 and 100")
	}
	return size, nil
}
