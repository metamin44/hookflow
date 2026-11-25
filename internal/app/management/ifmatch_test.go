package management

import (
	"errors"
	"strings"
	"testing"
)

const currentTag = "hf1-current"

func TestEvaluateIfMatch(t *testing.T) {
	cases := []struct {
		name   string
		header string
		want   error
	}{
		{name: "missing", header: "", want: errMissingIfMatch},
		{name: "blank", header: "   ", want: errMissingIfMatch},
		{name: "wildcard", header: "*", want: nil},
		{name: "exact", header: `"hf1-current"`, want: nil},
		{name: "list match", header: `"hf1-old", "hf1-current"`, want: nil},
		{name: "list maybe wildcard", header: `"hf1-old", *`, want: errInvalidIfMatch},
		{name: "stale", header: `"hf1-old"`, want: errStaleIfMatch},
		{name: "weak never matches", header: `W/"hf1-current"`, want: errStaleIfMatch},
		{name: "weak and strong", header: `W/"hf1-current", "hf1-current"`, want: nil},
		{name: "unquoted", header: `hf1-current`, want: errInvalidIfMatch},
		{name: "empty element", header: `"hf1-current",`, want: errInvalidIfMatch},
		{name: "bad weak", header: `W/hf1-current`, want: errInvalidIfMatch},
		{name: "embedded space", header: `"hf1 current"`, want: errInvalidIfMatch},
		{name: "bare quote", header: `"`, want: errInvalidIfMatch},
		{name: "suffix", header: `"hf1-current"x`, want: errInvalidIfMatch},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := evaluateIfMatch(testCase.header, currentTag)
			if testCase.want == nil {
				if err != nil {
					t.Fatalf("evaluateIfMatch(%q) = %v, want nil", testCase.header, err)
				}
				return
			}
			if !errors.Is(err, testCase.want) {
				t.Fatalf("evaluateIfMatch(%q) = %v, want %v", testCase.header, err, testCase.want)
			}
		})
	}
}

func TestEvaluateIfMatchIsCaseSensitive(t *testing.T) {
	if err := evaluateIfMatch(`"HF1-CURRENT"`, currentTag); !errors.Is(err, errStaleIfMatch) {
		t.Fatalf("opaque tags must compare case-sensitively, got %v", err)
	}
}

func TestIfMatchHTTPErrorMapping(t *testing.T) {
	if got := ifMatchHTTPError(errMissingIfMatch, currentTag); got.Status != 428 {
		t.Fatalf("missing If-Match status = %d, want 428", got.Status)
	}
	stale := ifMatchHTTPError(errStaleIfMatch, currentTag)
	if stale.Status != 412 {
		t.Fatalf("stale If-Match status = %d, want 412", stale.Status)
	}
	if stale.ETag != currentTag {
		t.Fatalf("stale If-Match ETag = %q, want current tag", stale.ETag)
	}
	if got := ifMatchHTTPError(errInvalidIfMatch, currentTag); got.Status != 400 {
		t.Fatalf("invalid If-Match status = %d, want 400", got.Status)
	}
}

func TestQuotedTagRejectsWhitespaceAndQuotes(t *testing.T) {
	for _, value := range []string{"", `"`, `""x`, `"a b"`, `"a"b"`, `abc`} {
		if quotedTag(value) {
			t.Fatalf("quotedTag(%q) = true, want false", value)
		}
	}
	if !quotedTag(`"hf1-current"`) {
		t.Fatal("quotedTag(valid) = false")
	}
	if strings.Contains(currentTag, `"`) {
		t.Fatal("test tag must not contain quotes")
	}
}
