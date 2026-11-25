package apikey

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"math"
	"strconv"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Approved Argon2id parameters and verifier bounds.
const (
	ArgonVersion   = 19
	ArgonMemory    = 19456
	ArgonTime      = 2
	ArgonThreads   = 1
	ArgonSaltSize  = 16
	ArgonKeySize   = 32
	MaxPHCLength   = 256
	MinMemory      = 19456
	MaxMemory      = 32768
	MinTime        = 2
	MaxTime        = 4
	MinThreads     = 1
	MaxThreads     = 2
	MaxConcurrent  = 4
	hashAlgorithm  = "argon2id"
	phcPrefix      = "$argon2id$"
	verifierFields = 6
)

// Verifier errors are static and never carry token, salt or hash bytes.
var (
	// ErrMismatch reports a token that does not verify.
	ErrMismatch = errors.New("api key does not verify")
	// ErrInvalidVerifier reports a stored verifier outside the approved bounds.
	ErrInvalidVerifier = errors.New("api key verifier is invalid")
)

// Hash returns the canonical Argon2id PHC verifier for a validated token.
func Hash(token string) (string, error) {
	return HashWithReader(token, rand.Reader)
}

// HashWithReader is the deterministic-seam variant used by tests.
func HashWithReader(token string, reader io.Reader) (string, error) {
	if _, err := ParseToken(token); err != nil {
		return "", err
	}
	if reader == nil {
		reader = rand.Reader
	}
	salt := make([]byte, ArgonSaltSize)
	if _, err := io.ReadFull(reader, salt); err != nil {
		return "", fmt.Errorf("generate api key salt: %w", err)
	}
	key := argon2.IDKey([]byte(token), salt, ArgonTime, ArgonMemory, ArgonThreads, ArgonKeySize)
	defer clear(key)
	return fmt.Sprintf("%s$v=%d$m=%d,t=%d,p=%d$%s$%s",
		phcPrefix[:len(phcPrefix)-1], ArgonVersion, ArgonMemory, ArgonTime, ArgonThreads,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key),
	), nil
}

// Algorithm reports the stored hash_algorithm value for new verifiers.
func Algorithm() string { return hashAlgorithm }

type params struct {
	memory      uint32
	time        uint32
	threads     uint8
	salt        []byte
	expectedKey []byte
}

// Verifier is the bounded Argon2id verification boundary. It caps concurrent
// derivations at MaxConcurrent; callers must construct it once per process.
type Verifier struct {
	sem chan struct{}
}

// NewVerifier builds a verifier with the approved concurrency bound.
func NewVerifier() *Verifier {
	return &Verifier{sem: make(chan struct{}, MaxConcurrent)}
}

// Verify checks a token against a stored PHC verifier within the approved
// parameter bounds. Malformed verifiers are rejected without derivation.
func (v *Verifier) Verify(ctx context.Context, token, encoded string) error {
	if _, err := ParseToken(token); err != nil {
		return ErrMismatch
	}
	parsed, err := parsePHC(encoded)
	if err != nil {
		return err
	}
	defer clear(parsed.expectedKey)
	if err := v.acquire(ctx); err != nil {
		return err
	}
	defer v.release()
	derived := argon2.IDKey([]byte(token), parsed.salt, parsed.time, parsed.memory, parsed.threads, ArgonKeySize)
	defer clear(derived)
	if subtle.ConstantTimeCompare(derived, parsed.expectedKey) != 1 {
		return ErrMismatch
	}
	return nil
}

// DummyVerify performs one full-cost derivation for an unknown prefix so that
// enumeration through response timing gains no signal.
func (v *Verifier) DummyVerify(ctx context.Context, token string) error {
	if err := v.acquire(ctx); err != nil {
		return err
	}
	defer v.release()
	salt := [ArgonSaltSize]byte{0x48, 0x6f, 0x6f, 0x6b, 0x46, 0x6c, 0x6f, 0x77, 0x64, 0x75, 0x6d, 0x6d, 0x79, 0x73, 0x61, 0x6c}
	derived := argon2.IDKey([]byte(token), salt[:], ArgonTime, ArgonMemory, ArgonThreads, ArgonKeySize)
	defer clear(derived)
	return nil
}

func (v *Verifier) acquire(ctx context.Context) error {
	if ctx != nil {
		select {
		case v.sem <- struct{}{}:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	v.sem <- struct{}{}
	return nil
}

func (v *Verifier) release() { <-v.sem }

func parsePHC(encoded string) (params, error) {
	if encoded == "" || len(encoded) > MaxPHCLength {
		return params{}, ErrInvalidVerifier
	}
	parts := strings.Split(encoded, "$")
	if len(parts) != verifierFields || parts[0] != "" || parts[1] != hashAlgorithm {
		return params{}, ErrInvalidVerifier
	}
	version, err := literalVersion(parts[2])
	if err != nil {
		return params{}, err
	}
	if version != ArgonVersion {
		return params{}, ErrInvalidVerifier
	}
	parsed, err := parseParams(parts[3])
	if err != nil {
		return params{}, err
	}
	salt, err := decodeCanonical(parts[4])
	if err != nil || len(salt) != ArgonSaltSize {
		return params{}, ErrInvalidVerifier
	}
	key, err := decodeCanonical(parts[5])
	if err != nil || len(key) != ArgonKeySize {
		return params{}, ErrInvalidVerifier
	}
	parsed.salt = salt
	parsed.expectedKey = key
	return parsed, nil
}

func literalVersion(value string) (int, error) {
	if !strings.HasPrefix(value, "v=") {
		return 0, ErrInvalidVerifier
	}
	version, err := strconv.Atoi(strings.TrimPrefix(value, "v="))
	if err != nil {
		return 0, ErrInvalidVerifier
	}
	return version, nil
}

func parseParams(value string) (params, error) {
	fields := strings.Split(value, ",")
	if len(fields) != 3 {
		return params{}, ErrInvalidVerifier
	}
	memory, err := boundedUint(fields[0], "m=", MinMemory, MaxMemory)
	if err != nil {
		return params{}, err
	}
	iterations, err := boundedUint(fields[1], "t=", MinTime, MaxTime)
	if err != nil {
		return params{}, err
	}
	threads, err := boundedUint(fields[2], "p=", MinThreads, MaxThreads)
	if err != nil {
		return params{}, err
	}
	// Explicit narrowing proofs at the conversion site; the bounds constants
	// keep these branches unreachable, and the checks keep the conversions
	// auditable without a linter suppression.
	if memory < 0 || memory > math.MaxUint32 || iterations < 0 || iterations > math.MaxUint32 ||
		threads < 0 || threads > math.MaxUint8 {
		return params{}, ErrInvalidVerifier
	}
	return params{
		memory:  uint32(memory),
		time:    uint32(iterations),
		threads: uint8(threads),
	}, nil
}

func boundedUint(field, prefix string, minimum, maximum int) (int, error) {
	if !strings.HasPrefix(field, prefix) {
		return 0, ErrInvalidVerifier
	}
	value, err := strconv.Atoi(strings.TrimPrefix(field, prefix))
	if err != nil || value < minimum || value > maximum {
		return 0, ErrInvalidVerifier
	}
	return value, nil
}

func decodeCanonical(value string) ([]byte, error) {
	decoded, err := base64.RawStdEncoding.Strict().DecodeString(value)
	if err != nil {
		return nil, ErrInvalidVerifier
	}
	if base64.RawStdEncoding.EncodeToString(decoded) != value {
		return nil, ErrInvalidVerifier
	}
	return decoded, nil
}
