// Package jcs implements RFC 8785 JSON Canonicalization Scheme (JCS) over the
// standard library's encoding/json/jsontext parser.
//
// The package is deliberately small and dependency-free: the standard library
// already implements RFC 7493 parsing (duplicate-name rejection, UTF-8
// validation, nesting limits) and RFC 8785 canonical serialization. This
// package adds the HookFlow-specific literal-level numeric validation (U46,
// U48 Option B and guard G1) that must run before a document is canonicalized
// or hashed, and exposes a single canonicalization entry point so ingestion
// cannot accidentally hash an ordinary marshaling.
package jcs

import (
	"bytes"
	"errors"
	"fmt"
	"io"

	"encoding/json/jsontext"
)

// Stable error classes. Callers map these to the approved API codes.
var (
	// ErrInvalidJSON reports malformed JSON, invalid UTF-8, invalid escapes,
	// excessive nesting, or a non-I-JSON string.
	ErrInvalidJSON = errors.New("invalid JSON document")
	// ErrDuplicateName reports a repeated object member name.
	ErrDuplicateName = errors.New("duplicate JSON object member name")
	// ErrMultipleValues reports more than one top-level JSON value.
	ErrMultipleValues = errors.New("multiple top-level JSON values")
	// ErrIntegerOutOfSafeRange reports an integer value outside
	// ±(2^53-1), or a fractional literal whose binary64 conversion reaches
	// 2^53 (guard G1).
	ErrIntegerOutOfSafeRange = errors.New("integer value is outside the safe range")
	// ErrNonFiniteNumber reports a value that is NaN, infinite, or otherwise
	// not representable as a finite IEEE 754 binary64 value.
	ErrNonFiniteNumber = errors.New("number is not finite")
)

// Stats reports observability-only facts observed while validating.
type Stats struct {
	// InexactNumbers counts accepted number literals whose shortest
	// round-trip canonical decimal differs from the literal's decimal value.
	InexactNumbers int
}

// Validate parses document as exactly one I-JSON value, rejects duplicate
// object names, and applies the approved numeric rules to every number token.
// It never decodes into map[string]interface{} and never converts a number to
// decide a range verdict.
func Validate(document []byte) (Stats, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(document))
	var stats Stats
	if err := walkValue(dec, &stats); err != nil {
		return Stats{}, normalizeSyntaxError(err)
	}
	if err := requireEOF(dec); err != nil {
		return Stats{}, err
	}
	return stats, nil
}

// Canonicalize returns the RFC 8785 canonical UTF-8 representation of
// document. It rejects duplicate member names and multiple top-level values.
//
// Canonicalize is the serialization stage only. Callers must run Validate on
// the original bytes first: the standard library serializes a finite literal
// whose binary64 conversion overflows (for example 1e400) at saturation rather
// than failing, while Validate rejects such a literal through the numeric
// policy (integral overflow as ErrIntegerOutOfSafeRange, fractional overflow
// as ErrNonFiniteNumber) before any canonical byte can be hashed. The
// approved ingestion pipeline always runs that validation first.
func Canonicalize(document []byte) ([]byte, error) {
	dec := jsontext.NewDecoder(bytes.NewReader(document))
	value, err := dec.ReadValue()
	if err != nil {
		return nil, normalizeSyntaxError(err)
	}
	// ReadValue's bytes are only valid until the next decoder call.
	value = value.Clone()
	if err := requireEOF(dec); err != nil {
		return nil, err
	}
	if err := value.Canonicalize(); err != nil {
		return nil, normalizeSyntaxError(err)
	}
	return value, nil
}

// requireEOF accepts only trailing JSON whitespace after the first value.
func requireEOF(dec *jsontext.Decoder) error {
	if _, err := dec.ReadToken(); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return normalizeSyntaxError(err)
	}
	return fmt.Errorf("%w: second top-level value", ErrMultipleValues)
}

func walkValue(dec *jsontext.Decoder, stats *Stats) error {
	kind := dec.PeekKind()
	switch kind {
	case jsontext.KindBeginObject:
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		for dec.PeekKind() != jsontext.KindEndObject {
			if _, err := dec.ReadToken(); err != nil {
				return err
			}
			if err := walkValue(dec, stats); err != nil {
				return err
			}
		}
		_, err := dec.ReadToken()
		return err
	case jsontext.KindBeginArray:
		if _, err := dec.ReadToken(); err != nil {
			return err
		}
		for dec.PeekKind() != jsontext.KindEndArray {
			if err := walkValue(dec, stats); err != nil {
				return err
			}
		}
		_, err := dec.ReadToken()
		return err
	case jsontext.KindNumber:
		token, err := dec.ReadToken()
		if err != nil {
			return err
		}
		inexact, err := validateNumber([]byte(token.String()))
		if err != nil {
			return &numberError{pointer: dec.StackPointer(), err: err}
		}
		if inexact {
			stats.InexactNumbers++
		}
		return nil
	case jsontext.KindString, jsontext.KindNull, jsontext.KindTrue, jsontext.KindFalse:
		return dec.SkipValue()
	default:
		return fmt.Errorf("%w: unexpected token %s", ErrInvalidJSON, kind)
	}
}

type numberError struct {
	pointer jsontext.Pointer
	err     error
}

func (e *numberError) Error() string { return e.err.Error() }
func (e *numberError) Unwrap() error { return e.err }

// Pointer returns the JSON Pointer of the first rejected number. It is for
// server-side logs only and must never be echoed to a client.
func Pointer(err error) string {
	var numberErr *numberError
	if errors.As(err, &numberErr) {
		return string(numberErr.pointer)
	}
	var syntaxErr *jsontext.SyntacticError
	if errors.As(err, &syntaxErr) {
		return string(syntaxErr.JSONPointer)
	}
	return ""
}

func normalizeSyntaxError(err error) error {
	if errors.Is(err, jsontext.ErrDuplicateName) {
		return fmt.Errorf("%w: %w", ErrDuplicateName, err)
	}
	if errors.Is(err, ErrMultipleValues) || errors.Is(err, ErrIntegerOutOfSafeRange) ||
		errors.Is(err, ErrNonFiniteNumber) {
		return err
	}
	return fmt.Errorf("%w: %w", ErrInvalidJSON, err)
}
