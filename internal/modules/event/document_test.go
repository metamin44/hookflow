package event

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/metamin44/hookflow/internal/platform/jcs"
)

func mustParse(t *testing.T, body string) Document {
	t.Helper()
	document, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse(%q): unexpected rejection %v", body, err)
	}
	return document
}

// rejectionOf asserts err is a *Rejection with the given code and field.
func rejectionOf(t *testing.T, err error, code, field string) *Rejection {
	t.Helper()
	var rejection *Rejection
	if !errors.As(err, &rejection) {
		t.Fatalf("error %v is not a *Rejection", err)
	}
	if rejection.Code != code {
		t.Fatalf("code = %q; want %q (detail %q)", rejection.Code, code, rejection.Detail)
	}
	if rejection.Field != field {
		t.Fatalf("field = %q; want %q (code %q)", rejection.Field, field, code)
	}
	return rejection
}

func TestParseAcceptsEnvelope(t *testing.T) {
	document := mustParse(t, `{"event_type":"user.created","payload":{}}`)
	if document.EventType != "user.created" {
		t.Fatalf("event type = %q", document.EventType)
	}
	if string(document.Payload) != `{}` {
		t.Fatalf("payload = %q; want {}", document.Payload)
	}
	if document.InexactNumbers != 0 {
		t.Fatalf("inexact numbers = %d; want 0", document.InexactNumbers)
	}
}

func TestParseAcceptsPayloadMembersAndOrder(t *testing.T) {
	body := `{ "payload" : {"items":[1,2],"meta":{"a":null},"event_type":"nested","flag":false}, "event_type" : "invoice.paid" }`
	document := mustParse(t, body)
	if document.EventType != "invoice.paid" {
		t.Fatalf("event type = %q", document.EventType)
	}
	want := `{"items":[1,2],"meta":{"a":null},"event_type":"nested","flag":false}`
	if string(document.Payload) != want {
		t.Fatalf("payload = %q; want %q", document.Payload, want)
	}
}

func TestParsePreservesRawPayloadLiteral(t *testing.T) {
	document := mustParse(t, `{"event_type":"a","payload":{"n":1.200e1}}`)
	if !bytes.Contains(document.Payload, []byte("1.200e1")) {
		t.Fatalf("payload lost its raw literal: %q", document.Payload)
	}
}

func TestParseRejections(t *testing.T) {
	depth := 10001
	tests := []struct {
		name  string
		body  string
		code  string
		field string
	}{
		{"empty", "", CodeInvalidDocument, ""},
		{"whitespace", "   \n\t", CodeInvalidDocument, ""},
		{"truncated", `{"event_type":"a","payload":{`, CodeInvalidDocument, ""},
		{"array document", `[]`, CodeInvalidDocument, ""},
		{"number document", `123`, CodeInvalidDocument, ""},
		{"string document", `"x"`, CodeInvalidDocument, ""},
		{"null document", `null`, CodeInvalidDocument, ""},
		{"boolean document", `true`, CodeInvalidDocument, ""},
		{"second top-level value", `{}{}`, CodeInvalidDocument, ""},
		{"trailing garbage", `{"event_type":"a","payload":{}} x`, CodeInvalidDocument, ""},
		{"duplicate top-level key", `{"event_type":"a","event_type":"b","payload":{}}`, CodeDuplicateKey, ""},
		{"duplicate nested key", `{"event_type":"a","payload":{"x":1,"x":2}}`, CodeDuplicateKey, ""},
		{"invalid utf-8", "{\"event_type\":\"a\",\"payload\":{\"x\":\"\xff\"}}", CodeInvalidDocument, ""},
		{"lone surrogate", `{"event_type":"a","payload":{"x":"\ud800"}}`, CodeInvalidDocument, ""},
		{"deep nesting", strings.Repeat("[", depth) + strings.Repeat("]", depth), CodeInvalidDocument, ""},
		{"missing event type", `{"payload":{}}`, CodeInvalidEventType, "event_type"},
		{"event type number", `{"event_type":1,"payload":{}}`, CodeInvalidEventType, "event_type"},
		{"event type null", `{"event_type":null,"payload":{}}`, CodeInvalidEventType, "event_type"},
		{"event type uppercase", `{"event_type":"User.Created","payload":{}}`, CodeInvalidEventType, "event_type"},
		{"event type empty", `{"event_type":"","payload":{}}`, CodeInvalidEventType, "event_type"},
		{"event type too long", `{"event_type":"` + strings.Repeat("a", 161) + `","payload":{}}`, CodeInvalidEventType, "event_type"},
		{"event type wildcard", `{"event_type":"user.*","payload":{}}`, CodeInvalidEventType, "event_type"},
		{"missing payload", `{"event_type":"a"}`, CodeInvalidDocument, "payload"},
		{"null payload", `{"event_type":"a","payload":null}`, CodeInvalidDocument, "payload"},
		{"array payload", `{"event_type":"a","payload":[]}`, CodeInvalidDocument, "payload"},
		{"string payload", `{"event_type":"a","payload":"x"}`, CodeInvalidDocument, "payload"},
		{"number payload", `{"event_type":"a","payload":1}`, CodeInvalidDocument, "payload"},
		{"boolean payload", `{"event_type":"a","payload":true}`, CodeInvalidDocument, "payload"},
		{"unknown field", `{"event_type":"a","payload":{},"id":"x"}`, CodeUnknownField, "id"},
		{"unsafe integer", `{"event_type":"a","payload":{"n":9007199254740992.5}}`, CodeIntegerOutOfSafeRange, ""},
		{"unsafe integer literal", `{"event_type":"a","payload":{"n":295147905179352830000}}`, CodeIntegerOutOfSafeRange, ""},
		{"overflow literal", `{"event_type":"a","payload":{"n":1e400}}`, CodeIntegerOutOfSafeRange, ""},
		{"numeric before envelope", `{"event_type":"a","payload":{},"x":9007199254740993}`, CodeIntegerOutOfSafeRange, ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Parse([]byte(test.body))
			rejection := rejectionOf(t, err, test.code, test.field)
			if rejection.Detail == "" {
				t.Fatal("empty detail")
			}
		})
	}
}

