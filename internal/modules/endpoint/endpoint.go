// Package endpoint implements HookFlow delivery destinations: the public
// representation, request validation, the Contract A strong ETag and the
// endpoints repository.
package endpoint

import "time"

// Live statuses.
const (
	StatusActive = "active"
	StatusPaused = "paused"
)

// SigningScheme is the immutable V1 protocol constant.
const SigningScheme = "hmac_sha256"

// Record is one endpoints row with its encrypted secret. The secret envelope
// is never exposed through the API representation.
type Record struct {
	ID              string
	OrganizationID  string
	Name            string
	Description     *string
	URL             string
	SecretEncrypted string
	SigningScheme   string
	HTTPHeaders     map[string]string
	Status          string
	DeletedAt       *time.Time
}

// Representation is the complete endpoint document used by Contract A ETags.
// It deliberately excludes organization_id, secrets, timestamps, metrics,
// timeout_ms and max_attempts (OD-3). API handlers must use RedactedRepresent.
type Representation struct {
	ID            string            `json:"id"`
	Name          string            `json:"name"`
	Description   *string           `json:"description"`
	URL           string            `json:"url"`
	Status        string            `json:"status"`
	SigningScheme string            `json:"signing_scheme"`
	HTTPHeaders   map[string]string `json:"http_headers"`
}

// Represent builds the public document from a stored row.
func (r Record) Represent() Representation {
	headers := make(map[string]string, len(r.HTTPHeaders))
	for name, value := range r.HTTPHeaders {
		headers[name] = value
	}
	return Representation{
		ID:            r.ID,
		Name:          r.Name,
		Description:   r.Description,
		URL:           r.URL,
		Status:        r.Status,
		SigningScheme: r.SigningScheme,
		HTTPHeaders:   headers,
	}
}

// RedactedRepresent builds the API-facing document without mutating the stored
// record. ETags must be calculated from Represent, not from this redacted view.
func (r Record) RedactedRepresent() Representation {
	representation := r.Represent()
	representation.HTTPHeaders = SafeHeaders(r.HTTPHeaders)
	return representation
}
