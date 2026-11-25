package jcs

import (
	"errors"

	"encoding/json/jsontext"
)

// AppendString appends s as an RFC 8785 minimal JSON string literal. Invalid
// UTF-8 is fatal and nothing is appended: the caller must never hash the
// replacement characters the standard library writes before reporting that
// error.
func AppendString(dst []byte, value string) ([]byte, error) {
	start := len(dst)
	encoded, err := jsontext.AppendQuote(dst, value)
	if err != nil {
		return dst[:start], errors.Join(ErrInvalidJSON, err)
	}
	return encoded, nil
}
