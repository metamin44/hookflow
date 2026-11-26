package id

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestNewV7ShapeAndUniqueness(t *testing.T) {
	before := time.Now().UnixMilli()
	seen := make(map[string]struct{}, 256)
	for range 256 {
		value, err := NewV7()
		if err != nil {
			t.Fatalf("NewV7() error: %v", err)
		}
		if len(value) != 36 || value[14] != '7' {
			t.Fatalf("invalid UUIDv7 %q", value)
		}
		if value[8] != '-' || value[13] != '-' || value[18] != '-' || value[23] != '-' {
			t.Fatalf("invalid UUID hyphenation %q", value)
		}
		if !strings.ContainsRune("89ab", rune(value[19])) {
			t.Fatalf("invalid UUID variant %q", value)
		}
		if _, exists := seen[value]; exists {
			t.Fatalf("duplicate UUIDv7 %q", value)
		}
		seen[value] = struct{}{}
		timestamp, err := strconv.ParseUint(value[:8]+value[9:13], 16, 48)
		if err != nil {
			t.Fatalf("parse UUIDv7 timestamp: %v", err)
		}
		after := time.Now().UnixMilli()
		if int64(timestamp) < before || int64(timestamp) > after {
			t.Fatalf("UUIDv7 timestamp %d outside [%d,%d]", timestamp, before, after)
		}
	}
}
