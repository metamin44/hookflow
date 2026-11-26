package endpoint

import (
	"context"
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
		"X-Tenant":   "acme",
	})
	if err != nil {
		t.Fatalf("CanonicalHeaders() = %v", err)
	}
	if canonical["x-trace-id"] != "abc" || canonical["x-tenant"] != "acme" {
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

func TestCanonicalHeadersCredentialPolicy(t *testing.T) {
	const secretValue = "credential-value-must-not-be-echoed"
	rejected := []string{
		"Authorization", "aUtHoRiZaTiOn", "Proxy-Authorization", "Cookie", "Set-Cookie",
		"X-API-Key", "Api-Key", "X-Auth-Token", "X-Access-Token", "X-Refresh-Token",
		"X-ID-Token", "X-Session-Token", "X-Csrf-Token", "X-Client-Secret",
		"X-Client-Password", "X-Password", "X-Passwd", "X-Credentials", "X-Private-Key",
		"X-Auth", "X-Authentication", "X-Bearer", "X-JWT",
		"X.Api.Key", "X.Access.Key", "X.Private.Key", "X.Secret.Key",
		"X_API_KEY", "x.auth.token", "XApiKey", "XAuthToken",
	}
	for _, name := range rejected {
		t.Run(name, func(t *testing.T) {
			canonical, field := CanonicalHeaders(map[string]string{name: secretValue})
			if field == nil || canonical != nil {
				t.Fatalf("CanonicalHeaders(%q) = %v, %v; want rejection", name, canonical, field)
			}
			if field.Code != "invalid_headers" || strings.Contains(field.Detail, secretValue) {
				t.Fatalf("unsafe header rejection: %+v", field)
			}
		})
	}

	accepted := []string{
		"X-Tenant", "X-Tenant-Id", "X-Trace-Id", "X-Event-Version", "X-Token-Count", "X-Tokenizer-Version",
	}
	for _, name := range accepted {
		t.Run(name, func(t *testing.T) {
			canonical, field := CanonicalHeaders(map[string]string{name: "ordinary-value"})
			if field != nil {
				t.Fatalf("CanonicalHeaders(%q) = %v, want acceptance", name, field)
			}
			if got := canonical[strings.ToLower(name)]; got != "ordinary-value" {
				t.Fatalf("canonical headers = %v", canonical)
			}
		})
	}

	for _, name := range []string{"X-Custom", "X-Test", "Accept", "Content-Type", "X-HookFlow-Delivery-Id"} {
		t.Run("unsupported "+name, func(t *testing.T) {
			if _, field := CanonicalHeaders(map[string]string{name: secretValue}); field == nil {
				t.Fatalf("CanonicalHeaders accepted unsupported name %q", name)
			}
		})
	}

	for _, name := range []string{"", "X Bad", "X\tBad", "X:Bad"} {
		t.Run("malformed "+name, func(t *testing.T) {
			if _, field := CanonicalHeaders(map[string]string{name: secretValue}); field == nil {
				t.Fatalf("CanonicalHeaders accepted malformed name %q", name)
			}
		})
	}
}

func TestCredentialBearingHeaderSegmentPolicy(t *testing.T) {
	for _, name := range []string{
		"Authorization", "Proxy-Authorization", "Cookie", "Set-Cookie", "X-Auth", "X-Authentication",
		"X-Bearer", "X-JWT", "X.Api.Key", "X.Access.Key", "X.Private.Key", "X.Secret.Key",
		"X-API-Key", "X-Access-Key", "X-Auth-Token", "X-Access-Token", "X-Client-Secret",
		"X-Password", "X-Passwd", "X-Pwd", "XPwd", "X-Credential", "X-Session-Token", "XApiKey", "XAuthToken",
		"X-Auth-Key", "XAuthKey", "X-Signing-Key", "X-Encryption-Key", "X-HMAC-Key",
	} {
		if !CredentialBearingHeader(name) {
			t.Errorf("CredentialBearingHeader(%q) = false", name)
		}
	}
	for _, name := range []string{
		"X-Tenant", "X-Trace-Id", "X-Token-Count", "X-Tokenizer-Version",
		"X-Cookie-Count", "X-Password-Expiry", "X-Credential-Type", "X-Token-Status",
		"X-Auth-Type", "X-Auth-Version", "X-Signing-Key-Version", "X-Encryption-Key-Rotation",
	} {
		if CredentialBearingHeader(name) {
			t.Errorf("CredentialBearingHeader(%q) = true", name)
		}
	}
}

func TestSafeHeadersFiltersHistoricalValuesDeterministically(t *testing.T) {
	headers := map[string]string{
		"X-Tenant":      "acme",
		"X.Api.Key":     "legacy-secret",
		"X-Custom":      "unsupported",
		"X-Token-Count": "3",
		"X-Trace-Id":    "trace",
		"X-TENANT":      "ambiguous",
	}
	got := SafeHeaders(headers)
	want := map[string]string{"x-token-count": "3", "x-trace-id": "trace"}
	if len(got) != len(want) {
		t.Fatalf("SafeHeaders() = %v, want %v", got, want)
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("SafeHeaders()[%q] = %q, want %q", name, got[name], value)
		}
	}
	if headers["X.Api.Key"] != "legacy-secret" || headers["X-TENANT"] != "ambiguous" {
		t.Fatal("SafeHeaders mutated its input")
	}
}

func TestPersistenceBoundaryRejectsUnsupportedHeaders(t *testing.T) {
	for _, write := range []struct {
		name string
		run  func() error
	}{
		{name: "insert", run: func() error {
			_, err := Insert(context.Background(), nil, Record{HTTPHeaders: map[string]string{"X-Auth": "secret"}})
			return err
		}},
		{name: "update", run: func() error {
			_, err := Update(context.Background(), nil, Record{HTTPHeaders: map[string]string{"X-Custom": "secret"}})
			return err
		}},
	} {
		t.Run(write.name, func(t *testing.T) {
			if err := write.run(); err == nil {
				t.Fatal("persistence boundary accepted unsupported headers")
			}
		})
	}
}

func TestValidateCreateInputRejectsCredentialHeader(t *testing.T) {
	_, field := ValidateCreateInput("receiver", nil, "https://example.test/hook",
		map[string]string{"X-Auth-Token": "create-secret"})
	if field == nil || field.Code != "invalid_headers" || strings.Contains(field.Detail, "create-secret") {
		t.Fatalf("ValidateCreateInput() field = %+v, want safe credential rejection", field)
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
	names := []string{"x-tenant", "x-tenant-id", "x-trace-id", "x-event-version", "x-token-count", "x-tokenizer-version"}
	headers := make(map[string]string, count)
	for i := 0; i < count; i++ {
		headers[names[i%len(names)]] = strings.Repeat("v", MaxHeaderValueLength)
	}
	return headers
}

func ptr(value string) *string { return &value }
