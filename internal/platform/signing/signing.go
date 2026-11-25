// Package signing implements the approved Standard Webhooks v1 request
// signature: HMAC-SHA256 over "webhook-id.webhook-timestamp.body".
//
// It exposes a signer for the delivery path and a constant-time verifier for
// test receivers. The HMAC key is the raw decrypted webhook secret, byte for
// byte; nothing is trimmed, decoded, or normalized here.
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Approved header names.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"

	// SignatureScheme is the only V1 signature version.
	SignatureScheme = "v1"

	MinKeyLength = 16
	MaxKeyLength = 1024
)

// Static signing errors.
var (
	ErrKeyLength        = errors.New("webhook signing key length is invalid")
	ErrInvalidID        = errors.New("webhook id is invalid")
	ErrInvalidTimestamp = errors.New("webhook timestamp is invalid")
	ErrInvalidSignature = errors.New("webhook signature is invalid")
)

// Headers are the three protocol headers to transmit after signing.
type Headers struct {
	ID        string
	Timestamp string
	Signature string
}

// Signer computes fresh signatures. It is safe for concurrent use.
type Signer struct {
	clock func() time.Time
}

// New returns a signer using the host clock.
func New() *Signer { return &Signer{clock: time.Now} }

// NewWithClock returns a signer with an injected clock.
func NewWithClock(clock func() time.Time) *Signer {
	if clock == nil {
		clock = time.Now
	}
	return &Signer{clock: clock}
}

// Sign builds a fresh timestamp and signature for one attempt. The delivery id
// is stable across attempts; the timestamp and signature are not.
func (s *Signer) Sign(deliveryID string, secret, body []byte) (Headers, error) {
	if s == nil || s.clock == nil {
		return Headers{}, errors.New("webhook signer is not initialized")
	}
	return SignAt(deliveryID, secret, body, s.clock())
}

// SignAt builds a signature for the supplied instant.
func SignAt(deliveryID string, secret, body []byte, now time.Time) (Headers, error) {
	if err := ValidateID(deliveryID); err != nil {
		return Headers{}, err
	}
	if len(secret) < MinKeyLength || len(secret) > MaxKeyLength {
		return Headers{}, ErrKeyLength
	}
	seconds := now.Unix()
	if seconds < 0 {
		return Headers{}, ErrInvalidTimestamp
	}
	timestamp := strconv.FormatInt(seconds, 10)
	mac := hmac.New(sha256.New, secret)
	mac.Write(CanonicalInput(deliveryID, timestamp, body))
	return Headers{
		ID:        deliveryID,
		Timestamp: timestamp,
		Signature: SignatureScheme + "," + base64.StdEncoding.EncodeToString(mac.Sum(nil)),
	}, nil
}

// CanonicalInput returns the exact bytes to MAC:
// webhook-id || 0x2E || timestamp || 0x2E || body.
func CanonicalInput(deliveryID, timestamp string, body []byte) []byte {
	buffer := make([]byte, 0, len(deliveryID)+len(timestamp)+len(body)+2)
	buffer = append(buffer, deliveryID...)
	buffer = append(buffer, '.')
	buffer = append(buffer, timestamp...)
	buffer = append(buffer, '.')
	buffer = append(buffer, body...)
	return buffer
}

// ValidateID accepts only the canonical lowercase hyphenated UUID form used by
// deliveries.id. Dots are impossible, so the canonical separator is unambiguous.
func ValidateID(deliveryID string) error {
	if len(deliveryID) != 36 {
		return ErrInvalidID
	}
	for i := 0; i < len(deliveryID); i++ {
		b := deliveryID[i]
		switch i {
		case 8, 13, 18, 23:
			if b != '-' {
				return ErrInvalidID
			}
		default:
			if (b < '0' || b > '9') && (b < 'a' || b > 'f') {
				return ErrInvalidID
			}
		}
	}
	return nil
}

// Verify checks a signature header with constant-time comparison. It does not
// enforce a timestamp window; callers that need one use VerifyWithTolerance.
func Verify(secret []byte, deliveryID, timestamp string, body []byte, header string) error {
	if len(secret) < MinKeyLength || len(secret) > MaxKeyLength {
		return ErrKeyLength
	}
	if err := ValidateID(deliveryID); err != nil {
		return err
	}
	if err := validateTimestamp(timestamp); err != nil {
		return err
	}
	if !strings.HasPrefix(header, SignatureScheme+",") || strings.Count(header, ",") != 1 {
		return ErrInvalidSignature
	}
	decoded, err := base64.StdEncoding.DecodeString(header[len(SignatureScheme)+1:])
	if err != nil || len(decoded) != sha256.Size {
		return ErrInvalidSignature
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(CanonicalInput(deliveryID, timestamp, body))
	if !hmac.Equal(decoded, mac.Sum(nil)) {
		return ErrInvalidSignature
	}
	return nil
}

// VerifyWithTolerance verifies the signature and bounds replay to tolerance.
func VerifyWithTolerance(secret []byte, deliveryID, timestamp string, body []byte, header string, now time.Time, tolerance time.Duration) error {
	if err := Verify(secret, deliveryID, timestamp, body, header); err != nil {
		return err
	}
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil {
		return ErrInvalidTimestamp
	}
	delta := now.Sub(time.Unix(seconds, 0))
	if delta < 0 {
		delta = -delta
	}
	if delta > tolerance {
		return fmt.Errorf("%w: timestamp outside tolerance", ErrInvalidTimestamp)
	}
	return nil
}

func validateTimestamp(timestamp string) error {
	if timestamp == "" || len(timestamp) > 19 || (len(timestamp) > 1 && timestamp[0] == '0') {
		return ErrInvalidTimestamp
	}
	for i := 0; i < len(timestamp); i++ {
		if timestamp[i] < '0' || timestamp[i] > '9' {
			return ErrInvalidTimestamp
		}
	}
	return nil
}
