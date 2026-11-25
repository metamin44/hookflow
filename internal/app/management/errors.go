package management

import (
	"errors"
	"net/http"
)

// HTTPError is a stable API rejection rendered as RFC 9457 Problem Details.
// Detail text is static except for validated field names; it never carries
// request values, secrets, SQL or provider output.
type HTTPError struct {
	Status  int
	Code    string
	Detail  string
	Field   string
	ETag    string
	Headers map[string]string
}

func (e *HTTPError) Error() string { return e.Code }

func badRequest(code, detail string) *HTTPError {
	return &HTTPError{Status: http.StatusBadRequest, Code: code, Detail: detail}
}

func fieldRejection(field, code, detail string) *HTTPError {
	return &HTTPError{Status: http.StatusBadRequest, Code: code, Detail: detail, Field: field}
}

// unauthorized renders the RFC 6750 Bearer challenge. The challenge carries no
// error code or description, so a missing, malformed, unknown, revoked, or
// expired key is indistinguishable to the client.
func unauthorized(code, detail string) *HTTPError {
	return &HTTPError{
		Status:  http.StatusUnauthorized,
		Code:    code,
		Detail:  detail,
		Headers: map[string]string{"WWW-Authenticate": "Bearer"},
	}
}

func forbidden(code, detail string) *HTTPError {
	return &HTTPError{Status: http.StatusForbidden, Code: code, Detail: detail}
}

func notFound() *HTTPError {
	return &HTTPError{Status: http.StatusNotFound, Code: "not_found", Detail: "resource not found"}
}

func conflict(code, detail string) *HTTPError {
	return &HTTPError{Status: http.StatusConflict, Code: code, Detail: detail}
}

func preconditionFailed(etag string) *HTTPError {
	return &HTTPError{
		Status: http.StatusPreconditionFailed,
		Code:   "precondition_failed",
		Detail: "the If-Match validator does not match the current representation",
		ETag:   etag,
	}
}

func preconditionRequired() *HTTPError {
	return &HTTPError{
		Status: http.StatusPreconditionRequired,
		Code:   "precondition_required",
		Detail: "this request requires an If-Match header",
	}
}

func methodNotAllowed(allow string) *HTTPError {
	return &HTTPError{
		Status:  http.StatusMethodNotAllowed,
		Code:    "method_not_allowed",
		Detail:  "the method is not supported for this resource",
		Headers: map[string]string{"Allow": allow},
	}
}

func internalError() *HTTPError {
	return &HTTPError{Status: http.StatusInternalServerError, Code: "internal_error", Detail: "internal server error"}
}

func providerUnavailable() *HTTPError {
	return &HTTPError{
		Status: http.StatusServiceUnavailable,
		Code:   "secret_provider_unavailable",
		Detail: "the secret provider is temporarily unavailable",
	}
}

var (
	errMissingIfMatch = errors.New("if-match header is required")
	errInvalidIfMatch = errors.New("if-match header is malformed")
	errStaleIfMatch   = errors.New("if-match validator does not match")
)
