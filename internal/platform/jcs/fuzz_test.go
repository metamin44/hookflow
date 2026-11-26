package jcs

import (
	"bytes"
	"testing"

	"encoding/json/jsontext"
)

func FuzzCanonicalize(f *testing.F) {
	seeds := [][]byte{
		[]byte(`{"a":1,"b":[true,null,"x"]}`),
		[]byte(`{"n":1.200e1}`),
		[]byte(`{"n":1e0000000001}`),
		[]byte(`{"n":1e-0000000001}`),
		[]byte(`{"n":1e-400}`),
		[]byte(`{"n":9007199254740992.5}`),
		[]byte(`{"a":"\ud800"}`),
		[]byte(`{}{}`),
		[]byte(`{"a":1,"a":2}`),
		bytes.Repeat([]byte("["), 64),
	}
	for _, seed := range seeds {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, input []byte) {
		canonical, err := Canonicalize(input)
		if err != nil {
			return
		}
		if !jsonValue(canonical) {
			t.Fatalf("canonical output is not one JSON value: %q", canonical)
		}
		again, err := Canonicalize(canonical)
		if err != nil {
			t.Fatalf("second Canonicalize failed: %v", err)
		}
		if !bytes.Equal(again, canonical) {
			t.Fatalf("canonicalization is not deterministic\nfirst: %q\nsecond: %q", canonical, again)
		}

		_, _ = Validate(input)
	})
}

func jsonValue(data []byte) bool {
	return jsontext.Value(data).IsValid()
}
