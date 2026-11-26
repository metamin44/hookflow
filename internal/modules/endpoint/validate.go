package endpoint

import (
	"net/netip"
	"net/url"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/metamin44/hookflow/internal/platform/destination"
)

// Approved request limits.
const (
	MaxURLLength          = 2048
	MaxNameLength         = 120
	MaxDescriptionLength  = 500
	MaxHeaderCount        = 32
	MaxHeaderNameLength   = 256
	MaxHeaderValueLength  = 4096
	MaxHeadersTotalLength = 16 * 1024
)

// FieldError is a stable validation rejection for one request field.
type FieldError struct {
	Field  string
	Code   string
	Detail string
}

func (e *FieldError) Error() string { return e.Code + ": " + e.Field }

func fieldError(field, code, detail string) *FieldError {
	return &FieldError{Field: field, Code: code, Detail: detail}
}

// ValidateName enforces the approved length and rejects control characters.
func ValidateName(name string) *FieldError {
	if !utf8.ValidString(name) {
		return fieldError("name", "invalid_name", "name must be valid UTF-8")
	}
	count := utf8.RuneCountInString(name)
	if count < 1 || count > MaxNameLength {
		return fieldError("name", "invalid_name", "name must contain 1 to 120 characters")
	}
	if containsControl(name) {
		return fieldError("name", "invalid_name", "name must not contain control characters")
	}
	return nil
}

// ValidateDescription enforces the approved optional length.
func ValidateDescription(description *string) *FieldError {
	if description == nil {
		return nil
	}
	if !utf8.ValidString(*description) {
		return fieldError("description", "invalid_description", "description must be valid UTF-8")
	}
	if utf8.RuneCountInString(*description) > MaxDescriptionLength {
		return fieldError("description", "invalid_description", "description must be at most 500 characters")
	}
	if containsControl(*description) {
		return fieldError("description", "invalid_description", "description must not contain control characters")
	}
	return nil
}

// ValidateStatus accepts only the two live status labels.
func ValidateStatus(status string) *FieldError {
	if status != StatusActive && status != StatusPaused {
		return fieldError("status", "invalid_status", "status must be active or paused")
	}
	return nil
}

// ValidateURL enforces the approved destination URL syntax: absolute HTTPS,
// a host, no userinfo, no fragment, no IPv6 zone, port 443 only, and no
// prohibited literal address. Hostname DNS preflight is performed separately by
// the management service and never substitutes for delivery-time enforcement.
func ValidateURL(raw string) *FieldError {
	if raw == "" || len(raw) > MaxURLLength {
		return fieldError("url", "invalid_url", "url must be 1 to 2048 bytes")
	}
	if !utf8.ValidString(raw) {
		return fieldError("url", "invalid_url", "url must be valid UTF-8")
	}
	if hasSpacesOrControls(raw) {
		return fieldError("url", "invalid_url", "url must not contain whitespace or control characters")
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.Opaque != "" {
		return fieldError("url", "invalid_url", "url must be an absolute https URL")
	}
	return validateURLAuthority(parsed, raw)
}

func hasSpacesOrControls(raw string) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] <= 0x20 || raw[i] == 0x7f {
			return true
		}
	}
	return false
}

func validateURLAuthority(parsed *url.URL, raw string) *FieldError {
	if parsed.User != nil {
		return fieldError("url", "invalid_url", "url must not contain user information")
	}
	if parsed.Fragment != "" || parsed.RawFragment != "" || strings.Contains(raw, "#") {
		return fieldError("url", "invalid_url", "url must not contain a fragment")
	}
	host := parsed.Hostname()
	if host == "" {
		return fieldError("url", "invalid_url", "url must contain a host")
	}
	if strings.Contains(host, "%") {
		return fieldError("url", "invalid_url", "url must not contain an IPv6 zone identifier")
	}
	if port := parsed.Port(); port != "" && port != destination.HTTPSPort {
		return fieldError("url", "invalid_url", "url must use port 443")
	}
	if strings.HasSuffix(parsed.Host, ":") {
		return fieldError("url", "invalid_url", "url must not contain an empty port")
	}
	if addr, err := netip.ParseAddr(host); err == nil {
		if destination.CheckAddr(addr) != nil {
			return fieldError("url", "invalid_url", "url must not target a prohibited address")
		}
	}
	return nil
}

