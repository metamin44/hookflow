// Package history provides read-only, organization-scoped queries over the
// events, deliveries and delivery_attempts tables. It never opens transactions,
// never writes, and never decrypts secrets: it projects only the columns that
// are safe to expose through the delivery history API.
package history

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

// ErrInvalidCursor reports a cursor that cannot be trusted: it is malformed,
// was tampered with, or was signed with a different credential.
var ErrInvalidCursor = errors.New("invalid history cursor")

const cursorVersion = 1

// Cursor kinds. A cursor is only accepted by the route that issued it.
const (
	CursorEvents     = "events"
	CursorDeliveries = "deliveries"
	CursorAttempts   = "attempts"
)

// Cursor is the decoded, authenticated pagination position. Time and ID carry
// the keyset position of the newest or oldest row already returned; Number
// carries the attempt position. The payload never encodes a database offset.
type Cursor struct {
	Version        int       `json:"v"`
	Kind           string    `json:"k"`
	OrganizationID string    `json:"o"`
	ResourceID     string    `json:"r,omitempty"`
	Filters        string    `json:"f,omitempty"`
	Time           time.Time `json:"t,omitzero"`
	ID             string    `json:"i,omitempty"`
	Number         int       `json:"n,omitempty"`
}

// KeyFromToken derives the cursor signing key from the presented API key. A
// cursor is therefore bound to the credential that issued it, and remains valid
// across API restarts and replicas without any additional deployment secret.
func KeyFromToken(token string) []byte {
	sum := sha256.Sum256([]byte("hookflow.cursor.key.v1\x00" + token))
	return sum[:]
}

// Codec signs and verifies opaque cursors with HMAC-SHA256.
type Codec struct {
	key []byte
}

// NewCodec builds a cursor codec. The key must be at least 32 bytes.
func NewCodec(key []byte) (*Codec, error) {
	if len(key) < 32 {
		return nil, errors.New("history: cursor key is too short")
	}
	copied := make([]byte, len(key))
	copy(copied, key)
	return &Codec{key: copied}, nil
}

// Encode renders the canonical signed cursor form.
func (c *Codec) Encode(cursor Cursor) (string, error) {
	cursor.Version = cursorVersion
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, c.key)
	mac.Write(payload)
	encoded := base64.RawURLEncoding.EncodeToString(payload) + "." +
		base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	return encoded, nil
}

// Decode verifies the signature and strictly parses the payload. Every failure
// is reported as ErrInvalidCursor so callers cannot distinguish causes.
func (c *Codec) Decode(value string) (Cursor, error) {
	parts := strings.Split(value, ".")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Cursor{}, ErrInvalidCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	signature, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil || len(signature) != sha256.Size {
		return Cursor{}, ErrInvalidCursor
	}
	mac := hmac.New(sha256.New, c.key)
	mac.Write(payload)
	if !hmac.Equal(signature, mac.Sum(nil)) {
		return Cursor{}, ErrInvalidCursor
	}
	var cursor Cursor
	decoder := json.NewDecoder(strings.NewReader(string(payload)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&cursor); err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	if cursor.Version != cursorVersion || cursor.Kind == "" || cursor.OrganizationID == "" {
		return Cursor{}, ErrInvalidCursor
	}
	return cursor, nil
}

// Fingerprint canonicalizes the filter context of a query. Length prefixes make
// the encoding unambiguous for any value, including empty strings.
func Fingerprint(kind string, parts ...string) string {
	var builder strings.Builder
	builder.WriteString(kind)
	for _, part := range parts {
		builder.WriteString("|")
		builder.WriteString(strconv.Itoa(len(part)))
		builder.WriteString(":")
		builder.WriteString(part)
	}
	return builder.String()
}

func timeValue(value *time.Time) string {
	if value == nil {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
