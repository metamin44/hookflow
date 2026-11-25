// Package secrets implements the approved D6/U25 version-1 secret envelope.
//
// It is a pure codec plus the approved AES-256-GCM data path: it performs no
// provider I/O and never logs, formats, or returns plaintext, wrapped bytes or
// key material in an error. Errors carry a static reason only.
package secrets

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// Storage and wire constants for envelope version 1.
const (
	Prefix = "hfse1."

	EnvelopeVersion byte = 0x01

	AlgorithmAES256GCM   byte = 0x01
	PurposeWebhookSecret byte = 0x01

	ProviderLocalFile byte = 0x01
	ProviderAWSKMS    byte = 0x02
	ProviderGCPKMS    byte = 0x03
	ProviderVault     byte = 0x04

	DEKSize   = 32
	NonceSize = 12
	TagSize   = 16

	MinKIDLength = 1
	MaxKIDLength = 64

	MinWrappedLength = 1
	MaxWrappedLength = 1024

	MinPlaintextLength = 1
	MaxPlaintextLength = 1024

	maxDecodedLength = 4096
	// maxEncodedLength is the unpadded base64url encoding of 4,096 bytes plus
	// the five-byte prefix. Input above it is rejected before decoding.
	maxEncodedLength = 5468
	// maxV1DecodedLength is the structural v1 ceiling
	// 39 + kid_len + wrapped_len + n = 39 + 64 + 1024 + 1024.
	maxV1DecodedLength = 2151
)

// Static parse and crypto errors. They never embed envelope or key bytes.
var (
	ErrMalformed      = errors.New("secret envelope is malformed")
	ErrUnsupported    = errors.New("secret envelope is unsupported")
	ErrAuthentication = errors.New("secret envelope authentication failed")
	ErrInvalidKey     = errors.New("secret envelope key material is invalid")
)

// Envelope is a decoded version-1 envelope. Decode fully validates it.
type Envelope struct {
	Version    byte
	Algorithm  byte
	Purpose    byte
	KID        string
	Provider   byte
	WrappedDEK []byte
	Nonce      []byte
	Ciphertext []byte
	Tag        []byte
}

// Validate reports whether every field satisfies the fixed v1 constraints.
func (e Envelope) Validate() error {
	if e.Version != EnvelopeVersion {
		return fmt.Errorf("%w: envelope version", ErrUnsupported)
	}
	if e.Algorithm != AlgorithmAES256GCM {
		return fmt.Errorf("%w: algorithm", ErrUnsupported)
	}
	if e.Purpose != PurposeWebhookSecret {
		return fmt.Errorf("%w: purpose", ErrUnsupported)
	}
	if !ValidProvider(e.Provider) {
		return fmt.Errorf("%w: provider tag", ErrUnsupported)
	}
	if len(e.KID) < MinKIDLength || len(e.KID) > MaxKIDLength {
		return fmt.Errorf("%w: kid length", ErrMalformed)
	}
	for i := 0; i < len(e.KID); i++ {
		if !validKIDByte(e.KID[i]) {
			return fmt.Errorf("%w: kid alphabet", ErrMalformed)
		}
	}
	if len(e.WrappedDEK) < MinWrappedLength || len(e.WrappedDEK) > MaxWrappedLength {
		return fmt.Errorf("%w: wrapped length", ErrMalformed)
	}
	if len(e.Nonce) != NonceSize {
		return fmt.Errorf("%w: nonce length", ErrMalformed)
	}
	if len(e.Ciphertext) < MinPlaintextLength || len(e.Ciphertext) > MaxPlaintextLength {
		return fmt.Errorf("%w: ciphertext length", ErrMalformed)
	}
	if len(e.Tag) != TagSize {
		return fmt.Errorf("%w: tag length", ErrMalformed)
	}
	return nil
}

// ValidProvider reports whether the provider tag is defined for v1.
func ValidProvider(provider byte) bool {
	switch provider {
	case ProviderLocalFile, ProviderAWSKMS, ProviderGCPKMS, ProviderVault:
		return true
	default:
		return false
	}
}

func validKIDByte(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
		b == '.' || b == '_' || b == ':' || b == '-'
}