// CanonicalHeaders validates a header object and returns the lowercase,
// deterministically ordered form stored in the database.
func CanonicalHeaders(headers map[string]string) (map[string]string, *FieldError) {
	if len(headers) > MaxHeaderCount {
		return nil, fieldError("http_headers", "invalid_headers", "at most 32 headers are allowed")
	}
	canonical := make(map[string]string, len(headers))
	total := 0
	for _, name := range SortedHeaderNames(headers) {
		value := headers[name]
		if !validHeaderName(name) {
			return nil, fieldError("http_headers", "invalid_headers", "header names must be RFC 7230 tokens")
		}
		if len(value) > MaxHeaderValueLength {
			return nil, fieldError("http_headers", "invalid_headers", "header values must be at most 4096 bytes")
		}
		if hasBadBytes(name) || hasBadBytes(value) {
			return nil, fieldError("http_headers", "invalid_headers", "headers must not contain control characters")
		}
		lower := strings.ToLower(name)
		if _, duplicate := canonical[lower]; duplicate {
			return nil, fieldError("http_headers", "invalid_headers", "header names must be unique ignoring case")
		}
		if CredentialBearingHeader(lower) {
			return nil, fieldError("http_headers", "invalid_headers", "header name is not allowed")
		}
		if !SupportedCustomHeader(lower) {
			return nil, fieldError("http_headers", "invalid_headers", "header name is not supported")
		}
		total += len(name) + len(value)
		if total > MaxHeadersTotalLength {
			return nil, fieldError("http_headers", "invalid_headers", "headers must total at most 16 KiB")
		}
		canonical[lower] = value
	}
	return canonical, nil
}

