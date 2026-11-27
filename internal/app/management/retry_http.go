package management

import (
	"net/http"
	"time"
)

const retryPath = deliveryPath + "/retry"

type retryDeliveryResponse struct {
	ID               string    `json:"id"`
	EventID          string    `json:"event_id"`
	EndpointID       string    `json:"endpoint_id"`
	Generation       int       `json:"generation"`
	ParentDeliveryID *string   `json:"parent_delivery_id"`
	State            string    `json:"state"`
	AttemptCount     int       `json:"attempt_count"`
	CreatedAt        time.Time `json:"created_at"`
}

func (h *Handler) retryDelivery(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !scopes.AllowsDeliveriesRetry() {
		h.writeError(w, r, insufficientScope())
		return
	}
	deliveryID := r.PathValue("delivery_id")
	if !pathUUID(deliveryID) {
		h.writeError(w, r, notFound())
		return
	}
	key, appErr := idempotencyKeyHeader(r)
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	result, err := h.service.RetryDelivery(r.Context(), identity, requestMeta(r), deliveryID, key)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	status := http.StatusCreated
	if result.Replayed {
		status = http.StatusOK
		w.Header().Set("HookFlow-Idempotent-Replay", "true")
	}
	w.Header().Set("Location", deliveriesPath+"/"+result.ID)
	writeJSON(w, status, retryDeliveryResponse{
		ID: result.ID, EventID: result.EventID, EndpointID: result.EndpointID,
		Generation: result.Generation, ParentDeliveryID: result.ParentDeliveryID,
		State: result.State, AttemptCount: result.AttemptCount, CreatedAt: result.CreatedAt,
	})
}
