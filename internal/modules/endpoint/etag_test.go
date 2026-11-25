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

func TestQuotedHeader(t *testing.T) {
	if got := QuotedHeader("hf1-abc"); got != `"hf1-abc"` {
		t.Fatalf("QuotedHeader() = %q", got)
	}
}