// SortedHeaderNames returns the canonical order used by the ETag encoding.
func SortedHeaderNames(headers map[string]string) []string {
	names := make([]string, 0, len(headers))
	for name := range headers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func validHeaderName(name string) bool {
	if name == "" || len(name) > MaxHeaderNameLength {
		return false
	}
	for i := 0; i < len(name); i++ {
		b := name[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') {
			continue
		}
		switch b {
		case '!', '#', '$', '%', '&', '\'', '*', '+', '-', '.', '^', '_', '`', '|', '~':
			continue
		default:
			return false
		}
	}
	return true
}

func hasBadBytes(value string) bool {
	for i := 0; i < len(value); i++ {
		if value[i] == '\r' || value[i] == '\n' || value[i] == 0 || value[i] < 0x20 || value[i] == 0x7f {
			return true
		}
	}
	return false
}

func containsControl(value string) bool {
	for _, r := range value {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// supportedCustomHeaders is the complete set of user-configurable endpoint
// header names. Application-managed content, transport, signature, routing and
// diagnostic headers are deliberately absent.
var supportedCustomHeaders = map[string]struct{}{
	"x-tenant":            {},
	"x-tenant-id":         {},
	"x-trace-id":          {},
	"x-event-version":     {},
	"x-token-count":       {},
	"x-tokenizer-version": {},
}

// SupportedCustomHeader reports whether name is explicitly supported for new
// endpoint writes. Lookup is case-insensitive but separators are not rewritten.
func SupportedCustomHeader(name string) bool {
	if !validHeaderName(name) {
		return false
	}
	_, ok := supportedCustomHeaders[strings.ToLower(name)]
	return ok
}

// CredentialBearingHeader is the defense-in-depth classifier shared by write
// validation, response redaction, snapshot creation and delivery execution.
// It is deliberately segment-aware so benign metadata such as X-Token-Count,
// X-Password-Expiry and X-Credential-Type is not rejected by substring matches.
func CredentialBearingHeader(name string) bool {
	segments := headerNameSegments(name)
	return credentialHeaderSegments(segments) || compactCredentialHeader(segments)
}

func credentialHeaderSegments(segments []string) bool {
	for i, segment := range segments {
		terminal := i == len(segments)-1
		next := ""
		if !terminal {
			next = segments[i+1]
		}
		switch segment {
		case "authorization", "authentication", "auth", "bearer", "jwt",
			"password", "passwd", "pwd", "cookie", "credential", "credentials", "secret", "token":
			if terminal || !benignHeaderQualifier(next) {
				return true
			}
		}
		if !terminal && sensitiveHeaderPair(segment, next) {
			after := ""
			if i+2 < len(segments) {
				after = segments[i+2]
			}
			if after == "" || !benignHeaderQualifier(after) {
				return true
			}
		}
	}
	return false
}

func compactCredentialHeader(segments []string) bool {
	// Compact spellings such as XApiKey and XAuthToken have no segment
	// boundary, so retain a narrow exact/suffix fallback after segment checks.
	compact := strings.Join(segments, "")
	switch compact {
	case "xauth", "xauthentication", "xbearer", "xjwt":
		return true
	}
	for _, suffix := range []string{
		"authorization", "password", "passwd", "pwd", "cookie", "credential", "credentials",
		"secret", "token", "apikey", "accesskey", "authkey", "privatekey", "secretkey",
		"signingkey", "encryptionkey", "hmackey", "clientsecret",
	} {
		if strings.HasSuffix(compact, suffix) {
			return true
		}
	}
	return false
}

func headerNameSegments(name string) []string {
	var segments []string
	var current strings.Builder
	flush := func() {
		if current.Len() > 0 {
			segments = append(segments, current.String())
			current.Reset()
		}
	}
	for _, r := range strings.ToLower(name) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			current.WriteRune(r)
			continue
		}
		flush()
	}
	flush()
	return segments
}

func benignHeaderQualifier(segment string) bool {
	switch segment {
	case "count", "type", "expiry", "status", "version", "format", "length",
		"enabled", "required", "policy", "rotation":
		return true
	default:
		return false
	}
}

func sensitiveHeaderPair(first, second string) bool {
	if second == "key" {
		switch first {
		case "api", "access", "auth", "private", "secret", "signing", "encryption", "hmac":
			return true
		}
	}
	return first == "client" && second == "secret"
}

// SafeHeaders returns the deterministic public/snapshot form of historical
// headers: supported names only, valid bounded values, no credential-bearing
// names, and no ambiguous case-insensitive duplicates. The input is not mutated.
func SafeHeaders(headers map[string]string) map[string]string {
	safe := make(map[string]string, len(headers))
	seen := make(map[string]struct{}, len(headers))
	conflicted := make(map[string]struct{})
	for _, name := range SortedHeaderNames(headers) {
		lower := strings.ToLower(name)
		if _, duplicate := seen[lower]; duplicate {
			delete(safe, lower)
			conflicted[lower] = struct{}{}
			continue
		}
		seen[lower] = struct{}{}
		if _, ambiguous := conflicted[lower]; ambiguous {
			continue
		}
		value := headers[name]
		if !SupportedCustomHeader(name) || CredentialBearingHeader(name) ||
			len(value) > MaxHeaderValueLength || hasBadBytes(value) {
			continue
		}
		safe[lower] = value
	}
	return safe
}

// ValidateCreateInput validates the full create document.
func ValidateCreateInput(name string, description *string, rawURL string, headers map[string]string) (map[string]string, *FieldError) {
	if err := ValidateName(name); err != nil {
		return nil, err
	}
	if err := ValidateDescription(description); err != nil {
		return nil, err
	}
	if err := ValidateURL(rawURL); err != nil {
		return nil, err
	}
	return CanonicalHeaders(headers)
}
