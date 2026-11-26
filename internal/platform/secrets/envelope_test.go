package secrets

import (
	"bytes"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"testing"
)

func goldenEnvelope() (Envelope, []byte) {
	envelope := Envelope{
		Version:    EnvelopeVersion,
		Algorithm:  AlgorithmAES256GCM,
		Purpose:    PurposeWebhookSecret,
		KID:        "kek-01",
		Provider:   ProviderLocalFile,
		WrappedDEK: []byte{0x0a, 0x0b, 0x0c},
		Nonce:      []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11},
		Ciphertext: []byte("ciph"),
		Tag:        []byte{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15},
	}
	// The wire layout is asserted byte for byte, independently of Encode.
	wire := make([]byte, 0, 20+len(envelope.Nonce)+len(envelope.Ciphertext)+len(envelope.Tag))
	wire = append(wire, 'H', 'F', 'S', 'E', 0x01, 0x01, 0x01, 6)
	wire = append(wire, "kek-01"...)
	wire = append(wire, 0x01, 0x00, 0x03)
	wire = append(wire, 0x0a, 0x0b, 0x0c)
	wire = append(wire, envelope.Nonce...)
	wire = append(wire, envelope.Ciphertext...)
	wire = append(wire, envelope.Tag...)
	return envelope, wire
}

func goldenEncoding(t *testing.T) string {
	t.Helper()
	_, wire := goldenEnvelope()
	return Prefix + base64.RawURLEncoding.EncodeToString(wire)
}

func TestEnvelopeGoldenVector(t *testing.T) {
	envelope, _ := goldenEnvelope()
	want := goldenEncoding(t)
	got, err := envelope.Encode()
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}
	if got != want {
		t.Fatalf("Encode() = %s, want %s", got, want)
	}
	decoded, err := Decode(want)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if decoded.Version != envelope.Version || decoded.Algorithm != envelope.Algorithm ||
		decoded.Purpose != envelope.Purpose || decoded.KID != envelope.KID ||
		decoded.Provider != envelope.Provider || !bytes.Equal(decoded.WrappedDEK, envelope.WrappedDEK) ||
		!bytes.Equal(decoded.Nonce, envelope.Nonce) ||
		!bytes.Equal(decoded.Ciphertext, envelope.Ciphertext) ||
		!bytes.Equal(decoded.Tag, envelope.Tag) {
		t.Fatalf("decoded envelope = %+v", decoded)
	}
}

func TestEnvelopeLimits(t *testing.T) {
	if AADSize != 64 || DEKSize != 32 || NonceSize != 12 || TagSize != 16 {
		t.Fatalf("approved size drift: aad=%d dek=%d nonce=%d tag=%d", AADSize, DEKSize, NonceSize, TagSize)
	}
	if MinPlaintextLength != 1 || MaxPlaintextLength != 1024 {
		t.Fatalf("plaintext limits = %d..%d", MinPlaintextLength, MaxPlaintextLength)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	for _, size := range []int{1, 16, 1024} {
		t.Run(strconv.Itoa(size), func(t *testing.T) {
			plaintext := bytes.Repeat([]byte{0x5a}, size)
			dek := bytes.Repeat([]byte{0x11}, DEKSize)
			nonce := bytes.Repeat([]byte{0x22}, NonceSize)
			aad := bytes.Repeat([]byte{0x33}, AADSize)
			ciphertext, tag, err := Seal(dek, nonce, plaintext, aad)
			if err != nil {
				t.Fatalf("Seal: %v", err)
			}
			if len(ciphertext) != size || len(tag) != TagSize {
				t.Fatalf("ciphertext=%d tag=%d", len(ciphertext), len(tag))
			}
			envelope := Envelope{
				Version: EnvelopeVersion, Algorithm: AlgorithmAES256GCM, Purpose: PurposeWebhookSecret,
				KID: "kek-1", Provider: ProviderLocalFile, WrappedDEK: []byte{0x01, 0x02},
				Nonce: nonce, Ciphertext: ciphertext, Tag: tag,
			}
			encoded, err := envelope.Encode()
			if err != nil {
				t.Fatalf("Encode: %v", err)
			}
			parsed, err := Decode(encoded)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			opened, err := Open(dek, parsed.Nonce, parsed.Ciphertext, parsed.Tag, aad)
			if err != nil {
				t.Fatalf("Open: %v", err)
			}
			if !bytes.Equal(opened, plaintext) {
				t.Fatalf("plaintext of %d bytes did not round trip", size)
			}
		})
	}
}

func TestSealOpenRejectOutOfRange(t *testing.T) {
	dek := bytes.Repeat([]byte{0x11}, DEKSize)
	nonce := bytes.Repeat([]byte{0x22}, NonceSize)
	aad := bytes.Repeat([]byte{0x33}, AADSize)
	if _, _, err := Seal(dek, nonce, nil, aad); err == nil {
		t.Fatal("empty plaintext sealed")
	}
	if _, _, err := Seal(dek, nonce, make([]byte, MaxPlaintextLength+1), aad); err == nil {
		t.Fatal("oversized plaintext sealed")
	}
	if _, _, err := Seal(dek[:31], nonce, []byte("x"), aad); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short dek error = %v", err)
	}
	if _, _, err := Seal(dek, nonce[:11], []byte("x"), aad); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short nonce error = %v", err)
	}
	if _, _, err := Seal(dek, nonce, []byte("x"), aad[:63]); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short aad error = %v", err)
	}
	ciphertext, tag, err := Seal(dek, nonce, []byte("plain"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dek, nonce, ciphertext, tag[:15], aad); !errors.Is(err, ErrInvalidKey) {
		t.Fatalf("short tag error = %v", err)
	}
}

