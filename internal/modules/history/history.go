package history

import (
	"encoding/json"
	"time"
)

// Page-size contract.
const (
	DefaultPageSize = 50
	MaxPageSize     = 100

	// DiagnosticLimit is the approved display bound for stored diagnostics.
	DiagnosticLimit = 500

	// MaxEventDeliveries bounds the inline delivery summaries of one event.
	MaxEventDeliveries = 100
)

// Delivery states accepted by chk_deliveries_state. These are the only values
// the API echoes or accepts as a filter.
const (
	StatePending   = "pending"
	StateRetrying  = "retrying"
	StateSucceeded = "succeeded"
	StateFailed    = "failed"
	StateExpired   = "expired"
)

// IsState reports whether value is a stored delivery state.
func IsState(value string) bool {
	switch value {
	case StatePending, StateRetrying, StateSucceeded, StateFailed, StateExpired:
		return true
	default:
		return false
	}
}

// Counts is the delivery summary of one event.
type Counts struct {
	Total int `json:"total"`
	// InitialTotal and RetryTotal split the same rows as Total into the
	// generation-1 fan-out and manual retry generations. Total remains the sum
	// of every delivery row, so Phase 9 responses stay additive-compatible.
	InitialTotal int `json:"initial_total"`
	RetryTotal   int `json:"retry_total"`
	Pending      int `json:"pending"`
	Retrying     int `json:"retrying"`
	Succeeded    int `json:"succeeded"`
	Failed       int `json:"failed"`
	Expired      int `json:"expired"`
}

// EventSummary is one event list row. It never carries the payload.
type EventSummary struct {
	ID              string    `json:"id"`
	Type            string    `json:"type"`
	ReceivedAt      time.Time `json:"received_at"`
	CreatedAt       time.Time `json:"created_at"`
	DeliverySummary Counts    `json:"delivery_summary"`
}

// EventDetail adds the stored payload and bounded delivery summaries.
type EventDetail struct {
	EventSummary
	Payload             json.RawMessage   `json:"payload"`
	DestinationCount    int               `json:"destination_count"`
	Deliveries          []DeliverySummary `json:"deliveries"`
	DeliveriesTruncated bool              `json:"deliveries_truncated,omitempty"`
}

// DeliverySummary is the bounded delivery projection shared by list, event
// detail and delivery detail. InProgress is derived from the worker lease and
// is never a stored state.
type DeliverySummary struct {
	ID                 string     `json:"id"`
	EventID            string     `json:"event_id"`
	EndpointID         string     `json:"endpoint_id"`
	State              string     `json:"state"`
	AttemptCount       int        `json:"attempt_count"`
	InProgress         bool       `json:"in_progress"`
	Generation         int        `json:"generation"`
	ParentDeliveryID   *string    `json:"parent_delivery_id"`
	CreatedAt          time.Time  `json:"created_at"`
	NextAttemptAt      *time.Time `json:"next_attempt_at"`
	LastAttemptAt      *time.Time `json:"last_attempt_at"`
	CompletedAt        *time.Time `json:"completed_at"`
	LastResponseStatus *int       `json:"last_response_status"`
	LeaseExpiresAt     *time.Time `json:"lease_expires_at,omitempty"`
	Diagnostic         string     `json:"diagnostic,omitempty"`
}

// DeliveryDetail adds the owning event metadata and the row update time.
type DeliveryDetail struct {
	DeliverySummary
	EventType string    `json:"event_type"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Attempt is one stored delivery attempt. Receiver headers and response-body
// snippets are never projected.
type Attempt struct {
	AttemptNumber  int       `json:"attempt_number"`
	Outcome        string    `json:"outcome"`
	ResponseStatus *int      `json:"response_status"`
	ErrorClass     *string   `json:"error_class"`
	ErrorMessage   string    `json:"error_message,omitempty"`
	DurationMS     *int      `json:"duration_ms"`
	StartedAt      time.Time `json:"started_at"`
	FinishedAt     time.Time `json:"finished_at"`
}

// EventFilter is the validated event list context.
type EventFilter struct {
	Type         string
	EndpointID   string
	ReceivedFrom *time.Time
	ReceivedTo   *time.Time
}

// Fingerprint renders the canonical cursor binding of this filter.
func (f EventFilter) Fingerprint() string {
	return Fingerprint(CursorEvents, f.Type, f.EndpointID, timeValue(f.ReceivedFrom), timeValue(f.ReceivedTo))
}

// DeliveryFilter is the validated delivery list context.
type DeliveryFilter struct {
	EndpointID  string
	State       string
	CreatedFrom *time.Time
	CreatedTo   *time.Time
}

// Fingerprint renders the canonical cursor binding of this filter.
func (f DeliveryFilter) Fingerprint() string {
	return Fingerprint(CursorDeliveries, f.EndpointID, f.State, timeValue(f.CreatedFrom), timeValue(f.CreatedTo))
}

// Position is a keyset position on (time, id).
type Position struct {
	Time time.Time
	ID   string
}
