package management

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
	"unicode"
	"unicode/utf8"

	eventapp "github.com/metamin44/hookflow/internal/app/event"
	"github.com/metamin44/hookflow/internal/modules/event"
	"github.com/metamin44/hookflow/internal/modules/organization"
)

const (
	eventsPath = "/v1/events"

	// maxIngestBodyBytes is the fixed approved ingestion cap (OD-5). It is a
	// contract, not configuration.
	maxIngestBodyBytes = 1 << 20

	// maxIdempotencyKeyBytes bounds the Idempotency-Key header value.
	maxIdempotencyKeyBytes = 200
)

// EventAcceptor is the ingestion use case. *eventapp.Service implements it.
type EventAcceptor interface {
	AcceptEvent(ctx context.Context, input eventapp.AcceptInput) (eventapp.Result, error)
}

// ingestResponse is the only 202 body. Field order is the approved contract and
// is produced by a single encoder for first accept and replay alike.
type ingestResponse struct {
	EventID          string `json:"event_id"`
	DestinationCount int    `json:"destination_count"`
	ReceivedAt       string `json:"received_at"`
}

func (h *Handler) ingestEvent(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsEventsWrite() {
		h.writeError(w, r, insufficientScope())
		return
	}
	if appErr := organizationWriteAllowed(identity.OrganizationStatus); appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	key, appErr := idempotencyKeyHeader(r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	body, appErr := readIngestBody(w, r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.events.AcceptEvent(r.Context(), eventapp.AcceptInput{
		OrganizationID: identity.OrganizationID,
		IdempotencyKey: key,
		Body:           body,
	})
	if err != nil {
		h.writeError(w, r, ingestError(err))
		return
	}
	writeIngestResponse(w, result)
}

// organizationWriteAllowed rejects suspended and archived tenants before the
// body is parsed. The transaction rechecks the state under a share lock.
func organizationWriteAllowed(status string) *HTTPError {
	switch status {
	case organization.StatusActive:
		return nil
	case organization.StatusSuspended:
		return forbidden("organization_suspended", "suspended organizations cannot accept events")
	default:
		return forbidden("organization_archived", "archived organizations cannot accept events")
	}
}

func idempotencyKeyHeader(r *http.Request) (string, *HTTPError) {
	values := r.Header.Values("Idempotency-Key")
	switch {
	case len(values) == 0:
		return "", badRequest("missing_idempotency_key", "exactly one Idempotency-Key header is required")
	case len(values) > 1:
		return "", badRequest("invalid_idempotency_key", "exactly one Idempotency-Key header is allowed")
	}
	value := values[0]
	if value == "" {
		return "", badRequest("missing_idempotency_key", "exactly one Idempotency-Key header is required")
	}
	if len(value) > maxIdempotencyKeyBytes || !utf8.ValidString(value) || containsControl(value) {
		return "", badRequest("invalid_idempotency_key", "the Idempotency-Key value is not acceptable")
	}
	return value, nil
}

func containsControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// readIngestBody reads at most maxIngestBodyBytes+1 bytes. The extra byte
// proves oversize without buffering the rest; the connection is then closed so
// an unread body cannot be reused.
func readIngestBody(w http.ResponseWriter, r *http.Request) ([]byte, *HTTPError) {
	if r.ContentLength > maxIngestBodyBytes {
		w.Header().Set("Connection", "close")
		return nil, ingestTooLarge()
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, maxIngestBodyBytes+1))
	if err != nil {
		return nil, badRequest("invalid_body", "request body cannot be read")
	}
	if len(body) > maxIngestBodyBytes {
		w.Header().Set("Connection", "close")
		return nil, ingestTooLarge()
	}
	return body, nil
}

func ingestTooLarge() *HTTPError {
	return badRequest("payload_too_large", "request body exceeds the size limit")
}

// ingestError maps application outcomes to the approved error contract. Unknown
// errors stay unwrapped so writeError renders a generic 500 and logs the type.
func ingestError(err error) error {
	var rejection *event.Rejection
	if errors.As(err, &rejection) {
		return &HTTPError{
			Status: http.StatusBadRequest,
			Code:   rejection.Code,
			Detail: rejection.Detail,
			Field:  rejection.Field,
		}
	}
	var inactive *eventapp.InactiveOrganizationError
	if errors.As(err, &inactive) {
		if inactive.Status == organization.StatusSuspended {
			return forbidden("organization_suspended", "suspended organizations cannot accept events")
		}
		return forbidden("organization_archived", "archived organizations cannot accept events")
	}
	if errors.Is(err, eventapp.ErrIdempotencyConflict) {
		return conflict("idempotency_key_conflict",
			"the idempotency key was already used with a different request body")
	}
	return err
}

// writeIngestResponse emits the approved 202 body. Replay uses the stored
// result, so the bytes match the first acceptance exactly.
func writeIngestResponse(w http.ResponseWriter, result eventapp.Result) {
	if result.Replayed {
		w.Header().Set("HookFlow-Idempotent-Replay", "true")
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(ingestResponse{
		EventID:          result.EventID,
		DestinationCount: result.DestinationCount,
		ReceivedAt:       result.ReceivedAt.UTC().Format(time.RFC3339),
	})
}