func TestDecodeRejectsMalformed(t *testing.T) {
	valid := goldenEncoding(t)
	overlong := Prefix + strings.Repeat("A", maxEncodedLength)
	cases := map[string]string{
		"empty":            "",
		"prefix only":      Prefix,
		"wrong prefix":     "hfse2." + strings.TrimPrefix(valid, Prefix),
		"uppercase prefix": "HFSE1." + strings.TrimPrefix(valid, Prefix),
		"padded":           valid + "=",
		"non-canonical":    Prefix + "AAAB",
		"invalid alphabet": Prefix + "!!!!",
		"whitespace":       " " + valid,
		"overlong input":   overlong,
	}
	for name, input := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Decode(input); err == nil {
				t.Fatalf("Decode accepted %q", input)
			} else if !errors.Is(err, ErrMalformed) && !errors.Is(err, ErrUnsupported) {
				t.Fatalf("error %v is not a typed parse failure", err)
			}
		})
	}
}

func TestDecodeNeverReconstructsATruncatedEnvelope(t *testing.T) {
	_, wire := goldenEnvelope()
	dek := bytes.Repeat([]byte{0x11}, DEKSize)
	aad := bytes.Repeat([]byte{0x33}, AADSize)

	for cut := len(wire) - 1; cut > 0; cut-- {
		value := Prefix + base64.RawURLEncoding.EncodeToString(wire[:cut])
		decoded, err := Decode(value)
		if err != nil {
			continue
		}
		// A structurally valid truncation moves the authentication boundaries, so
		// it can never authenticate as the original secret.
		if _, err := Open(dek, decoded.Nonce, decoded.Ciphertext, decoded.Tag, aad); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("truncated wire of %d bytes authenticated: %v", cut, err)
		}
	}
}

func TestDecodeAbsorbsAppendedBytesIntoAuthenticatedData(t *testing.T) {
	envelope, wire := goldenEnvelope()
	dek := bytes.Repeat([]byte{0x11}, DEKSize)
	aad := bytes.Repeat([]byte{0x33}, AADSize)
	ciphertext, tag, err := Seal(dek, envelope.Nonce, []byte("ciph"), aad)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dek, envelope.Nonce, ciphertext, tag, aad); err != nil {
		t.Fatal(err)
	}

	extended := append(append([]byte(nil), wire...), 0x7f)
	decoded, err := Decode(Prefix + base64.RawURLEncoding.EncodeToString(extended))
	if err != nil {
		t.Fatalf("appended byte rejected structurally: %v", err)
	}
	if _, err := Open(dek, decoded.Nonce, decoded.Ciphertext, decoded.Tag, aad); !errors.Is(err, ErrAuthentication) {
		t.Fatalf("appended byte still authenticated: %v", err)
	}
}

