package management

import (
	"net/http"

	"github.com/metamin44/hookflow/internal/modules/history"
)

const (
	eventPath      = eventsPath + "/{event_id}"
	deliveriesPath = "/v1/deliveries"
	deliveryPath   = deliveriesPath + "/{delivery_id}"
	attemptsPath   = deliveryPath + "/attempts"
)

type eventsListResponse struct {
	Events     []history.EventSummary `json:"events"`
	NextCursor string                 `json:"next_cursor,omitempty"`
}

type deliveriesListResponse struct {
	Deliveries []history.DeliverySummary `json:"deliveries"`
	NextCursor string                    `json:"next_cursor,omitempty"`
}

type attemptsListResponse struct {
	Attempts   []history.Attempt `json:"attempts"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

func (h *Handler) listEvents(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsDeliveriesRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	query, appErr := decodeEventListQuery(r.URL.Query())
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.service.ListEvents(r.Context(), identity.OrganizationID, query, cursorKey(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, eventsListResponse(result))
}

func (h *Handler) getEvent(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsDeliveriesRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	eventID := r.PathValue("event_id")
	if !pathUUID(eventID) {
		h.writeError(w, r, notFound())
		return
	}
	detail, err := h.service.GetEvent(r.Context(), identity.OrganizationID, eventID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) listDeliveries(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsDeliveriesRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	query, appErr := decodeDeliveryListQuery(r.URL.Query())
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.service.ListDeliveries(r.Context(), identity.OrganizationID, query, cursorKey(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, deliveriesListResponse(result))
}

func (h *Handler) getDelivery(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsDeliveriesRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	deliveryID := r.PathValue("delivery_id")
	if !pathUUID(deliveryID) {
		h.writeError(w, r, notFound())
		return
	}
	detail, err := h.service.GetDelivery(r.Context(), identity.OrganizationID, deliveryID)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

func (h *Handler) listDeliveryAttempts(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsDeliveriesRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	deliveryID := r.PathValue("delivery_id")
	if !pathUUID(deliveryID) {
		h.writeError(w, r, notFound())
		return
	}
	cursor, pageSize, appErr := decodeAttemptListQuery(r.URL.Query())
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.service.ListDeliveryAttempts(r.Context(), identity.OrganizationID, deliveryID,
		cursor, pageSize, cursorKey(r))
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, attemptsListResponse(result))
}

func cursorKey(r *http.Request) []byte {
	token, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		return nil
	}
	return history.KeyFromToken(token)
}

func pathUUID(value string) bool {
	return value != "" && canonicalUUID(value)
}
