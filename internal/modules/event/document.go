// Package event implements the Event domain: ingestion-document validation,
// the U41 canonical fingerprint, persistence and exact destination matching.
//
// The package never decodes a document into map[string]interface{}, never
// hashes raw request bytes, and never treats ordinary JSON marshaling as a
// substitute for RFC 8785 canonicalization.
package event

import (
	"bytes"
	"crypto/sha256"
	"errors"

	"encoding/json/jsontext"

	"github.com/metamin44/hookflow/internal/modules/subscription"
	"github.com/metamin44/hookflow/internal/platform/jcs"
)

// Envelope member names. No other top-level member is accepted.
const (
	memberEventType = "event_type"
	memberPayload   = "payload"
)

// InexactNumberMetric is the approved observability counter incremented once
// per accepted number whose canonical decimal differs from the literal's
// decimal value. It is never a response field, never a rejection and carries
// no threshold (U48, overview §15.3 stage 8).
const InexactNumberMetric = "hookflow_ingest_number_inexact_total"

// Document is a validated ingestion envelope.
//
// Payload holds the validated raw JSON object bytes exactly as received
// (Decoder.ReadValue), which is the U41 input; it is deliberately not a
// decoded value or a database representation. Build documents with Parse: the
// zero Document is not a valid envelope.
type Document struct {
	EventType string
	Payload   []byte
	// InexactNumbers counts accepted-but-coarsened number literals for the
	// observability counter only.
	InexactNumbers int
}

// Parse validates body as one I-JSON object and applies, in the approved
// order, duplicate-name rejection, literal-level integer-range validation,
// binary64 conversion, non-finite rejection, guard G1, and the envelope shape
// rules (a string event_type with the events.type syntax and an object
// payload). Rejections are *Rejection values carrying the approved §5.5 code.
func Parse(body []byte) (Document, error) {
	stats, err := jcs.Validate(body)
	if err != nil {
		return Document{}, mapParserError(err)
	}
	document, err := parseEnvelope(body)
	if err != nil {
		return Document{}, err
	}
	document.InexactNumbers = stats.InexactNumbers
	return document, nil
}

// parseEnvelope reads event_type and the raw payload bytes. It runs after
// jcs.Validate, so syntax, duplicate names, depth and numeric rules are
// already settled.
func parseEnvelope(body []byte) (Document, error) {
	decoder := jsontext.NewDecoder(bytes.NewReader(body))
	opening, err := decoder.ReadToken()
	if err != nil || opening.Kind() != jsontext.KindBeginObject {
		return Document{}, InvalidDocument("")
	}

	var (
		document      Document
		eventTypeSeen bool
		payloadSeen   bool
	)
	for decoder.PeekKind() != jsontext.KindEndObject {
		name, err := decoder.ReadToken()
		if err != nil {
			return Document{}, InvalidDocument("")
		}
		switch name.String() {
		case memberEventType:
			eventTypeSeen = true
			value, err := decoder.ReadToken()
			if err != nil {
				return Document{}, InvalidDocument("")
			}
			if value.Kind() != jsontext.KindString {
				return Document{}, TypeNotString()
			}
			if !subscription.ValidateEventType(value.String()) {
				return Document{}, InvalidEventType()
			}
			document.EventType = value.String()
		case memberPayload:
			payloadSeen = true
			value, err := decoder.ReadValue()
			if err != nil {
				return Document{}, InvalidDocument("payload")
			}
			if value.Kind() != jsontext.KindBeginObject {
				return Document{}, InvalidDocument("payload")
			}
			// ReadValue bytes are only valid until the next decoder call.
			document.Payload = append([]byte(nil), value...)
		default:
			return Document{}, UnknownField(name.String())
		}
	}
	if _, err := decoder.ReadToken(); err != nil {
		return Document{}, InvalidDocument("")
	}
	if !eventTypeSeen {
		return Document{}, MissingEventType()
	}
	if !payloadSeen {
		return Document{}, InvalidDocument("payload")
	}
	return document, nil
}

// Canonical returns the RFC 8785 canonical UTF-8 bytes of the U41 wrapper
// {"event_type":<encoded string>,"payload":<validated raw payload>}. The
// event type goes through a real JSON string encoder.
func (d Document) Canonical() ([]byte, error) {
	input, err := d.fingerprintInput()
	if err != nil {
		return nil, err
	}
	return jcs.Canonicalize(input)
}

// Fingerprint returns SHA-256 over the canonical wrapper bytes, the U41
// request_hash. It is computed before any transaction is opened.
func (d Document) Fingerprint() ([32]byte, error) {
	canonical, err := d.Canonical()
	if err != nil {
		return [32]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}

// fingerprintInput builds {"event_type":<encoded>,"payload":<raw>} without
// ever concatenating an unescaped string.
func (d Document) fingerprintInput() ([]byte, error) {
	input := make([]byte, 0, len(d.Payload)+len(d.EventType)+24)
	input = append(input, `{"event_type":`...)
	input, err := jcs.AppendString(input, d.EventType)
	if err != nil {
		return nil, err
	}
	input = append(input, `,"payload":`...)
	input = append(input, d.Payload...)
	input = append(input, '}')
	return input, nil
}

// mapParserError translates a jcs error into the approved rejection, keeping
// the parser error as the cause so jcs.Pointer(err) still yields the position
// for logs.
func mapParserError(err error) error {
	var rejection *Rejection
	switch {
	case errors.Is(err, jcs.ErrDuplicateName):
		rejection = DuplicateKey()
	case errors.Is(err, jcs.ErrIntegerOutOfSafeRange):
		rejection = IntegerOutOfSafeRange()
	case errors.Is(err, jcs.ErrNonFiniteNumber):
		rejection = NonFiniteNumber()
	default:
		rejection = InvalidDocument("")
	}
	rejection.cause = err
	return rejection
}
