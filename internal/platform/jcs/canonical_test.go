package jcs

import (
	"bufio"
	"bytes"
	"errors"
	"math"
	"os"
	"strconv"
	"strings"
	"testing"

	"encoding/hex"
	"encoding/json/jsontext"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/rfc8785/" + name)
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	return bytes.TrimSuffix(data, []byte("\n"))
}

func TestRFC8785Section322CanonicalBytes(t *testing.T) {
	input := readFixture(t, "section-3.2.2-input.json")
	want := readFixture(t, "section-3.2.2-canonical.json")
	got, err := Canonicalize(input)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("canonical bytes differ\n got: %s\nwant: %s", got, want)
	}

	again, err := Canonicalize(got)
	if err != nil {
		t.Fatalf("Canonicalize canonical form: %v", err)
	}
	if !bytes.Equal(again, got) {
		t.Fatalf("canonicalization is not a fixed point\n first: %s\nsecond: %s", got, again)
	}
}

func TestRFC8785UTF16KeyOrdering(t *testing.T) {
	input := readFixture(t, "utf16-order-input.json")
	want := readFixture(t, "utf16-order-canonical.json")
	got, err := Canonicalize(input)
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("UTF-16 key order differs\n got: %q\nwant: %q", got, want)
	}
}

// TestRFC8785Section324UTF8Bytes checks the exact byte sequence published in
// RFC 8785 section 3.2.4. The fixture comparison above proves the text; this
// test proves the UTF-8 encoding that is hashed.
func TestRFC8785Section324UTF8Bytes(t *testing.T) {
	const vector = `
7b 22 6c 69 74 65 72 61 6c 73 22 3a 5b 6e 75 6c 6c 2c 74 72
75 65 2c 66 61 6c 73 65 5d 2c 22 6e 75 6d 62 65 72 73 22 3a
5b 33 33 33 33 33 33 33 33 33 2e 33 33 33 33 33 33 33 2c 31
65 2b 33 30 2c 34 2e 35 2c 30 2e 30 30 32 2c 31 65 2d 32 37
5d 2c 22 73 74 72 69 6e 67 22 3a 22 e2 82 ac 24 5c 75 30 30
30 66 5c 6e 41 27 42 5c 22 5c 5c 5c 5c 5c 22 2f 22 7d`
	want, err := hex.DecodeString(strings.Join(strings.Fields(vector), ""))
	if err != nil {
		t.Fatalf("decode RFC vector: %v", err)
	}
	got, err := Canonicalize(readFixture(t, "section-3.2.2-input.json"))
	if err != nil {
		t.Fatalf("Canonicalize: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("section 3.2.4 bytes differ\n got: %x\nwant: %x", got, want)
	}
}

func TestAppendixBNumberSerialization(t *testing.T) {
	file, err := os.Open("testdata/rfc8785/appendix-b-numbers.tsv")
	if err != nil {
		t.Fatalf("open vectors: %v", err)
	}
	defer func() { _ = file.Close() }()

	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("malformed vector line %q", line)
		}
		bits, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil {
			t.Fatalf("parse bits %q: %v", fields[0], err)
		}
		want := fields[1]
		value := math.Float64frombits(bits)
		if bits != 0x8000000000000000 {
			got := string(jsontext.AppendFloat(nil, value, 64))
			if got != want {
				t.Errorf("float bits %s: AppendFloat = %s, want %s", fields[0], got, want)
			}
		}

		literal := want
		if bits == 0x8000000000000000 {
			literal = "-0"
		}
		canonical, err := Canonicalize([]byte(`{"n":` + literal + `}`))
		if err != nil {
			t.Errorf("bits %s canonicalize %s: %v", fields[0], literal, err)
			continue
		}
		if expected := `{"n":` + want + `}`; string(canonical) != expected {
			t.Errorf("bits %s: canonical = %s, want %s", fields[0], canonical, expected)
		}
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read vectors: %v", err)
	}
}

