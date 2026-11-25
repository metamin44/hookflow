package endpoint

import (
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	valid := []string{
		"https://example.test/hook",
		"https://example.test:443/hook?x=1",
		"https://sub.example.test/path/to/hook",
		"https://example.test/" + strings.Repeat("a", 1000),
		"HTTPS://example.test/hook",
		"https://1.1.1.1/hook",
		"https://[2606:4700:4700::1111]/hook",
	}
	for _, value := range valid {
		if err := ValidateURL(value); err != nil {
			t.Fatalf("ValidateURL(%q) = %v, want nil", value, err)
		}
	}

	invalid := map[string]string{
		"empty":          "",
		"http":           "http://example.test/hook",
		"relative":       "/hook",
		"no host":        "https:///hook",
		"opaque":         "https:example.test",
		"userinfo":       "https://user:pass@example.test/hook",
		"username only":  "https://user@example.test/hook",
		"fragment":       "https://example.test/hook#frag",
		"empty fragment": "https://example.test/hook#",
		"space":          "https://example.test/a b",
		"tab":            "https://example.test/a\tb",
		"newline":        "https://example.test/a\nb",
		"nul":            "https://example.test/a\x00b",
		"del":            "https://example.test/a\x7fb",
		"too long":       "https://example.test/" + strings.Repeat("a", MaxURLLength),
		"invalid utf8":   "https://example.test/\xff",
		"port 8443":      "https://example.test:8443/hook?x=1",
		"port 80":        "https://example.test:80/hook",
		"empty port":     "https://example.test:/hook",
		"ipv6 zone":      "https://[fe80::1%25eth0]/hook",
		"private ipv4":   "https://10.0.0.5/hook",
		"loopback":       "https://127.0.0.1/hook",
		"ipv6 loopback":  "https://[::1]/hook",
		"metadata":       "https://169.254.169.254/latest/meta-data",
	}
	for name, value := range invalid {
		t.Run(name, func(t *testing.T) {
			if err := ValidateURL(value); err == nil {
				t.Fatalf("ValidateURL(%q) = nil, want a field error", value)
			}
		})
	}
}

func TestValidateName(t *testing.T) {
	if err := ValidateName("Webhook receiver"); err != nil {
		t.Fatalf("ValidateName(valid) = %v", err)
	}
	if err := ValidateName(strings.Repeat("é", MaxNameLength)); err != nil {
		t.Fatalf("ValidateName(120 runes) = %v", err)
	}
	if err := ValidateName(""); err == nil {
		t.Fatal("ValidateName(empty) = nil, want error")
	}
	if err := ValidateName(strings.Repeat("a", MaxNameLength+1)); err == nil {
		t.Fatal("ValidateName(121) = nil, want error")
	}
	if err := ValidateName("bad\nname"); err == nil {
		t.Fatal("ValidateName(control) = nil, want error")
	}
	if err := ValidateName("\xff"); err == nil {
		t.Fatal("ValidateName(invalid utf8) = nil, want error")
	}
}

func TestValidateDescription(t *testing.T) {
	if err := ValidateDescription(nil); err != nil {
		t.Fatalf("ValidateDescription(nil) = %v", err)
	}
	if err := ValidateDescription(ptr(strings.Repeat("a", MaxDescriptionLength))); err != nil {
		t.Fatalf("ValidateDescription(500) = %v", err)
	}
	if err := ValidateDescription(ptr(strings.Repeat("a", MaxDescriptionLength+1))); err == nil {
		t.Fatal("ValidateDescription(501) = nil, want error")
	}
	if err := ValidateDescription(ptr("bad\x00description")); err == nil {
		t.Fatal("ValidateDescription(control) = nil, want error")
	}
}

func TestValidateStatus(t *testing.T) {
	for _, value := range []string{StatusActive, StatusPaused} {
		if err := ValidateStatus(value); err != nil {
			t.Fatalf("ValidateStatus(%q) = %v", value, err)
		}
	}
	for _, value := range []string{"", "ACTIVE", "deleted", "active "} {
		if err := ValidateStatus(value); err == nil {
			t.Fatalf("ValidateStatus(%q) = nil, want error", value)
		}
	}
}

func TestCanonicalHeaders(t *testing.T) {
	canonical, err := CanonicalHeaders(map[string]string{
		"X-Trace-Id": "abc",
		"Accept":     "application/json",
	})
	if err != nil {
		t.Fatalf("CanonicalHeaders() = %v", err)
	}
	if canonical["x-trace-id"] != "abc" || canonical["accept"] != "application/json" {
		t.Fatalf("CanonicalHeaders() = %v, want lowercase names", canonical)
	}

	rejected := map[string]map[string]string{
		"too many":           manyHeaders(MaxHeaderCount + 1),
		"long name":          {strings.Repeat("a", MaxHeaderNameLength+1): "v"},
		"long value":         {"x-test": strings.Repeat("v", MaxHeaderValueLength+1)},
		"total too large":    manyValues(5),
		"duplicate case":     {"x-test": "a", "X-Test": "b"},
		"control in value":   {"x-test": "a\r\nb"},
		"control in name":    {"x\x00test": "v"},
		"non token name":     {"x test": "v"},
		"prohibited":         {"Authorization": "Bearer secret"},
		"hop by hop":         {"Connection": "close"},
		"content type":       {"Content-Type": "text/plain"},
		"forwarded":          {"X-Forwarded-For": "127.0.0.1"},
		"hookflow namespace": {"X-HookFlow-Signature": "x"},
		"host":               {"Host": "example.test"},
		"webhook id":         {"Webhook-Id": "x"},
	}
	for name, headers := range rejected {
		t.Run(name, func(t *testing.T) {
			if _, err := CanonicalHeaders(headers); err == nil {
				t.Fatalf("CanonicalHeaders(%v) = nil, want error", headers)
			}
		})
	}
}

func TestCanonicalHeadersEmpty(t *testing.T) {
	canonical, err := CanonicalHeaders(nil)
	if err != nil {
		t.Fatalf("CanonicalHeaders(nil) = %v", err)
	}
	if len(canonical) != 0 {
		t.Fatalf("CanonicalHeaders(nil) = %v, want empty", canonical)
	}
}

func manyHeaders(count int) map[string]string {
	headers := make(map[string]string, count)
	for i := 0; i < count; i++ {
		headers["x-"+strings.Repeat("a", i%10)+"-"+strings.Repeat("b", i/10)] = "v"
	}
	return headers
}

func manyValues(count int) map[string]string {
	headers := make(map[string]string, count)
	for i := 0; i < count; i++ {
		headers["x-test-"+strings.Repeat("a", i)] = strings.Repeat("v", MaxHeaderValueLength)
	}
	return headers
}

func ptr(value string) *string { return &value }
