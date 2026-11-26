// Package diagnostics normalizes untrusted diagnostic text before it is stored
// or displayed. It redacts recognizable credential-bearing values, collapses
// whitespace and control characters, and truncates on rune boundaries.
//
// The coverage is deliberately conservative: recognizable key/value forms
// (including JSON-quoted keys), bearer/basic authorization values, and cookie
// headers. Unstructured payloads are outside this guarantee, so callers must
// still avoid exposing receiver-controlled bodies.
package diagnostics

import (
	"regexp"
	"strings"
)

// Credential-bearing patterns redacted before storage or display.
var (
	credentialCookieRE = regexp.MustCompile(
		`(?i)\b(cookie|set[_-]?cookie)\b(["']?\s*[:=]\s*)[^;]*(?:;\s*[A-Za-z0-9!#$%&'*+.^_` + "`" + `|~-]+\s*=\s*[^;]*)*`,
	)
	credentialFieldRE = regexp.MustCompile(
		`(?i)\b(authorization|proxy-authorization|x-api-key|api[_-]?key|access[_-]?token|refresh[_-]?token|client[_-]?secret|password|passwd|pwd|secret)\b(["']?\s*[:=]\s*)("(?:[^"\\]|\\.)*"|'(?:[^'\\]|\\.)*'|(?:bearer|basic)\s+[^\s,;&]+|[^\s,;&]+)`,
	)
	credentialBearerRE = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
)

// Bounded normalizes a raw diagnostic, redacts recognizable credential-bearing
// values, collapses whitespace, and truncates to limit characters without
// splitting a UTF-8 code point. A non-positive limit returns the scrubbed text
// unchanged.
func Bounded(value string, limit int) string {
	value = strings.ToValidUTF8(value, "?")
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.Map(func(r rune) rune {
		switch r {
		case 0, '\r', '\n', '\t':
			return ' '
		}
		if r < 0x20 || r == 0x7f {
			return ' '
		}
		return r
	}, value)
	value = strings.Join(strings.Fields(value), " ")
	value = Redact(value)
	if limit > 0 {
		if runes := []rune(value); len(runes) > limit {
			value = string(runes[:limit])
		}
	}
	return value
}

// Redact replaces recognizable credential-bearing values with [REDACTED]. It
// performs no normalization or truncation.
func Redact(value string) string {
	value = credentialCookieRE.ReplaceAllString(value, "$1$2[REDACTED]")
	value = credentialFieldRE.ReplaceAllString(value, "$1$2[REDACTED]")
	return credentialBearerRE.ReplaceAllString(value, "Bearer [REDACTED]")
}
