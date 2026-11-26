// Package id provides application-generated UUIDv7 entity identifiers.
package id

import (
	"crypto/rand"
	"encoding/binary"
	"fmt"
	"time"
)

// NewV7 returns a cryptographically random RFC 9562 UUIDv7 string. The
// timestamp is descriptive only; callers must use database timestamp columns
// for scheduling and ordering.
func NewV7() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", fmt.Errorf("generate UUIDv7: %w", err)
	}

	milliseconds := time.Now().UnixMilli()
	if milliseconds < 0 {
		milliseconds = 0
	}
	binary.BigEndian.PutUint64(value[:8], uint64(milliseconds)<<16)
	value[6] = (value[6] & 0x0f) | 0x70
	value[8] = (value[8] & 0x3f) | 0x80

	return fmt.Sprintf("%x-%x-%x-%x-%x",
		value[0:4], value[4:6], value[6:8], value[8:10], value[10:16]), nil
}