func TestDecodeRejectsTamperedStructuralFields(t *testing.T) {
	_, wire := goldenEnvelope()
	cases := map[string]func([]byte){
		"magic":         func(b []byte) { b[0] = 'X' },
		"version":       func(b []byte) { b[4] = 0x02 },
		"algorithm":     func(b []byte) { b[5] = 0x02 },
		"purpose":       func(b []byte) { b[6] = 0x02 },
		"kid length 0":  func(b []byte) { b[7] = 0 },
		"kid length 65": func(b []byte) { b[7] = 65 },
		"kid alphabet":  func(b []byte) { b[8] = '/' },
		"provider":      func(b []byte) { b[14] = 0x09 },
		"wrapped zero":  func(b []byte) { b[15], b[16] = 0, 0 },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			tampered := append([]byte(nil), wire...)
			mutate(tampered)
			encoded := Prefix + base64.RawURLEncoding.EncodeToString(tampered)
			if _, err := Decode(encoded); err == nil {
				t.Fatalf("tampered %s accepted", name)
			}
		})
	}
}

func TestEnvelopeValidatesAllFields(t *testing.T) {
	base, _ := goldenEnvelope()
	cases := map[string]func(*Envelope){
		"version":      func(e *Envelope) { e.Version = 0x02 },
		"algorithm":    func(e *Envelope) { e.Algorithm = 0x02 },
		"purpose":      func(e *Envelope) { e.Purpose = 0x02 },
		"provider":     func(e *Envelope) { e.Provider = 0x09 },
		"empty kid":    func(e *Envelope) { e.KID = "" },
		"long kid":     func(e *Envelope) { e.KID = strings.Repeat("k", MaxKIDLength+1) },
		"kid alphabet": func(e *Envelope) { e.KID = "kek 01" },
		"wrapped":      func(e *Envelope) { e.WrappedDEK = nil },
		"wrapped max":  func(e *Envelope) { e.WrappedDEK = make([]byte, MaxWrappedLength+1) },
		"nonce":        func(e *Envelope) { e.Nonce = e.Nonce[:11] },
		"ciphertext 0": func(e *Envelope) { e.Ciphertext = nil },
		"ciphertext max": func(e *Envelope) {
			e.Ciphertext = make([]byte, MaxPlaintextLength+1)
		},
		"tag": func(e *Envelope) { e.Tag = e.Tag[:15] },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			envelope := base
			mutate(&envelope)
			if err := envelope.Validate(); err == nil {
				t.Fatalf("invalid %s accepted", name)
			}
			if encoded, err := envelope.Encode(); err == nil {
				t.Fatalf("invalid %s encoded as %q", name, encoded)
			}
		})
	}
}