// TestAppendixBNonFiniteRowsAreRejected covers the two Appendix B Table 1
// rows that have no JSON representation. RFC 8785 section 3.2.2.3 requires a
// compliant implementation to terminate on NaN and Infinity. HookFlow parses
// JSON, so those literals are malformed; a finite literal that would overflow
// binary64 is refused by the numeric policy before serialization, as either an
// unsafe integer (stage 4) or a non-finite number (stage 6).
//
// Canonicalize alone is the serialization stage; the approved pipeline runs
// Validate first (see the package comment), which is where overflow becomes a
// terminal error.
func TestAppendixBNonFiniteRowsAreRejected(t *testing.T) {
	validation := []struct {
		name    string
		body    string
		wantErr error
	}{
		{name: "NaN", body: `{"n":NaN}`, wantErr: ErrInvalidJSON},
		{name: "Infinity", body: `{"n":Infinity}`, wantErr: ErrInvalidJSON},
		{name: "negative Infinity", body: `{"n":-Infinity}`, wantErr: ErrInvalidJSON},
		{name: "positive integer overflow", body: `{"n":1e400}`, wantErr: ErrIntegerOutOfSafeRange},
		{name: "negative integer overflow", body: `{"n":-1e400}`, wantErr: ErrIntegerOutOfSafeRange},
		{name: "fractional overflow", body: `{"n":` + strings.Repeat("9", 309) + `.5}`, wantErr: ErrNonFiniteNumber},
	}
	for _, test := range validation {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Validate([]byte(test.body)); !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate error = %v, want %v", err, test.wantErr)
			}
		})
	}

	// The bare parser rejects the non-JSON spellings before serialization.
	for _, body := range []string{`{"n":NaN}`, `{"n":Infinity}`, `{"n":-Infinity}`} {
		if _, err := Canonicalize([]byte(body)); !errors.Is(err, ErrInvalidJSON) {
			t.Fatalf("Canonicalize(%s) error = %v, want %v", body, err, ErrInvalidJSON)
		}
	}
}

func TestDuplicateNamesInEscapedSpellings(t *testing.T) {
	tests := []string{
		`{"a":1,"\u0061":2}`,
		`{"\u00e9":1,"é":2}`,
		`{"a":{"b":1,"\u0062":2}}`,
		`[{"x":1,"\u0078":2}]`,
	}
	for _, body := range tests {
		t.Run(body, func(t *testing.T) {
			if _, err := Validate([]byte(body)); !errors.Is(err, ErrDuplicateName) {
				t.Fatalf("Validate error = %v, want %v", err, ErrDuplicateName)
			}
			if _, err := Canonicalize([]byte(body)); !errors.Is(err, ErrDuplicateName) {
				t.Fatalf("Canonicalize error = %v, want %v", err, ErrDuplicateName)
			}
		})
	}
}

func TestCanonicalizationFixedPoint(t *testing.T) {
	tests := []string{
		`{"b":2,"a":{"d":4,"c":[3,{"f":6,"e":5}]}}`,
		`{"n":1e-400}`,
		`[true,false,null,"x",1.5]`,
		`{"a":"\u2028","b":"€","c":"\u0001"}`,
	}
	for _, input := range tests {
		t.Run(input, func(t *testing.T) {
			first, err := Canonicalize([]byte(input))
			if err != nil {
				t.Fatalf("Canonicalize: %v", err)
			}
			second, err := Canonicalize(first)
			if err != nil {
				t.Fatalf("Canonicalize canonical form: %v", err)
			}
			if !bytes.Equal(first, second) {
				t.Fatalf("not a fixed point\n first: %s\nsecond: %s", first, second)
			}
		})
	}
}

