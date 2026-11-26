//go:build v8conf

package jcs

// This file is the independent ECMAScript conformance comparison for RFC 8785.
// It is excluded from the default build by the v8conf tag so that ordinary
// unit and integration runs never depend on Node.js. The dedicated CI job runs
// it with -tags=v8conf and treats a missing runtime, a missing script, or a
// process failure as a test failure, never as a skip.
//
// Scope: serialization only. RFC 8785 section 3.2.2.3 delegates number
// serialization to ECMAScript, section 3.2.2.2 delegates string
// serialization, and section 3.2.3 sorts property names as UTF-16 code units.
// The reference side is Node.js using JSON.stringify and the canonicalizer
// published verbatim in RFC 8785 Appendix A. HookFlow-specific input policy
// (safe-integer limits, duplicate-name rejection, lone-surrogate rejection) is
// not part of this comparison and is covered by the package unit tests.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// v8Seed fixes the splitmix64 stream that supplies additional IEEE 754 values,
// following the RFC 8785 Appendix B recommendation to validate a number
// serializer against V8 with a substantial amount of values. The stream is
// implemented here instead of using math/rand so the exact values never depend
// on a standard library implementation detail.
const (
	v8Seed       uint64 = 0x8785_5eed_2026_1125
	v8RandomBits        = 2000
)

type v8Payload struct {
	Numbers   []string `json:"numbers,omitempty"`
	Strings   []string `json:"strings,omitempty"`
	Documents []string `json:"documents,omitempty"`
}

type v8Output struct {
	Numbers   []string `json:"numbers"`
	Strings   []string `json:"strings"`
	Documents []string `json:"documents"`
}

type v8NumberCase struct {
	bits    uint64
	literal string
	source  string
}

// TestV8ConformanceNumberSerialization compares HookFlow's canonical number
// text for the same IEEE 754 binary64 value with JSON.stringify.
func TestV8ConformanceNumberSerialization(t *testing.T) {
	cases := v8NumberCases(t)
	t.Logf("comparing %d IEEE 754 values (seed %#x, %d generated) against Node.js",
		len(cases), v8Seed, v8RandomBits)

	payload := v8Payload{Numbers: make([]string, len(cases))}
	for i, item := range cases {
		payload.Numbers[i] = fmt.Sprintf("%016x", item.bits)
	}
	out := runV8(t, payload)
	if len(out.Numbers) != len(cases) {
		t.Fatalf("reference returned %d numbers, want %d", len(out.Numbers), len(cases))
	}

	for i, item := range cases {
		want := out.Numbers[i]
		got := canonicalNumberText(t, item.literal)
		if got != want {
			t.Errorf("%s (bits %016x, literal %s): canonical = %s, ECMAScript = %s",
				item.source, item.bits, item.literal, got, want)
		}
	}
}

// TestV8ConformanceStringSerialization compares AppendString with
// JSON.stringify for valid Unicode, including the RFC 8785 sample strings.
func TestV8ConformanceStringSerialization(t *testing.T) {
	cases := v8StringCases()
	payload := v8Payload{Strings: make([]string, len(cases))}
	for i, item := range cases {
		payload.Strings[i] = item.value
	}
	out := runV8(t, payload)
	if len(out.Strings) != len(cases) {
		t.Fatalf("reference returned %d strings, want %d", len(out.Strings), len(cases))
	}

	for i, item := range cases {
		got, err := AppendString(nil, item.value)
		if err != nil {
			t.Errorf("%s: AppendString(%q) = %v", item.name, item.value, err)
			continue
		}
		if want := out.Strings[i]; string(got) != want {
			t.Errorf("%s: AppendString(%q) = %s, ECMAScript = %s", item.name, item.value, got, want)
		}
	}
}

// TestV8ConformanceDocumentCanonicalization compares the full canonical
// document with the RFC 8785 Appendix A canonicalizer running on V8.
func TestV8ConformanceDocumentCanonicalization(t *testing.T) {
	cases := []struct {
		name string
		text []byte
	}{
		{name: "RFC 8785 section 3.2.2 sample", text: readFixture(t, "section-3.2.2-input.json")},
		{name: "RFC 8785 section 3.2.3 ordering sample", text: readFixture(t, "utf16-order-input.json")},
		{name: "nested numbers and unicode names", text: []byte(`{"z":[1,2,{"b":true,"a":null}],"é":"x","\u20ac":1.5,"A":"\u0001"}`)},
	}
	payload := v8Payload{Documents: make([]string, len(cases))}
	for i, item := range cases {
		payload.Documents[i] = string(item.text)
	}
	out := runV8(t, payload)
	if len(out.Documents) != len(cases) {
		t.Fatalf("reference returned %d documents, want %d", len(out.Documents), len(cases))
	}

	for i, item := range cases {
		got, err := Canonicalize(item.text)
		if err != nil {
			t.Errorf("%s: Canonicalize: %v", item.name, err)
			continue
		}
		if want := out.Documents[i]; string(got) != want {
			t.Errorf("%s: canonical = %s, ECMAScript = %s", item.name, got, want)
		}
	}
}

