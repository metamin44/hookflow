package management

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"
)

// maxBodyBytes bounds every Phase 6 management document.
const maxBodyBytes = 1 << 20

// createDocument is the decoded endpoint-create allowlist.
type createDocument struct {
	Name           string
	Description    *string
	URL            string
	Headers        map[string]string
	HeadersPresent bool
}

// patchDocument is the decoded endpoint-patch allowlist with presence flags.
type patchDocument struct {
	Name           *string
	Description    *string
	DescriptionSet bool
	URL            *string
	Headers        map[string]string
	HeadersSet     bool
	Status         *string
}

func (p patchDocument) empty() bool {
	return p.Name == nil && !p.DescriptionSet && p.URL == nil && !p.HeadersSet && p.Status == nil
}

var createFields = map[string]struct{}{
	"name": {}, "description": {}, "url": {}, "http_headers": {},
}

var patchFields = map[string]struct{}{
	"name": {}, "description": {}, "url": {}, "http_headers": {}, "status": {},
}

// Forbidden on both Create and PATCH. signing_scheme is immutable in V1, and
// timeout_ms/max_attempts are reserved by OD-3.
var forbiddenFields = map[string]struct{}{
	"id": {}, "organization_id": {}, "secret": {}, "secret_encrypted": {},
	"signing_scheme": {}, "timeout_ms": {}, "max_attempts": {},
	"deleted_at": {}, "created_at": {}, "updated_at": {},
	"last_success_at": {}, "last_failure_at": {},
}

// organizationSubscriptionFields is the full-replacement allowlist.
var organizationSubscriptionFields = map[string]struct{}{
	"plan": {}, "status": {}, "current_period_start": {},
	"current_period_end": {}, "trial_ends_at": {},
}

func decodeObject(w http.ResponseWriter, r *http.Request) (map[string]json.RawMessage, *HTTPError) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			return nil, &HTTPError{Status: http.StatusRequestEntityTooLarge,
				Code: "payload_too_large", Detail: "request body exceeds the size limit"}
		}
		return nil, badRequest("invalid_body", "request body cannot be read")
	}
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return nil, badRequest("invalid_body", "request body must be a JSON object")
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(trimmed, &document); err != nil {
		return nil, badRequest("invalid_body", "request body must be a JSON object")
	}
	return document, nil
}

func decodeCreate(document map[string]json.RawMessage) (createDocument, *HTTPError) {
	var decoded createDocument
	for key, raw := range document {
		if _, forbidden := forbiddenFields[key]; forbidden {
			return createDocument{}, forbiddenField(key)
		}
		if _, allowed := createFields[key]; !allowed {
			return createDocument{}, unknownField(key)
		}
		switch key {
		case "name":
			value, appErr := decodeStringField(raw, "name", "invalid_name")
			if appErr != nil {
				return createDocument{}, appErr
			}
			decoded.Name = value
		case "description":
			value, appErr := decodeNullableString(raw, "description", "invalid_description")
			if appErr != nil {
				return createDocument{}, appErr
			}
			decoded.Description = value
		case "url":
			value, appErr := decodeStringField(raw, "url", "invalid_url")
			if appErr != nil {
				return createDocument{}, appErr
			}
			decoded.URL = value
		case "http_headers":
			headers, appErr := decodeHeaders(raw)
			if appErr != nil {
				return createDocument{}, appErr
			}
			decoded.Headers = headers
			decoded.HeadersPresent = true
		}
	}
	if decoded.Name == "" {
		if _, present := document["name"]; !present {
			return createDocument{}, fieldRejection("name", "invalid_name", "name is required")
		}
	}
	if _, present := document["url"]; !present {
		return createDocument{}, fieldRejection("url", "invalid_url", "url is required")
	}
	if !decoded.HeadersPresent {
		decoded.Headers = map[string]string{}
	}
	return decoded, nil
}

func decodePatch(document map[string]json.RawMessage) (patchDocument, *HTTPError) {
	var decoded patchDocument
	for key, raw := range document {
		if _, forbidden := forbiddenFields[key]; forbidden {
			return patchDocument{}, forbiddenField(key)
		}
		if _, allowed := patchFields[key]; !allowed {
			return patchDocument{}, unknownField(key)
		}
		switch key {
		case "name":
			value, appErr := decodeStringField(raw, "name", "invalid_name")
			if appErr != nil {
				return patchDocument{}, appErr
			}
			decoded.Name = &value
		case "description":
			value, appErr := decodeNullableString(raw, "description", "invalid_description")
			if appErr != nil {
				return patchDocument{}, appErr
			}
			decoded.Description = value
			decoded.DescriptionSet = true
		case "url":
			value, appErr := decodeStringField(raw, "url", "invalid_url")
			if appErr != nil {
				return patchDocument{}, appErr
			}
			decoded.URL = &value
		case "http_headers":
			headers, appErr := decodeHeaders(raw)
			if appErr != nil {
				return patchDocument{}, appErr
			}
			decoded.Headers = headers
			decoded.HeadersSet = true
		case "status":
			value, appErr := decodeStringField(raw, "status", "invalid_status")
			if appErr != nil {
				return patchDocument{}, appErr
			}
			decoded.Status = &value
		}
	}
	if decoded.empty() {
		return patchDocument{}, badRequest("empty_patch", "request must contain at least one writable field")
	}
	return decoded, nil
}

