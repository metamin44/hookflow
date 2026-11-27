package management

import (
	"net/http"
	"time"

	"github.com/metamin44/hookflow/internal/modules/operations"
)

const (
	operationsPath          = organizationsPath + "/operations"
	operationsOverviewPath  = operationsPath + "/overview"
	operationsEndpointsPath = operationsPath + "/endpoints"
)

type operationWindowDocument struct {
	From    string `json:"from"`
	To      string `json:"to"`
	Seconds int64  `json:"seconds"`
}

type deliveryCountsDocument struct {
	Total       int64    `json:"total"`
	Initial     int64    `json:"initial"`
	Retry       int64    `json:"retry"`
	Pending     int64    `json:"pending"`
	Retrying    int64    `json:"retrying"`
	Succeeded   int64    `json:"succeeded"`
	Failed      int64    `json:"failed"`
	Expired     int64    `json:"expired"`
	Terminal    int64    `json:"terminal"`
	SuccessRate *float64 `json:"success_rate"`
}

type retryEffectivenessDocument struct {
	Generations int64    `json:"generations"`
	Succeeded   int64    `json:"succeeded"`
	Failed      int64    `json:"failed"`
	Expired     int64    `json:"expired"`
	InProgress  int64    `json:"in_progress"`
	Terminal    int64    `json:"terminal"`
	SuccessRate *float64 `json:"success_rate"`
}

type latencyDocument struct {
	Attempts int64   `json:"attempts"`
	AvgMS    float64 `json:"avg_ms"`
	P95MS    float64 `json:"p95_ms"`
	Sampled  bool    `json:"sampled"`
}

type operationalOverviewDocument struct {
	Window             operationWindowDocument    `json:"window"`
	PreviousWindow     operationWindowDocument    `json:"previous_window"`
	EventsAccepted     int64                      `json:"events_accepted"`
	Deliveries         deliveryCountsDocument     `json:"deliveries"`
	PreviousDeliveries deliveryCountsDocument     `json:"previous_deliveries"`
	Latency            latencyDocument            `json:"latency"`
	PreviousLatency    latencyDocument            `json:"previous_latency"`
	Retries            retryEffectivenessDocument `json:"retries"`
	ObservedAt         string                     `json:"observed_at"`
}

type endpointHealthDocument struct {
	EndpointID              string   `json:"endpoint_id"`
	Name                    string   `json:"name"`
	Status                  string   `json:"status"`
	Deliveries              int64    `json:"deliveries"`
	Succeeded               int64    `json:"succeeded"`
	Failed                  int64    `json:"failed"`
	Expired                 int64    `json:"expired"`
	InProgress              int64    `json:"in_progress"`
	Terminal                int64    `json:"terminal"`
	SuccessRate             *float64 `json:"success_rate"`
	ConsecutiveFailures     int64    `json:"consecutive_failures"`
	StreakCapped            bool     `json:"streak_capped"`
	LastSucceededAt         *string  `json:"last_succeeded_at,omitempty"`
	LastFailedAt            *string  `json:"last_failed_at,omitempty"`
	LastSucceededDeliveryID *string  `json:"last_succeeded_delivery_id,omitempty"`
	LastFailedDeliveryID    *string  `json:"last_failed_delivery_id,omitempty"`
}

type endpointHealthResponse struct {
	Window     operationWindowDocument  `json:"window"`
	Endpoints  []endpointHealthDocument `json:"endpoints"`
	Limit      int                      `json:"limit"`
	Truncated  bool                     `json:"truncated"`
	ObservedAt string                   `json:"observed_at"`
}

func (h *Handler) getOperationalOverview(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsOperationsRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	window, appErr := decodeOverviewQuery(r.URL.Query())
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	overview, err := h.service.OperationalOverview(r.Context(), identity.OrganizationID, window)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, overviewDocument(overview))
}

