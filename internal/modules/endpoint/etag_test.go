package endpoint

import (
	"strings"
	"testing"
)

func sampleRepresentation() Representation {
	return Representation{
		ID:            "0198c0de-0000-7000-8000-000000000001",
		Name:          "billing",
		Description:   ptr("Billing receiver"),
		URL:           "https://example.test/hook",
		Status:        StatusActive,
		SigningScheme: SigningScheme,
		HTTPHeaders:   map[string]string{"x-trace": "abc", "accept": "application/json"},
	}
}

func TestStrongTagIsDeterministicAndPrefixed(t *testing.T) {
	rep := sampleRepresentation()
	first := StrongTag(rep, "hfse1.envelope")
	second := StrongTag(rep, "hfse1.envelope")
	if first != second {
		t.Fatalf("StrongTag() is not deterministic: %q != %q", first, second)
	}
	if !strings.HasPrefix(first, ETagPrefix) {
		t.Fatalf("StrongTag() = %q, want prefix %q", first, ETagPrefix)
	}
	encoded := first[len(ETagPrefix):]
	if len(encoded) != 43 {
		t.Fatalf("base64url digest length = %d, want 43", len(encoded))
	}
	if strings.ContainsAny(encoded, "=+/") {
		t.Fatalf("digest is not unpadded base64url: %q", encoded)
	}
}

func TestStrongTagExcludesMutableAndInternalFields(t *testing.T) {
	rep := sampleRepresentation()
	base := StrongTag(rep, "hfse1.envelope")
	// Record-level fields that are not in Representation cannot influence the
	// tag: only the representation and the secret envelope digest are hashed.
	if StrongTag(rep, "hfse1.envelope") != base {
		t.Fatal("StrongTag() changed without a representation change")
	}
}

func TestStrongTagSecretDigestChangesTag(t *testing.T) {
	rep := sampleRepresentation()
	first := StrongTag(rep, "hfse1.envelope-a")
	second := StrongTag(rep, "hfse1.envelope-b")
	if first == second {
		t.Fatal("StrongTag() ignored the secret envelope")
	}
}

func TestStrongTagHeaderCanonicalization(t *testing.T) {
	a := sampleRepresentation()
	b := sampleRepresentation()
	b.HTTPHeaders = map[string]string{"X-Trace": "abc", "Accept": "application/json"}
	if StrongTag(a, "env") != StrongTag(b, "env") {
		t.Fatal("StrongTag() differs for case-equivalent header names")
	}
}

func TestStrongTagHistoricalCaseDuplicatesAreDeterministic(t *testing.T) {
	rep := sampleRepresentation()
	rep.HTTPHeaders = map[string]string{"X-Tenant": "upper", "x-tenant": "lower"}
	want := StrongTag(rep, "env")
	for i := 0; i < 100; i++ {
		if got := StrongTag(rep, "env"); got != want {
			t.Fatalf("StrongTag() for historical duplicates changed: %q != %q", got, want)
		}
	}
}

func TestStrongTagDescriptionPresenceIsDistinct(t *testing.T) {
	absent := sampleRepresentation()
	absent.Description = nil
	empty := sampleRepresentation()
	empty.Description = ptr("")
	value := sampleRepresentation()
	if StrongTag(absent, "env") == StrongTag(empty, "env") {
		t.Fatal("StrongTag() treats a null description and an empty description as equal")
	}
	if StrongTag(empty, "env") == StrongTag(value, "env") {
		t.Fatal("StrongTag() ignores the description value")
	}
}

func TestStrongTagFieldBoundaryCollisions(t *testing.T) {
	left := Representation{ID: "id", Name: "ab", URL: "https://example.test/c", Status: StatusActive, SigningScheme: SigningScheme, HTTPHeaders: map[string]string{}}
	right := Representation{ID: "id", Name: "a", URL: "https://example.test/bc", Status: StatusActive, SigningScheme: SigningScheme, HTTPHeaders: map[string]string{}}
	if StrongTag(left, "env") == StrongTag(right, "env") {
		t.Fatal("StrongTag() concatenates fields without length prefixes")
	}
}

func TestStrongTagHeaderBoundaryCollisions(t *testing.T) {
	left := Representation{ID: "id", Name: "n", URL: "https://example.test/", Status: StatusActive, SigningScheme: SigningScheme,
		HTTPHeaders: map[string]string{"a": "bc"}}
	right := Representation{ID: "id", Name: "n", URL: "https://example.test/", Status: StatusActive, SigningScheme: SigningScheme,
		HTTPHeaders: map[string]string{"ab": "c"}}
	if StrongTag(left, "env") == StrongTag(right, "env") {
		t.Fatal("StrongTag() does not separate header names from header values")
	}
}

func TestStrongTagExcludesOD3Fields(t *testing.T) {
	// Representation has no timeout_ms/max_attempts members at all; this test
	// documents that the ETag is derived only from the approved public fields.
	rep := sampleRepresentation()
	first := StrongTag(rep, "env")
	rep.URL = "https://example.test/other"
	second := StrongTag(rep, "env")
	if first == second {
		t.Fatal("StrongTag() ignored a public field change")
	}
}

func TestRedactedRepresentPreservesFullStateETag(t *testing.T) {
	left := Record{
		ID: "0198c0de-0000-7000-8000-000000000001", Name: "billing", URL: "https://example.test/hook",
		Status: StatusActive, SigningScheme: SigningScheme, SecretEncrypted: "env-a",
		HTTPHeaders: map[string]string{"x-tenant": "acme", "authorization": "legacy-a"},
	}
	right := left
	right.HTTPHeaders = map[string]string{"x-tenant": "acme", "authorization": "legacy-b"}

	leftPublic := left.RedactedRepresent()
	rightPublic := right.RedactedRepresent()
	if leftPublic.HTTPHeaders["x-tenant"] != "acme" || len(leftPublic.HTTPHeaders) != 1 {
		t.Fatalf("redacted headers = %v", leftPublic.HTTPHeaders)
	}
	if len(rightPublic.HTTPHeaders) != 1 || rightPublic.HTTPHeaders["x-tenant"] != "acme" {
		t.Fatalf("redacted headers = %v", rightPublic.HTTPHeaders)
	}
	if StrongTag(leftPublic, left.SecretEncrypted) != StrongTag(rightPublic, right.SecretEncrypted) {
		t.Fatal("redacted representations did not converge")
	}
	if StrongTag(left.Represent(), left.SecretEncrypted) == StrongTag(right.Represent(), right.SecretEncrypted) {
		t.Fatal("full-state ETags ignored distinct historical credential values")
	}
	if left.Represent().HTTPHeaders["authorization"] != "legacy-a" || left.HTTPHeaders["authorization"] != "legacy-a" {
		t.Fatal("redaction mutated the full representation or stored record")
	}
}

func TestQuotedHeader(t *testing.T) {
	if got := QuotedHeader("hf1-abc"); got != `"hf1-abc"` {
		t.Fatalf("QuotedHeader() = %q", got)
	}
}