// Encode validates the envelope and renders the canonical storage form:
// the exact prefix followed by unpadded base64url.
func (e Envelope) Encode() (string, error) {
	if err := e.Validate(); err != nil {
		return "", err
	}
	total := 39 + len(e.KID) + len(e.WrappedDEK) + len(e.Ciphertext)
	if total > maxV1DecodedLength {
		return "", fmt.Errorf("%w: decoded envelope is too large", ErrUnsupported)
	}
	buf := make([]byte, 0, total)
	buf = append(buf, 'H', 'F', 'S', 'E')
	//nolint:gosec // Validate bounds the KID length before this conversion.
	buf = append(buf, e.Version, e.Algorithm, e.Purpose, byte(len(e.KID)))
	buf = append(buf, e.KID...)
	buf = append(buf, e.Provider)
	var length [2]byte
	//nolint:gosec // Validate bounds the wrapped length before this conversion.
	binary.BigEndian.PutUint16(length[:], uint16(len(e.WrappedDEK)))
	buf = append(buf, length[:]...)
	buf = append(buf, e.WrappedDEK...)
	buf = append(buf, e.Nonce...)
	buf = append(buf, e.Ciphertext...)
	buf = append(buf, e.Tag...)
	if len(buf) != total {
		return "", fmt.Errorf("%w: encoded length mismatch", ErrMalformed)
	}
	return Prefix + base64.RawURLEncoding.EncodeToString(buf), nil
}

// Decode applies the strict v1 parser. It never reports envelope bytes.
func Decode(value string) (Envelope, error) {
	if value == "" {
		return Envelope{}, fmt.Errorf("%w: empty input", ErrMalformed)
	}
	if len(value) > maxEncodedLength {
		return Envelope{}, fmt.Errorf("%w: encoded length", ErrMalformed)
	}
	if len(value) <= len(Prefix) || value[:len(Prefix)] != Prefix {
		return Envelope{}, fmt.Errorf("%w: prefix", ErrMalformed)
	}
	encoded := value[len(Prefix):]
	decoded, err := base64.RawURLEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return Envelope{}, fmt.Errorf("%w: base64url", ErrMalformed)
	}
	if len(decoded) > maxDecodedLength {
		return Envelope{}, fmt.Errorf("%w: decoded length", ErrMalformed)
	}
	// Reject non-canonical encodings that Strict does not already refuse.
	if base64.RawURLEncoding.EncodeToString(decoded) != encoded {
		return Envelope{}, fmt.Errorf("%w: non-canonical base64url", ErrMalformed)
	}
	return parseDecoded(decoded)
}

func parseDecoded(buf []byte) (Envelope, error) {
	// 4 magic + 1 version + 1 alg + 1 purpose + 1 kid_len + 1 provider +
	// 2 wrapped_len + 12 nonce + 16 tag + at least one ciphertext byte.
	const minimum = 39 + MinKIDLength + MinWrappedLength + MinPlaintextLength
	if len(buf) < minimum {
		return Envelope{}, fmt.Errorf("%w: truncated header", ErrMalformed)
	}
	if len(buf) > maxV1DecodedLength {
		return Envelope{}, fmt.Errorf("%w: decoded length", ErrMalformed)
	}
	if string(buf[:4]) != "HFSE" {
		return Envelope{}, fmt.Errorf("%w: magic", ErrMalformed)
	}
	var envelope Envelope
	envelope.Version = buf[4]
	envelope.Algorithm = buf[5]
	envelope.Purpose = buf[6]
	kidLength := int(buf[7])
	if kidLength < MinKIDLength || kidLength > MaxKIDLength {
		return Envelope{}, fmt.Errorf("%w: kid length", ErrMalformed)
	}
	offset := 8
	if len(buf) < offset+kidLength+1+2+NonceSize+MinPlaintextLength+TagSize {
		return Envelope{}, fmt.Errorf("%w: truncated kid", ErrMalformed)
	}
	envelope.KID = string(buf[offset : offset+kidLength])
	offset += kidLength
	envelope.Provider = buf[offset]
	offset++
	wrappedLength := int(binary.BigEndian.Uint16(buf[offset : offset+2]))
	offset += 2
	if wrappedLength < MinWrappedLength || wrappedLength > MaxWrappedLength {
		return Envelope{}, fmt.Errorf("%w: wrapped length", ErrMalformed)
	}
	if len(buf) < offset+wrappedLength+NonceSize+MinPlaintextLength+TagSize {
		return Envelope{}, fmt.Errorf("%w: truncated wrapped key", ErrMalformed)
	}
	envelope.WrappedDEK = append([]byte(nil), buf[offset:offset+wrappedLength]...)
	offset += wrappedLength
	envelope.Nonce = append([]byte(nil), buf[offset:offset+NonceSize]...)
	offset += NonceSize
	ciphertextLength := len(buf) - offset - TagSize
	if ciphertextLength < MinPlaintextLength || ciphertextLength > MaxPlaintextLength {
		return Envelope{}, fmt.Errorf("%w: ciphertext length", ErrMalformed)
	}
	envelope.Ciphertext = append([]byte(nil), buf[offset:offset+ciphertextLength]...)
	offset += ciphertextLength
	envelope.Tag = append([]byte(nil), buf[offset:]...)
	if len(envelope.Tag) != TagSize {
		return Envelope{}, fmt.Errorf("%w: tag length", ErrMalformed)
	}
	if err := envelope.Validate(); err != nil {
		return Envelope{}, err
	}
	return envelope, nil
}