// TestSerializationVersusHookFlowInputPolicy keeps the two contracts visibly
// separate. RFC 8785 Appendix B serializes these values, but HookFlow's U46
// policy refuses them as ingestion input. The V8 conformance comparison checks
// the serialization contract only; it says nothing about what HookFlow accepts.
func TestSerializationVersusHookFlowInputPolicy(t *testing.T) {
	for _, body := range []string{
		`{"n":9007199254740992}`,
		`{"n":295147905179352830000}`,
	} {
		if _, err := Canonicalize([]byte(body)); err != nil {
			t.Fatalf("Canonicalize(%s) = %v, want the RFC serialization", body, err)
		}
		if _, err := Validate([]byte(body)); !errors.Is(err, ErrIntegerOutOfSafeRange) {
			t.Fatalf("Validate(%s) = %v, want %v", body, err, ErrIntegerOutOfSafeRange)
		}
	}
}

func TestNumericPolicyBoundaryLiterals(t *testing.T) {
	tests := []struct {
		name    string
		literal string
		wantErr error
		inexact bool
	}{
		{name: "trailing zeros integral", literal: "1.200e1"},
		{name: "trailing zeros integral two", literal: "1.2000e2"},
		{name: "exponent negative integral", literal: "1200e-2"},
		{name: "safe fraction", literal: "1.2001e1"},
		{name: "integral value at limit", literal: "9007199254740992.0", wantErr: ErrIntegerOutOfSafeRange},
		{name: "fraction rounds to limit", literal: "9007199254740992.5", wantErr: ErrIntegerOutOfSafeRange},
		{name: "huge integral", literal: "1.7e308", wantErr: ErrIntegerOutOfSafeRange},
		{name: "1E30 integer notation", literal: "1E30", wantErr: ErrIntegerOutOfSafeRange},
		{name: "simple decimal", literal: "0.1"},
		{name: "underflow accepted but inexact", literal: "1e-400", inexact: true},
		{name: "negative underflow accepted but inexact", literal: "-1e-400", inexact: true},
		{name: "smallest subnormal", literal: "5e-324"},
		{name: "150 decimal spelling", literal: "150.0"},
		{name: "150 exponent spelling", literal: "1.5e2"},
		{name: "unsafe integer literal", literal: "9007199254740993", wantErr: ErrIntegerOutOfSafeRange},
		{name: "unsafe exponent", literal: "9.007199254740992e15", wantErr: ErrIntegerOutOfSafeRange},
		{name: "unsafe exponent one above limit", literal: "9.007199254740993e15", wantErr: ErrIntegerOutOfSafeRange},
		{name: "int64 max", literal: "9223372036854775807", wantErr: ErrIntegerOutOfSafeRange},
		{name: "overflow", literal: "1e400", wantErr: ErrIntegerOutOfSafeRange},
		{name: "large exponent with digits", literal: "295147905179352830000", wantErr: ErrIntegerOutOfSafeRange},
		{name: "max safe integer decimal spelling", literal: "9007199254740991.0"},
		{name: "negative safe integer", literal: "-9007199254740991"},
		{name: "negative unsafe integer", literal: "-9007199254740992", wantErr: ErrIntegerOutOfSafeRange},
		{name: "negative zero", literal: "-0"},
		{name: "negative zero fraction", literal: "-0.0"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := []byte(`{"event_type":"x","payload":{"n":` + test.literal + `}}`)
			stats, err := Validate(document)
			if test.wantErr != nil {
				if !errors.Is(err, test.wantErr) {
					t.Fatalf("Validate error = %v, want %v", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Validate: %v", err)
			}
			if test.inexact && stats.InexactNumbers != 1 {
				t.Fatalf("InexactNumbers = %d, want 1", stats.InexactNumbers)
			}
			if !test.inexact && stats.InexactNumbers != 0 {
				t.Fatalf("InexactNumbers = %d, want 0", stats.InexactNumbers)
			}
		})
	}
}

func TestFractionalOverflowIsNonFinite(t *testing.T) {
	literal := strings.Repeat("9", 309) + ".5"
	_, err := Validate([]byte(`{"n":` + literal + `}`))
	if !errors.Is(err, ErrNonFiniteNumber) {
		t.Fatalf("Validate error = %v, want %v", err, ErrNonFiniteNumber)
	}
}

func TestStructuralRejections(t *testing.T) {
	tests := []struct {
		name    string
		body    []byte
		wantErr error
	}{
		{name: "duplicate top-level key", body: []byte(`{"a":1,"a":2}`), wantErr: ErrDuplicateName},
		{name: "duplicate nested key", body: []byte(`{"a":{"b":1,"b":2}}`), wantErr: ErrDuplicateName},
		{name: "second top-level value", body: []byte(`{}{}`), wantErr: ErrMultipleValues},
		{name: "second top-level value after whitespace", body: []byte("{}\n\t {}"), wantErr: ErrMultipleValues},
		{name: "trailing garbage", body: []byte(`{} x`), wantErr: ErrInvalidJSON},
		{name: "whitespace only", body: []byte(" \n\t\r"), wantErr: ErrInvalidJSON},
		{name: "unterminated object", body: []byte(`{"a":`), wantErr: ErrInvalidJSON},
		{name: "invalid escape", body: []byte(`{"a":"\q"}`), wantErr: ErrInvalidJSON},
		{name: "trailing array comma", body: []byte(`[1,]`), wantErr: ErrInvalidJSON},
		{name: "lone surrogate", body: []byte(`{"a":"\ud800"}`), wantErr: ErrInvalidJSON},
		{name: "invalid utf8", body: []byte{'{', '"', 'a', '"', ':', '"', 0xff, '"', '}'}, wantErr: ErrInvalidJSON},
		{name: "empty", body: nil, wantErr: ErrInvalidJSON},
		{name: "excessive depth", body: []byte(strings.Repeat("[", 10001) + strings.Repeat("]", 10001)), wantErr: ErrInvalidJSON},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Validate(test.body)
			if !errors.Is(err, test.wantErr) {
				t.Fatalf("Validate error = %v, want %v", err, test.wantErr)
			}
		})
	}
}