func TestParseAcceptsUnderflowAsInexact(t *testing.T) {
	document := mustParse(t, `{"event_type":"a","payload":{"n":1e-400}}`)
	if document.InexactNumbers != 1 {
		t.Fatalf("inexact numbers = %d; want 1", document.InexactNumbers)
	}
}

func TestParseCountsInexactNumbersOnce(t *testing.T) {
	document := mustParse(t, `{"event_type":"a","payload":{"a":0.1,"b":1.0000000000000000001,"c":1e-400,"d":9007199254740991}}`)
	// 0.1 round-trips as "0.1" and 9007199254740991 is exact; the other two
	// change value under JCS number serialization.
	if document.InexactNumbers != 2 {
		t.Fatalf("inexact numbers = %d; want 2", document.InexactNumbers)
	}
}

func TestRejectionKeepsParserPointerForLogs(t *testing.T) {
	_, err := Parse([]byte(`{"event_type":"a","payload":{"x":1,"x":2}}`))
	if rejection := rejectionOf(t, err, CodeDuplicateKey, ""); rejection == nil {
		t.Fatal("expected a rejection")
	}
	if pointer := jcs.Pointer(err); pointer == "" {
		t.Fatal("expected a JSON pointer for the duplicate member")
	}
}

func TestCanonicalWrapperBytes(t *testing.T) {
	document := mustParse(t, `{"event_type":"user.created","payload":{"b":2,"a":1}}`)
	canonical, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	want := `{"event_type":"user.created","payload":{"a":1,"b":2}}`
	if string(canonical) != want {
		t.Fatalf("canonical = %s; want %s", canonical, want)
	}
	// Fixed point: canonicalizing the canonical bytes changes nothing.
	again, err := jcs.Canonicalize(canonical)
	if err != nil {
		t.Fatalf("re-canonicalize: %v", err)
	}
	if !bytes.Equal(again, canonical) {
		t.Fatalf("canonicalization is not a fixed point: %s", again)
	}
}

func TestCanonicalNormalizesEquivalentNumbers(t *testing.T) {
	document := mustParse(t, `{"event_type":"a","payload":{"x":1.200e1,"y":1.2000e2,"z":1200e-2}}`)
	canonical, err := document.Canonical()
	if err != nil {
		t.Fatalf("Canonical: %v", err)
	}
	want := `{"event_type":"a","payload":{"x":12,"y":120,"z":12}}`
	if string(canonical) != want {
		t.Fatalf("canonical = %s; want %s", canonical, want)
	}
}

func TestFingerprintSemanticEquivalence(t *testing.T) {
	first := mustParse(t, `{"event_type":"user.created","payload":{"b":2,"a":1.0}}`)
	// Different whitespace, member order and number spelling; same content.
	second := mustParse(t, "{ \"payload\" : { \"a\" : 1, \"b\" : 2.000 } , \"event_type\" : \"user.created\" }")

	firstHash, err := first.Fingerprint()
	if err != nil {
		t.Fatalf("first fingerprint: %v", err)
	}
	secondHash, err := second.Fingerprint()
	if err != nil {
		t.Fatalf("second fingerprint: %v", err)
	}
	if firstHash != secondHash {
		t.Fatal("semantically equivalent documents produced different fingerprints")
	}
	if hash, err := first.Fingerprint(); err != nil || hash != firstHash {
		t.Fatalf("fingerprint is not deterministic: %v %v", hash, err)
	}

	differentType := mustParse(t, `{"event_type":"user.deleted","payload":{"a":1,"b":2}}`)
	if hash, err := differentType.Fingerprint(); err != nil || hash == firstHash {
		t.Fatalf("different event type did not change the fingerprint: %v %v", hash, err)
	}
	differentValue := mustParse(t, `{"event_type":"user.created","payload":{"a":1,"b":3}}`)
	if hash, err := differentValue.Fingerprint(); err != nil || hash == firstHash {
		t.Fatalf("different payload value did not change the fingerprint: %v %v", hash, err)
	}
}

func TestFingerprintCoversCanonicalBytesNotRawBytes(t *testing.T) {
	compact := mustParse(t, `{"event_type":"a","payload":{"x":1}}`)
	padded := mustParse(t, "{\n  \"event_type\" : \"a\",\n  \"payload\" : { \"x\" : 1.0 }\n}\n")
	first, err := compact.Fingerprint()
	if err != nil {
		t.Fatalf("compact fingerprint: %v", err)
	}
	second, err := padded.Fingerprint()
	if err != nil {
		t.Fatalf("padded fingerprint: %v", err)
	}
	if first != second {
		t.Fatal("raw request bytes leaked into the fingerprint")
	}
}

func TestInexactNumberMetricName(t *testing.T) {
	if InexactNumberMetric != "hookflow_ingest_number_inexact_total" {
		t.Fatalf("metric = %q", InexactNumberMetric)
	}
}