func TestOpenRejectsEveryTamperedAuthenticatedField(t *testing.T) {
	dek := bytes.Repeat([]byte{0x11}, DEKSize)
	nonce := bytes.Repeat([]byte{0x22}, NonceSize)
	organization := UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	endpoint := UUID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	plaintext := []byte("secret-payload-1")
	aad, err := BuildAAD(AlgorithmAES256GCM, PurposeWebhookSecret, nonce, organization, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext, tag, err := Seal(dek, nonce, plaintext, aad[:])
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(dek, nonce, ciphertext, tag, aad[:]); err != nil {
		t.Fatalf("baseline Open: %v", err)
	}

	tamper := func(value []byte, index int) []byte {
		cloned := append([]byte(nil), value...)
		cloned[index] ^= 0x01
		return cloned
	}
	t.Run("ciphertext", func(t *testing.T) {
		if _, err := Open(dek, nonce, tamper(ciphertext, len(ciphertext)-1), tag, aad[:]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("tag", func(t *testing.T) {
		if _, err := Open(dek, nonce, ciphertext, tamper(tag, 0), aad[:]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("nonce", func(t *testing.T) {
		changed := tamper(nonce, 0)
		changedAAD, err := BuildAAD(AlgorithmAES256GCM, PurposeWebhookSecret, changed, organization, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dek, changed, ciphertext, tag, changedAAD[:]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("organization transplant", func(t *testing.T) {
		other, err := BuildAAD(AlgorithmAES256GCM, PurposeWebhookSecret, nonce, UUID{9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9, 9}, endpoint)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dek, nonce, ciphertext, tag, other[:]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("endpoint transplant", func(t *testing.T) {
		other, err := BuildAAD(AlgorithmAES256GCM, PurposeWebhookSecret, nonce, organization, UUID{8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8, 8})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := Open(dek, nonce, ciphertext, tag, other[:]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("dek", func(t *testing.T) {
		if _, err := Open(tamper(dek, 0), nonce, ciphertext, tag, aad[:]); !errors.Is(err, ErrAuthentication) {
			t.Fatalf("error = %v", err)
		}
	})
}

func TestEnvelopeAADIgnoresRewrappableMetadata(t *testing.T) {
	nonce := bytes.Repeat([]byte{0x22}, NonceSize)
	organization := UUID{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16}
	endpoint := UUID{16, 15, 14, 13, 12, 11, 10, 9, 8, 7, 6, 5, 4, 3, 2, 1}
	first := Envelope{Version: EnvelopeVersion, Algorithm: AlgorithmAES256GCM, Purpose: PurposeWebhookSecret,
		KID: "kek-1", Provider: ProviderLocalFile, WrappedDEK: []byte{0x01}, Nonce: nonce, Ciphertext: []byte("x"), Tag: make([]byte, TagSize)}
	second := first
	second.KID = "kek-2"
	second.Provider = ProviderAWSKMS
	second.WrappedDEK = []byte{0x02, 0x03}

	firstAAD, err := first.BuildAAD(organization, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	secondAAD, err := second.BuildAAD(organization, endpoint)
	if err != nil {
		t.Fatal(err)
	}
	if firstAAD != secondAAD {
		t.Fatal("AAD changed when only rewrappable metadata changed")
	}
}

func FuzzDecode(f *testing.F) {
	envelope, _ := goldenEnvelope()
	encoded, err := envelope.Encode()
	if err != nil {
		f.Fatal(err)
	}
	f.Add(encoded)
	f.Add(Prefix)
	f.Add("hfse1.AAAA")
	f.Add("")
	f.Fuzz(func(t *testing.T, value string) {
		decoded, err := Decode(value)
		if err != nil {
			return
		}
		if err := decoded.Validate(); err != nil {
			t.Fatalf("Decode returned an invalid envelope: %v", err)
		}
		reencoded, err := decoded.Encode()
		if err != nil {
			t.Fatalf("accepted envelope cannot be encoded: %v", err)
		}
		if reencoded != value {
			t.Fatalf("accepted envelope is not canonical: %q", value)
		}
	})
}

// Fresh provisioning material is sized by the approved constants and is drawn
// from the supplied CSPRNG without reuse.
func TestDEKAndNonceGeneration(t *testing.T) {
	t.Parallel()

	dek1, err := NewDEK(DefaultReader())
	if err != nil {
		t.Fatalf("NewDEK() error: %v", err)
	}
	dek2, err := NewDEK(DefaultReader())
	if err != nil {
		t.Fatalf("NewDEK() error: %v", err)
	}
	if len(dek1) != DEKSize || len(dek2) != DEKSize {
		t.Fatalf("DEK lengths = %d and %d, want %d", len(dek1), len(dek2), DEKSize)
	}
	if bytes.Equal(dek1, dek2) {
		t.Fatal("two generated DEKs are identical")
	}

	nonce1, err := NewNonce(DefaultReader())
	if err != nil {
		t.Fatalf("NewNonce() error: %v", err)
	}
	nonce2, err := NewNonce(DefaultReader())
	if err != nil {
		t.Fatalf("NewNonce() error: %v", err)
	}
	if len(nonce1) != NonceSize || len(nonce2) != NonceSize {
		t.Fatalf("nonce lengths = %d and %d, want %d", len(nonce1), len(nonce2), NonceSize)
	}
	if bytes.Equal(nonce1, nonce2) {
		t.Fatal("two generated nonces are identical")
	}
	if _, err := NewDEK(nil); err == nil {
		t.Fatal("NewDEK(nil) accepted a missing reader")
	}
	if _, err := NewNonce(nil); err == nil {
		t.Fatal("NewNonce(nil) accepted a missing reader")
	}
}