// NewDEK reads exactly 32 bytes from reader. Production callers pass
// crypto/rand.Reader; tests may pass a deterministic reader.
func NewDEK(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("secret DEK reader is required")
	}
	dek := make([]byte, DEKSize)
	if _, err := io.ReadFull(reader, dek); err != nil {
		clear(dek)
		return nil, fmt.Errorf("generate secret data key: %w", err)
	}
	return dek, nil
}

// NewNonce reads exactly 12 bytes from reader.
func NewNonce(reader io.Reader) ([]byte, error) {
	if reader == nil {
		return nil, errors.New("secret nonce reader is required")
	}
	nonce := make([]byte, NonceSize)
	if _, err := io.ReadFull(reader, nonce); err != nil {
		return nil, fmt.Errorf("generate secret data nonce: %w", err)
	}
	return nonce, nil
}

// DefaultReader is the production cryptographic randomness source.
func DefaultReader() io.Reader { return rand.Reader }

// Seal encrypts plaintext under a 32-byte DEK with the approved 12-byte nonce
// and 64-byte AAD. It returns the ciphertext and the 16-byte tag separately.
func Seal(dek, nonce, plaintext, aad []byte) (ciphertext, tag []byte, err error) {
	if len(dek) != DEKSize {
		return nil, nil, fmt.Errorf("%w: data key length", ErrInvalidKey)
	}
	if len(nonce) != NonceSize {
		return nil, nil, fmt.Errorf("%w: nonce length", ErrInvalidKey)
	}
	if len(aad) != AADSize {
		return nil, nil, fmt.Errorf("%w: associated data length", ErrInvalidKey)
	}
	if len(plaintext) < MinPlaintextLength || len(plaintext) > MaxPlaintextLength {
		return nil, nil, fmt.Errorf("%w: plaintext length", ErrMalformed)
	}
	aead, err := newGCM(dek)
	if err != nil {
		return nil, nil, err
	}
	sealed := aead.Seal(nil, nonce, plaintext, aad)
	if len(sealed) != len(plaintext)+TagSize {
		return nil, nil, fmt.Errorf("%w: sealed length", ErrMalformed)
	}
	ciphertext = sealed[:len(sealed)-TagSize]
	tag = sealed[len(sealed)-TagSize:]
	return ciphertext, tag, nil
}

// Open authenticates and decrypts one v1 ciphertext. Any authentication
// failure is reported as ErrAuthentication without detail.
func Open(dek, nonce, ciphertext, tag, aad []byte) ([]byte, error) {
	if len(dek) != DEKSize || len(nonce) != NonceSize || len(tag) != TagSize || len(aad) != AADSize {
		return nil, fmt.Errorf("%w: input length", ErrInvalidKey)
	}
	if len(ciphertext) < MinPlaintextLength || len(ciphertext) > MaxPlaintextLength {
		return nil, fmt.Errorf("%w: ciphertext length", ErrMalformed)
	}
	aead, err := newGCM(dek)
	if err != nil {
		return nil, err
	}
	sealed := make([]byte, 0, len(ciphertext)+TagSize)
	sealed = append(sealed, ciphertext...)
	sealed = append(sealed, tag...)
	plaintext, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, ErrAuthentication
	}
	return plaintext, nil
}

func newGCM(dek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(dek)
	if err != nil {
		return nil, fmt.Errorf("%w: cipher construction", ErrInvalidKey)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: GCM construction", ErrInvalidKey)
	}
	return aead, nil
}
