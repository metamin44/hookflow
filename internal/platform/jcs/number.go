package jcs

import (
	"errors"
	"math"
	"strconv"
	"strings"

	"encoding/json/jsontext"
)

// safeIntegerLimit is 2^53. U46 accepts integer values with magnitude at most
// 2^53-1; guard G1 rejects binary64 values whose magnitude reaches 2^53.
const safeIntegerLimitText = "9007199254740992"
const maxExponentDigits = 7
const clampedExponent = int64(10000000)

// validateNumber applies the approved §15.3 numeric order to one literal:
// literal-level integrality and range, binary64 conversion, finiteness, and
// guard G1. It reports whether the accepted literal was inexact for the
// observability counter only.
func validateNumber(literal []byte) (bool, error) {
	original, err := parseDecimal(literal)
	if err != nil {
		return false, ErrInvalidJSON
	}

	// Stages 2-4: decide integrality and range from the literal alone.
	if original.integral() {
		if !original.safeInteger() {
			return false, ErrIntegerOutOfSafeRange
		}
	}

	// Stage 5: the first lossy conversion.
	value, err := strconv.ParseFloat(string(literal), 64)
	if err != nil && !errors.Is(err, strconv.ErrRange) {
		return false, ErrInvalidJSON
	}

	// Stage 6: reject non-finite results. Underflow (0, ErrRange on some
	// versions, 0, nil on others) remains an accepted inexact value.
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return false, ErrNonFiniteNumber
	}

	// Stage 7: G1. Every binary64 at or above 2^53 is an integer, so this
	// closes fractional literals that collapsed onto an unsafe integer.
	if math.Abs(value) >= float64(uint64(1)<<53) {
		return false, ErrIntegerOutOfSafeRange
	}

	// Stage 8: accept and report inexactness only when the shortest
	// round-trip decimal differs from the literal's mathematical value.
	canonical, err := parseDecimal(jsontext.AppendFloat(nil, value, 64))
	if err != nil {
		return false, ErrInvalidJSON
	}
	return !original.sameValue(canonical), nil
}

type decimal struct {
	digits string
	exp    int64
}

func (d decimal) isZero() bool { return d.digits == "0" }

// integral reports whether d represents an integer mathematically. Trailing
// zeros are deliberately retained in d.digits so 1.200e1 is integral.
func (d decimal) integral() bool {
	if d.isZero() {
		return true
	}
	if d.exp >= 0 {
		return true
	}
	need := -d.exp
	trailing := int64(0)
	for i := len(d.digits) - 1; i >= 0 && d.digits[i] == '0'; i-- {
		trailing++
	}
	return trailing >= need
}

// safeInteger compares the integral value's digit count and, only for a
// 16-digit value, its bounded decimal text against 2^53. No decimal shift is
// ever materialized.
func (d decimal) safeInteger() bool {
	if d.isZero() {
		return true
	}
	count := int64(len(d.digits)) + d.exp
	if count < 16 {
		return true
	}
	if count > 16 {
		return false
	}
	text := d.digits
	if d.exp > 0 {
		text += strings.Repeat("0", int(d.exp))
	} else if d.exp < 0 {
		cut := int64(len(text)) + d.exp
		text = text[:cut]
	}
	return text < safeIntegerLimitText
}

// sameValue compares two decimal values after removing insignificant trailing
// zeros from the significands.
func (d decimal) sameValue(other decimal) bool {
	left, leftExp := trimZeros(d)
	right, rightExp := trimZeros(other)
	return left == right && leftExp == rightExp
}

func trimZeros(d decimal) (string, int64) {
	if d.isZero() {
		return "0", 0
	}
	digits := d.digits
	exp := d.exp
	for len(digits) > 1 && digits[len(digits)-1] == '0' {
		digits = digits[:len(digits)-1]
		exp++
	}
	return digits, exp
}

// parseDecimal splits an RFC 8259 number literal into D and p for
// value = D × 10^p. D strips leading zeros and keeps trailing zeros. The
// exponent is clamped after seven digits so attacker input cannot amplify
// work.
func parseDecimal(literal []byte) (decimal, error) {
	text := string(literal)
	if text == "" {
		return decimal{}, errors.New("empty number")
	}
	if text[0] == '-' {
		text = text[1:]
	}
	mantissa := text
	exponent := int64(0)
	if index := strings.IndexAny(text, "eE"); index >= 0 {
		mantissa = text[:index]
		parsed, err := parseExponent(text[index+1:])
		if err != nil {
			return decimal{}, err
		}
		exponent = parsed
	}

	fractionDigits := 0
	if dot := strings.IndexByte(mantissa, '.'); dot >= 0 {
		fractionDigits = len(mantissa) - dot - 1
		mantissa = mantissa[:dot] + mantissa[dot+1:]
	}
	digits := strings.TrimLeft(mantissa, "0")
	if digits == "" {
		return decimal{digits: "0", exp: 0}, nil
	}
	return decimal{digits: digits, exp: exponent - int64(fractionDigits)}, nil
}

func parseExponent(text string) (int64, error) {
	if text == "" {
		return 0, errors.New("missing exponent")
	}
	negative := false
	switch text[0] {
	case '+':
		text = text[1:]
	case '-':
		negative = true
		text = text[1:]
	}
	if text == "" {
		return 0, errors.New("missing exponent digits")
	}
	for i := 0; i < len(text); i++ {
		if text[i] < '0' || text[i] > '9' {
			return 0, errors.New("invalid exponent")
		}
	}
	if len(text) > maxExponentDigits {
		// Only the sign can affect any decision at this magnitude.
		if negative {
			return -clampedExponent, nil
		}
		return clampedExponent, nil
	}
	value, err := strconv.ParseInt(text, 10, 64)
	if err != nil {
		return 0, err
	}
	if negative {
		return -value, nil
	}
	return value, nil
}
