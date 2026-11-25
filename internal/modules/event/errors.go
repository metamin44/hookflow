package event

// Approved ingestion rejection codes (overview §5.5, U42).
const (
	CodeInvalidDocument       = "invalid_json_document"
	CodeDuplicateKey          = "duplicate_json_key"
	CodeIntegerOutOfSafeRange = "integer_out_of_safe_range"
	CodeNonFiniteNumber       = "non_finite_number"
	CodeInvalidEventType      = "invalid_event_type"
	CodeUnknownField          = "unknown_field"
)

// Static detail text, one per reason. U42 fixes `detail` to static text, so
// these constants never interpolate request content; Field names an envelope
// member, never a client value.
const (
	detailInvalidDocument  = "request body must be a single JSON object with a string event_type and an object payload"
	detailDuplicateKey     = "JSON object member names must be unique"
	detailSafeRange        = "number is outside the safe integer range"
	detailNonFinite        = "number is not finite"
	detailEventTypeMissing = "event_type is required"
	detailEventTypeString  = "event_type must be a string"
	detailEventTypeSyntax  = "event_type must be 1 to 160 lowercase characters from a-z, 0-9, '_', '.' and '-'"
	detailUnknownField     = "request contains an unknown field"
)

// Rejection is a static ingestion rejection. The HTTP layer maps Code, Detail
// and Field to an RFC 9457 problem document; the wrapped cause keeps the
// parser's JSON pointer available for server-side logs only.
type Rejection struct {
	Code   string
	Detail string
	Field  string
	cause  error
}

func (r *Rejection) Error() string { return r.Code }

// Unwrap exposes the parser error. jcs.Pointer(err) recovers the member
// position for logs; that position must never be echoed to a client.
func (r *Rejection) Unwrap() error { return r.cause }

// InvalidDocument rejects malformed JSON, a non-object document, a missing or
// non-object payload, or trailing content after the first value. field is
// empty unless the rejected member is known to be payload.
func InvalidDocument(field string) *Rejection {
	return &Rejection{Code: CodeInvalidDocument, Detail: detailInvalidDocument, Field: field}
}

// DuplicateKey rejects a JSON object that repeats a member name.
func DuplicateKey() *Rejection {
	return &Rejection{Code: CodeDuplicateKey, Detail: detailDuplicateKey}
}

// IntegerOutOfSafeRange rejects an integer value outside ±(2^53−1), including
// the G1 case of a fractional literal that collapses onto an unsafe integer.
func IntegerOutOfSafeRange() *Rejection {
	return &Rejection{Code: CodeIntegerOutOfSafeRange, Detail: detailSafeRange}
}

// NonFiniteNumber rejects a value that is not representable as a finite
// IEEE 754 binary64 number.
func NonFiniteNumber() *Rejection {
	return &Rejection{Code: CodeNonFiniteNumber, Detail: detailNonFinite}
}

// MissingEventType rejects an envelope without a top-level event_type.
func MissingEventType() *Rejection {
	return &Rejection{Code: CodeInvalidEventType, Detail: detailEventTypeMissing, Field: "event_type"}
}

// TypeNotString rejects a non-string event_type.
func TypeNotString() *Rejection {
	return &Rejection{Code: CodeInvalidEventType, Detail: detailEventTypeString, Field: "event_type"}
}

// InvalidEventType rejects an event_type that fails the routing-key syntax.
func InvalidEventType() *Rejection {
	return &Rejection{Code: CodeInvalidEventType, Detail: detailEventTypeSyntax, Field: "event_type"}
}

// UnknownField rejects any top-level member other than event_type and payload.
func UnknownField(member string) *Rejection {
	return &Rejection{Code: CodeUnknownField, Detail: detailUnknownField, Field: member}
}
