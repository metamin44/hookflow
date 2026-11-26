package history

import (
	"strings"
	"testing"
	"time"
)

func testCodec(t *testing.T, token string) *Codec {
	t.Helper()
	codec, err := NewCodec(KeyFromToken(token))
	if err != nil {
		t.Fatalf("NewCodec() = %v", err)
	}
	return codec
}

func TestCursorRoundTrip(t *testing.T) {
	codec := testCodec(t, "whf_test_token")
	position := time.Date(2026, 1, 2, 3, 4, 5, 123456000, time.UTC)
	value, err := codec.Encode(Cursor{
		Kind:           CursorEvents,
		OrganizationID: "0190f5b0-0000-7000-8000-000000000001",
		Filters:        EventFilter{Type: "order.created"}.Fingerprint(),
		Time:           position,
		ID:             "0190f5b0-0000-7000-8000-000000000002",
	})
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}
	if strings.Contains(value, "|") || strings.Contains(strings.ToLower(value), "created_at") {
		t.Fatalf("cursor leaks implementation detail: %q", value)
	}
	decoded, err := codec.Decode(value)
	if err != nil {
		t.Fatalf("Decode() = %v", err)
	}
	if decoded.Version != cursorVersion || decoded.Kind != CursorEvents ||
		decoded.OrganizationID != "0190f5b0-0000-7000-8000-000000000001" ||
		decoded.ID != "0190f5b0-0000-7000-8000-000000000002" ||
		!decoded.Time.Equal(position) {
		t.Fatalf("decoded cursor = %+v", decoded)
	}
}

func TestCursorRejectsTamperingAndForeignKeys(t *testing.T) {
	codec := testCodec(t, "whf_test_token")
	value, err := codec.Encode(Cursor{
		Kind:           CursorDeliveries,
		OrganizationID: "0190f5b0-0000-7000-8000-000000000001",
		Filters:        DeliveryFilter{State: StatePending}.Fingerprint(),
		Time:           time.Now().UTC(),
		ID:             "0190f5b0-0000-7000-8000-000000000002",
	})
	if err != nil {
		t.Fatalf("Encode() = %v", err)
	}

	for _, testCase := range []struct {
		name  string
		value string
	}{
		{"empty", ""},
		{"missing signature", strings.SplitN(value, ".", 2)[0]},
		{"extra part", value + ".extra"},
		{"invalid base64", "%%%." + strings.SplitN(value, ".", 2)[1]},
		{"truncated signature", value[:len(value)-1]},
		{"flipped payload", flipByte(value, 3)},
		{"flipped signature", flipByte(value, len(value)-3)},
	} {
		if _, err := codec.Decode(testCase.value); err == nil {
			t.Fatalf("%s cursor was accepted", testCase.name)
		}
	}

	other := testCodec(t, "whf_other_token")
	if _, err := other.Decode(value); err == nil {
		t.Fatal("cursor signed for another credential was accepted")
	}
}

func TestNewCodecRequiresLongKey(t *testing.T) {
	if _, err := NewCodec(make([]byte, 31)); err == nil {
		t.Fatal("short cursor key was accepted")
	}
	if _, err := NewCodec(make([]byte, 32)); err != nil {
		t.Fatalf("NewCodec() = %v", err)
	}
}

func TestFingerprintIsUnambiguous(t *testing.T) {
	first := Fingerprint(CursorEvents, "ab", "c")
	second := Fingerprint(CursorEvents, "a", "bc")
	if first == second {
		t.Fatalf("fingerprints collided: %q", first)
	}
	if Fingerprint(CursorEvents, "", "") == Fingerprint(CursorEvents) {
		t.Fatal("empty filter fields collapsed into an absent filter")
	}
	if (EventFilter{}).Fingerprint() == (EventFilter{Type: "x"}).Fingerprint() {
		t.Fatal("event filter change was not reflected in the fingerprint")
	}
}

func flipByte(value string, index int) string {
	replacement := byte('A')
	if value[index] == 'A' {
		replacement = 'B'
	}
	raw := []byte(value)
	raw[index] = replacement
	return string(raw)
}