func TestCanonicalizationRejectsDuplicateNames(t *testing.T) {
	_, err := Canonicalize([]byte(`{"a":1,"a":2}`))
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("Canonicalize error = %v, want %v", err, ErrDuplicateName)
	}
}

func TestAppendStringEscaping(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{input: `a"b`, want: `"a\"b"`},
		{input: "tab\there", want: `"tab\there"`},
		{input: "€", want: `"€"`},
		{input: "\x7f", want: "\"\x7f\""},
		{input: "\u0001", want: `"\u0001"`},
	}
	for _, test := range tests {
		got, err := AppendString(nil, test.input)
		if err != nil {
			t.Fatalf("AppendString(%q): %v", test.input, err)
		}
		if string(got) != test.want {
			t.Errorf("AppendString(%q) = %q, want %q", test.input, got, test.want)
		}
	}
}

func TestAppendStringRejectsInvalidUTF8(t *testing.T) {
	buffer := []byte("prefix")
	got, err := AppendString(buffer, string([]byte{0xff}))
	if !errors.Is(err, ErrInvalidJSON) {
		t.Fatalf("AppendString error = %v, want %v", err, ErrInvalidJSON)
	}
	if string(got) != "prefix" {
		t.Fatalf("AppendString wrote partial output: %q", got)
	}
}

func TestEquivalentDocumentsProduceEqualCanonicalBytes(t *testing.T) {
	left := []byte("{\n  \"payload\": {\"b\": 2, \"a\": [1, 2]},\n  \"event_type\": \"user.created\"\n}")
	right := []byte(`{"event_type":"user.created","payload":{"a":[1,2],"b":2}}`)
	leftCanonical, err := Canonicalize(left)
	if err != nil {
		t.Fatalf("Canonicalize left: %v", err)
	}
	rightCanonical, err := Canonicalize(right)
	if err != nil {
		t.Fatalf("Canonicalize right: %v", err)
	}
	if !bytes.Equal(leftCanonical, rightCanonical) {
		t.Fatalf("equivalent documents differ\n left: %s\nright: %s", leftCanonical, rightCanonical)
	}
}