// v8NumberCases reads the RFC 8785 Appendix B Table 1 values and appends a
// deterministic broad sample of finite IEEE 754 values.
func v8NumberCases(t *testing.T) []v8NumberCase {
	t.Helper()
	file, err := os.Open("testdata/rfc8785/appendix-b-numbers.tsv")
	if err != nil {
		t.Fatalf("open RFC 8785 Appendix B vectors: %v", err)
	}
	defer func() { _ = file.Close() }()

	var cases []v8NumberCase
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		if len(fields) < 2 {
			t.Fatalf("malformed RFC 8785 vector line %q", line)
		}
		bits, err := strconv.ParseUint(fields[0], 16, 64)
		if err != nil {
			t.Fatalf("parse RFC 8785 bits %q: %v", fields[0], err)
		}
		literal := fields[1]
		if bits == 0x8000000000000000 {
			// Table 1 serializes minus zero as "0"; the input literal that
			// reaches the canonicalizer is "-0".
			literal = "-0"
		}
		cases = append(cases, v8NumberCase{bits: bits, literal: literal, source: "RFC 8785 Appendix B " + fields[0]})
	}
	if err := scanner.Err(); err != nil {
		t.Fatalf("read RFC 8785 vectors: %v", err)
	}
	if len(cases) == 0 {
		t.Fatal("RFC 8785 Appendix B vector set is empty")
	}

	state := v8Seed
	finite := 0
	for finite < v8RandomBits {
		bits := splitmix64(&state)
		value := math.Float64frombits(bits)
		if math.IsNaN(value) || math.IsInf(value, 0) {
			continue
		}
		literal := strconv.FormatFloat(value, 'g', -1, 64)
		roundTrip, err := strconv.ParseFloat(literal, 64)
		if err != nil {
			t.Fatalf("generated literal %q does not parse: %v", literal, err)
		}
		if math.Float64bits(roundTrip) != bits {
			t.Fatalf("generated literal %q does not round-trip to %016x", literal, bits)
		}
		cases = append(cases, v8NumberCase{bits: bits, literal: literal, source: "splitmix64"})
		finite++
	}
	return cases
}

type v8StringCase struct {
	name  string
	value string
}

func v8StringCases() []v8StringCase {
	cases := []v8StringCase{
		// RFC 8785 section 3.2.2 parsed string value.
		{name: "section 3.2.2 sample", value: "€$\u000f\nA'B\"\\\\\"/"},
		{name: "euro sign", value: "\u20ac"},
		{name: "carriage return", value: "\r"},
		{name: "hebrew dalet with dagesh", value: "\ufb33"},
		{name: "digit one", value: "1"},
		{name: "emoji grinning face", value: "\U0001F600"},
		{name: "control U+0080", value: "\u0080"},
		{name: "latin small o with diaeresis", value: "\u00f6"},
		{name: "del", value: "\u007f"},
		{name: "line separator", value: "\u2028"},
		{name: "paragraph separator", value: "\u2029"},
		{name: "quote", value: `"`},
		{name: "backslash", value: `\`},
		{name: "solidus", value: "/"},
		{name: "combining acute", value: "a\u0301"},
		{name: "empty", value: ""},
	}
	for b := 0; b < 0x20; b++ {
		cases = append(cases, v8StringCase{
			name:  fmt.Sprintf("control U+%04X", b),
			value: string(rune(b)),
		})
	}
	return cases
}

// canonicalNumberText returns HookFlow's canonical text for one number literal
// inside a minimal document.
func canonicalNumberText(t *testing.T, literal string) string {
	t.Helper()
	const prefix = `{"n":`
	canonical, err := Canonicalize([]byte(prefix + literal + `}`))
	if err != nil {
		t.Fatalf("Canonicalize number literal %q: %v", literal, err)
	}
	if !bytes.HasPrefix(canonical, []byte(prefix)) || len(canonical) <= len(prefix) || canonical[len(canonical)-1] != '}' {
		t.Fatalf("unexpected canonical document for %q: %s", literal, canonical)
	}
	return string(canonical[len(prefix) : len(canonical)-1])
}

func runV8(t *testing.T, payload v8Payload) v8Output {
	t.Helper()
	node, err := exec.LookPath("node")
	if err != nil {
		t.Fatalf("v8 conformance requires Node.js on PATH: %v", err)
	}
	if version, err := exec.Command(node, "--version").CombinedOutput(); err == nil {
		t.Logf("reference runtime: node %s", strings.TrimSpace(string(version)))
	} else {
		t.Fatalf("node --version failed: %v", err)
	}

	script := filepath.Join("testdata", "v8", "serialize.mjs")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("v8 conformance reference script is missing: %v", err)
	}
	input, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("encode reference payload: %v", err)
	}

	cmd := exec.Command(node, script)
	cmd.Stdin = bytes.NewReader(input)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("reference runtime failed: %v\nstderr: %s", err, stderr.String())
	}

	var out v8Output
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("decode reference output: %v\nstdout: %s", err, stdout.String())
	}
	return out
}

func splitmix64(state *uint64) uint64 {
	*state += 0x9e3779b97f4a7c15
	z := *state
	z = (z ^ (z >> 30)) * 0xbf58476d1ce4e5b9
	z = (z ^ (z >> 27)) * 0x94d049bb133111eb
	return z ^ (z >> 31)
}