func (h *Handler) getOperationalEndpointHealth(w http.ResponseWriter, r *http.Request) {
	identity, scopes, ok := h.authenticate(w, r)
	if !ok || !h.sameTenant(w, r, identity) {
		return
	}
	if !scopes.AllowsOperationsRead() {
		h.writeError(w, r, insufficientScope())
		return
	}
	window, limit, appErr := decodeEndpointHealthQuery(r.URL.Query())
	if appErr != nil {
		h.writeError(w, r, appErr)
		return
	}
	observed, records, truncated, err := h.service.OperationalEndpointHealth(r.Context(),
		identity.OrganizationID, window, limit)
	if err != nil {
		h.writeError(w, r, err)
		return
	}
	response := endpointHealthResponse{
		Window:     windowDocument(observed),
		Endpoints:  make([]endpointHealthDocument, 0, len(records)),
		Limit:      limit,
		Truncated:  truncated,
		ObservedAt: rfc3339(observed.To),
	}
	for _, record := range records {
		response.Endpoints = append(response.Endpoints, endpointHealthOf(record))
	}
	writeJSON(w, http.StatusOK, response)
}

func overviewDocument(overview operations.Overview) operationalOverviewDocument {
	return operationalOverviewDocument{
		Window:             windowDocument(overview.Window),
		PreviousWindow:     windowDocument(overview.Previous),
		EventsAccepted:     overview.EventsAccepted,
		Deliveries:         deliveryCountsOf(overview.Deliveries),
		PreviousDeliveries: deliveryCountsOf(overview.PreviousDeliveries),
		Latency:            latencyOf(overview.Latency),
		PreviousLatency:    latencyOf(overview.PreviousLatency),
		Retries:            retriesOf(overview.Retries),
		ObservedAt:         rfc3339(overview.Window.To),
	}
}

func windowDocument(window operations.Window) operationWindowDocument {
	return operationWindowDocument{
		From:    rfc3339(window.From),
		To:      rfc3339(window.To),
		Seconds: int64(window.Duration() / time.Second),
	}
}

func deliveryCountsOf(counts operations.DeliveryCounts) deliveryCountsDocument {
	return deliveryCountsDocument{
		Total:       counts.Total,
		Initial:     counts.Initial,
		Retry:       counts.Retry,
		Pending:     counts.Pending,
		Retrying:    counts.Retrying,
		Succeeded:   counts.Succeeded,
		Failed:      counts.Failed,
		Expired:     counts.Expired,
		Terminal:    counts.Terminal(),
		SuccessRate: counts.SuccessRate(),
	}
}

func retriesOf(retries operations.RetryEffectiveness) retryEffectivenessDocument {
	return retryEffectivenessDocument{
		Generations: retries.Generations,
		Succeeded:   retries.Succeeded,
		Failed:      retries.Failed,
		Expired:     retries.Expired,
		InProgress:  retries.InProgress,
		Terminal:    retries.Terminal(),
		SuccessRate: retries.SuccessRate(),
	}
}

func latencyOf(latency operations.Latency) latencyDocument {
	return latencyDocument{
		Attempts: latency.Attempts,
		AvgMS:    latency.AvgMS,
		P95MS:    latency.P95MS,
		Sampled:  latency.Sampled,
	}
}

func endpointHealthOf(record operations.EndpointHealth) endpointHealthDocument {
	return endpointHealthDocument{
		EndpointID:              record.ID,
		Name:                    record.Name,
		Status:                  record.Status,
		Deliveries:              record.Deliveries,
		Succeeded:               record.Succeeded,
		Failed:                  record.Failed,
		Expired:                 record.Expired,
		InProgress:              record.InProgress,
		Terminal:                record.Terminal(),
		SuccessRate:             record.SuccessRate(),
		ConsecutiveFailures:     record.ConsecutiveFailures,
		StreakCapped:            record.StreakCapped,
		LastSucceededAt:         timeOrNil(record.LastSucceededAt),
		LastFailedAt:            timeOrNil(record.LastFailedAt),
		LastSucceededDeliveryID: record.LastSucceededDeliveryID,
		LastFailedDeliveryID:    record.LastFailedDeliveryID,
	}
}

func rfc3339(value time.Time) string { return value.UTC().Format(time.RFC3339) }

func timeOrNil(value *time.Time) *string {
	if value == nil {
		return nil
	}
	rendered := rfc3339(*value)
	return &rendered
}
