package apikey

import (
	"bytes"
	"strings"
	"testing"
)

func TestGenerateAndParseToken(t *testing.T) {
	reader := bytes.NewReader(bytes.Repeat([]byte{0x2a}, TokenBytes+1))
	token, prefix, err := Generate(reader)
	if err != nil {
		t.Fatalf("Generate() error: %v", err)
	}
	if len(token) != TokenLength {
		t.Fatalf("token length = %d, want %d", len(token), TokenLength)
	}
	if prefix != token[:PrefixLength] {
		t.Fatalf("prefix = %q, want first %d characters", prefix, PrefixLength)
	}
	parsed, err := ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken() error: %v", err)
	}
	if parsed != prefix {
		t.Fatalf("ParseToken() = %q, want %q", parsed, prefix)
	}
}

func TestParseTokenRejectsMalformedValues(t *testing.T) {
	valid := strings.Repeat("A", TokenLength)
	cases := map[string]string{
		"empty":           "",
		"too short":       valid[:TokenLength-1],
		"too long":        valid + "A",
		"padding":         valid[:TokenLength-1] + "=",
		"standard base64": valid[:TokenLength-1] + "+",
		"whitespace":      valid[:TokenLength-1] + " ",
		"newline":         valid[:TokenLength-1] + "\n",
	}
	for name, token := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := ParseToken(token); err == nil {
				t.Fatalf("ParseToken(%q) succeeded, want ErrInvalidToken", token)
			}
		})
	}
}

func TestParseTokenRejectsNonCanonicalEncoding(t *testing.T) {
	// 43 'A's decode to 32 zero bytes, but canonical encoding of that value is
	// 43 'A's followed by no bits, so the final character must be 'A'. 'B' is a
	// non-canonical tail because the trailing bits are not zero.
	token := strings.Repeat("A", TokenLength-1) + "B"
	if _, err := ParseToken(token); err == nil {
		t.Fatal("ParseToken() accepted a non-canonical trailing character")
	}
}
