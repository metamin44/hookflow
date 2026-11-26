package secrets

import (
	"encoding/hex"
	"errors"
	"fmt"
)

// AADSize is the exact approved associated-data length for envelope version 1.
const AADSize = 64

// AADDomain is the immutable first field of every v1 associated-data value.
const AADDomain = "hookflow.secret.v1"

// UUID is the raw 16-byte big-endian value of a canonical UUID.
type UUID [16]byte

// ParseUUID accepts the canonical 36-character hyphenated form only. It does
// not accept braces, URN prefixes, or alternate separators.
func ParseUUID(value string) (UUID, error) {
	var id UUID
	if len(value) != 36 {
		return id, errors.New("secret identity is not a canonical UUID")
	}
	for i := 0; i < len(value); i++ {
		switch i {
		case 8, 13, 18, 23:
			if value[i] != '-' {
				return id, errors.New("secret identity is not a canonical UUID")
			}
		default:
			if !isHex(value[i]) {
				return id, errors.New("secret identity is not a canonical UUID")
			}
		}
	}
	compact := value[0:8] + value[9:13] + value[14:18] + value[19:23] + value[24:36]
	if _, err := hex.Decode(id[:], []byte(compact)); err != nil {
		return UUID{}, errors.New("secret identity is not a canonical UUID")
	}
	return id, nil
}

func isHex(b byte) bool {
	return (b >= '0' && b <= '9') || (b >= 'a' && b <= 'f') || (b >= 'A' && b <= 'F')
}

// BuildAAD constructs the exact 64-byte v1 associated data:
// "hookflow.secret.v1" || alg || purpose || data_nonce || org(16) || endpoint(16).
// No length prefixes, separators, kid, provider, or wrapped-key bytes.
func BuildAAD(algorithm, purpose byte, nonce []byte, organizationID, endpointID UUID) ([AADSize]byte, error) {
	var aad [AADSize]byte
	if algorithm != AlgorithmAES256GCM {
		return aad, fmt.Errorf("%w: algorithm", ErrUnsupported)
	}
	if purpose != PurposeWebhookSecret {
		return aad, fmt.Errorf("%w: purpose", ErrUnsupported)
	}
	if len(nonce) != NonceSize {
		return aad, fmt.Errorf("%w: nonce length", ErrMalformed)
	}
	copy(aad[0:18], AADDomain)
	aad[18] = algorithm
	aad[19] = purpose
	copy(aad[20:32], nonce)
	copy(aad[32:48], organizationID[:])
	copy(aad[48:64], endpointID[:])
	return aad, nil
}

// BuildAAD binds the envelope's algorithm, purpose, and nonce to the supplied
// organization and endpoint identities.
func (e Envelope) BuildAAD(organizationID, endpointID UUID) ([AADSize]byte, error) {
	return BuildAAD(e.Algorithm, e.Purpose, e.Nonce, organizationID, endpointID)
}

// KEKContextSize is the approved local-provider wrap associated-data length:
// "hookflow.kekwrap.v1" (19) || organization (16) || endpoint (16).
const KEKContextSize = 51

// KEKWrapDomain is the immutable domain tag for local KEK wrapping.
const KEKWrapDomain = "hookflow.kekwrap.v1"

// BuildKEKWrapAAD constructs the exact 51-byte local-provider wrap context.
func BuildKEKWrapAAD(organizationID, endpointID UUID) [KEKContextSize]byte {
	var aad [KEKContextSize]byte
	copy(aad[0:19], KEKWrapDomain)
	copy(aad[19:35], organizationID[:])
	copy(aad[35:51], endpointID[:])
	return aad
}