// subscriptionDocument is the decoded organization-subscription replacement.
type subscriptionDocument struct {
	Plan               string
	Status             string
	CurrentPeriodStart *time.Time
	CurrentPeriodEnd   *time.Time
	TrialEndsAt        *time.Time
}

func decodeOrganizationSubscription(document map[string]json.RawMessage) (subscriptionDocument, *HTTPError) {
	var decoded subscriptionDocument
	for key, raw := range document {
		if _, forbidden := forbiddenFields[key]; forbidden || key == "canceled_at" {
			return subscriptionDocument{}, forbiddenField(key)
		}
		if _, allowed := organizationSubscriptionFields[key]; !allowed {
			return subscriptionDocument{}, unknownField(key)
		}
		switch key {
		case "plan":
			value, appErr := decodeStringField(raw, "plan", "invalid_plan")
			if appErr != nil {
				return subscriptionDocument{}, appErr
			}
			decoded.Plan = value
		case "status":
			value, appErr := decodeStringField(raw, "status", "invalid_status")
			if appErr != nil {
				return subscriptionDocument{}, appErr
			}
			decoded.Status = value
		case "current_period_start":
			value, appErr := decodeOptionalTime(raw, "current_period_start")
			if appErr != nil {
				return subscriptionDocument{}, appErr
			}
			decoded.CurrentPeriodStart = value
		case "current_period_end":
			value, appErr := decodeOptionalTime(raw, "current_period_end")
			if appErr != nil {
				return subscriptionDocument{}, appErr
			}
			decoded.CurrentPeriodEnd = value
		case "trial_ends_at":
			value, appErr := decodeOptionalTime(raw, "trial_ends_at")
			if appErr != nil {
				return subscriptionDocument{}, appErr
			}
			decoded.TrialEndsAt = value
		}
	}
	if _, present := document["plan"]; !present {
		return subscriptionDocument{}, fieldRejection("plan", "invalid_plan", "plan is required")
	}
	if _, present := document["status"]; !present {
		return subscriptionDocument{}, fieldRejection("status", "invalid_status", "status is required")
	}
	return decoded, nil
}

func decodeOptionalTime(raw json.RawMessage, field string) (*time.Time, *HTTPError) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return nil, fieldRejection(field, "invalid_"+field, field+" must be an RFC 3339 timestamp or null")
	}
	value, err := time.Parse(time.RFC3339, text)
	if err != nil {
		return nil, fieldRejection(field, "invalid_"+field, field+" must be an RFC 3339 timestamp or null")
	}
	// PostgreSQL timestamptz stores microseconds; normalize before validation so
	// a sub-microsecond difference cannot pass the application and fail the DB
	// period check.
	normalized := value.UTC().Truncate(time.Microsecond)
	return &normalized, nil
}

func decodeStringField(raw json.RawMessage, field, code string) (string, *HTTPError) {
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", fieldRejection(field, code, field+" must be a string")
	}
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return "", fieldRejection(field, code, field+" must be a string")
	}
	return value, nil
}

func decodeNullableString(raw json.RawMessage, field, code string) (*string, *HTTPError) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, nil
	}
	value, appErr := decodeStringField(raw, field, code)
	if appErr != nil {
		return nil, appErr
	}
	return &value, nil
}

func decodeHeaders(raw json.RawMessage) (map[string]string, *HTTPError) {
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) {
		return nil, fieldRejection("http_headers", "invalid_headers", "http_headers must be an object of string values")
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return nil, fieldRejection("http_headers", "invalid_headers", "http_headers must be an object of string values")
	}
	headers := make(map[string]string, len(fields))
	for name, value := range fields {
		var text string
		if err := json.Unmarshal(value, &text); err != nil {
			return nil, fieldRejection("http_headers", "invalid_headers", "http_headers values must be strings")
		}
		headers[name] = text
	}
	return headers, nil
}

func unknownField(field string) *HTTPError {
	return fieldRejection(field, "unknown_field", "request contains an unknown field")
}

func forbiddenField(field string) *HTTPError {
	return fieldRejection(field, "forbidden_field", "field is not writable through this API")
}
