package kek

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"

	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// FileProvider is the development-only local keyring provider.
//
// The ring is read once at construction; a missing, unreadable, malformed,
// wrongly-sized, or insecurely permissioned ring fails startup. Key material is
// held in memory and never formatted, logged, or echoed through an error.
type FileProvider struct {
	current string
	keys    map[string][]byte
	reader  io.Reader
}

// FileOptions are explicit construction seams. Production callers leave them
// zero; tests may supply deterministic randomness.
type FileOptions struct {
	Reader io.Reader
}

// NewFileProvider reads and strictly validates a local keyring file.
func NewFileProvider(path, currentKID string, opts FileOptions) (*FileProvider, error) {
	if path == "" {
		return nil, fmt.Errorf("%w: keyring path is required", ErrKeyring)
	}
	if currentKID == "" {
		return nil, fmt.Errorf("%w: current kid is required", ErrKeyring)
	}
	if err := validKID(currentKID); err != nil {
		return nil, err
	}
	raw, err := readKeyringFile(path)
	if err != nil {
		return nil, err
	}
	defer clear(raw)
	keys, err := parseKeyring(raw)
	if err != nil {
		return nil, err
	}
	if _, ok := keys[currentKID]; !ok {
		clearKeys(keys)
		return nil, fmt.Errorf("%w: current kid is not present in the keyring", ErrKeyring)
	}
	reader := opts.Reader
	if reader == nil {
		reader = rand.Reader
	}
	return &FileProvider{current: currentKID, keys: keys, reader: reader}, nil
}

// CurrentKID returns the configured active wrapping key id.
func (p *FileProvider) CurrentKID() string { return p.current }

// ValidateKIDs fails when a configured allowed kid is absent from the ring.
func (p *FileProvider) ValidateKIDs(kids []string) error {
	for _, kid := range kids {
		if err := validKID(kid); err != nil {
			return err
		}
		if _, ok := p.keys[kid]; !ok {
			return fmt.Errorf("%w: configured kid is not present in the keyring", ErrKeyring)
		}
	}
	return nil
}

// Wrap wraps dek under the current KEK.
func (p *FileProvider) Wrap(ctx context.Context, dek []byte, c Context) (Wrapped, error) {
	if err := contextDone(ctx); err != nil {
		return Wrapped{}, err
	}
	key, ok := p.keys[p.current]
	if !ok {
		return Wrapped{}, fmt.Errorf("%w: current kid is unavailable", ErrKeyring)
	}
	nonce := make([]byte, secrets.NonceSize)
	if _, err := io.ReadFull(p.reader, nonce); err != nil {
		return Wrapped{}, fmt.Errorf("%w: wrap randomness", ErrTransient)
	}
	aad := secrets.BuildKEKWrapAAD(secrets.UUID(c.OrganizationID), secrets.UUID(c.EndpointID))
	blob, err := wrapLocal(key, dek, nonce, aad[:])
	if err != nil {
		return Wrapped{}, err
	}
	return Wrapped{KID: p.current, Provider: secrets.ProviderLocalFile, Blob: blob}, nil
}

// Unwrap unwraps exactly the stored blob under exactly the recorded kid.
func (p *FileProvider) Unwrap(ctx context.Context, w Wrapped, c Context) ([]byte, error) {
	if err := contextDone(ctx); err != nil {
		return nil, err
	}
	if w.Provider != secrets.ProviderLocalFile {
		return nil, fmt.Errorf("%w: provider tag", ErrUnsupportedProvider)
	}
	if err := validKID(w.KID); err != nil {
		return nil, ErrInvalidWrappedKey
	}
	key, ok := p.keys[w.KID]
	if !ok {
		return nil, fmt.Errorf("%w: wrapped kid is unknown", ErrUnknownKID)
	}
	aad := secrets.BuildKEKWrapAAD(secrets.UUID(c.OrganizationID), secrets.UUID(c.EndpointID))
	dek, err := unwrapLocal(key, w.Blob, aad[:])
	if err != nil {
		return nil, err
	}
	if len(dek) != secrets.DEKSize {
		clear(dek)
		return nil, ErrInvalidWrappedKey
	}
	return dek, nil
}

// Rewrap re-wraps the same DEK under the current KEK without re-encrypting the
// payload. The data ciphertext, nonce, and tag are untouched by the caller.
func (p *FileProvider) Rewrap(ctx context.Context, w Wrapped, c Context) (Wrapped, error) {
	dek, err := p.Unwrap(ctx, w, c)
	if err != nil {
		return Wrapped{}, err
	}
	defer clear(dek)
	return p.Wrap(ctx, dek, c)
}

func readKeyringFile(path string) ([]byte, error) {
	// #nosec G304 -- keyring path is operator-supplied startup configuration validated as a regular file.
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("%w: keyring file cannot be opened", ErrKeyring)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("%w: keyring file cannot be inspected", ErrKeyring)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%w: keyring path is not a regular file", ErrKeyring)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o077 != 0 {
		return nil, fmt.Errorf("%w: keyring file permissions must be 0600", ErrKeyring)
	}
	raw, err := io.ReadAll(io.LimitReader(file, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("%w: keyring file cannot be read", ErrKeyring)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("%w: keyring file is empty", ErrKeyring)
	}
	return raw, nil
}

func parseKeyring(raw []byte) (map[string][]byte, error) {
	var top map[string]json.RawMessage
	if err := json.Unmarshal(raw, &top); err != nil || top == nil {
		return nil, fmt.Errorf("%w: keyring is not a JSON object", ErrKeyring)
	}
	if len(top) != 2 {
		return nil, fmt.Errorf("%w: keyring has unknown or missing fields", ErrKeyring)
	}
	currentRaw, ok := top["current"]
	if !ok {
		return nil, fmt.Errorf("%w: keyring current is missing", ErrKeyring)
	}
	keysRaw, ok := top["keys"]
	if !ok {
		return nil, fmt.Errorf("%w: keyring keys are missing", ErrKeyring)
	}
	var current string
	if err := json.Unmarshal(currentRaw, &current); err != nil || current == "" {
		return nil, fmt.Errorf("%w: keyring current is invalid", ErrKeyring)
	}
	encoded, err := parseKeyEntries(keysRaw)
	if err != nil {
		return nil, err
	}
	keys := make(map[string][]byte, len(encoded))
	for kid, value := range encoded {
		if err := validKID(kid); err != nil {
			clearKeys(keys)
			return nil, fmt.Errorf("%w: keyring kid is invalid", ErrKeyring)
		}
		decoded, err := base64.RawURLEncoding.Strict().DecodeString(value)
		if err != nil || len(decoded) != secrets.DEKSize {
			clear(decoded)
			clearKeys(keys)
			return nil, fmt.Errorf("%w: keyring key material is invalid", ErrKeyring)
		}
		if base64.RawURLEncoding.EncodeToString(decoded) != value {
			clear(decoded)
			clearKeys(keys)
			return nil, fmt.Errorf("%w: keyring key material is not canonical", ErrKeyring)
		}
		keys[kid] = decoded
	}
	if _, ok := keys[current]; !ok {
		clearKeys(keys)
		return nil, fmt.Errorf("%w: current kid is not present in the keyring", ErrKeyring)
	}
	return keys, nil
}

// parseKeyEntries rejects duplicate kid definitions, which a plain map decode
// would silently collapse.
func parseKeyEntries(raw json.RawMessage) (map[string]string, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	token, err := decoder.Token()
	if err != nil || token != json.Delim('{') {
		return nil, fmt.Errorf("%w: keyring keys are not an object", ErrKeyring)
	}
	entries := make(map[string]string)
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			return nil, fmt.Errorf("%w: keyring keys are malformed", ErrKeyring)
		}
		kid, ok := token.(string)
		if !ok {
			return nil, fmt.Errorf("%w: keyring kid is malformed", ErrKeyring)
		}
		if _, duplicate := entries[kid]; duplicate {
			return nil, fmt.Errorf("%w: keyring contains a duplicate kid", ErrKeyring)
		}
		var value string
		if err := decoder.Decode(&value); err != nil {
			return nil, fmt.Errorf("%w: keyring key material is malformed", ErrKeyring)
		}
		entries[kid] = value
	}
	if token, err := decoder.Token(); err != nil || token != json.Delim('}') {
		return nil, fmt.Errorf("%w: keyring keys are malformed", ErrKeyring)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("%w: keyring has trailing data", ErrKeyring)
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("%w: keyring has no keys", ErrKeyring)
	}
	return entries, nil
}

func validKID(kid string) error {
	if kid == "" || len(kid) > secrets.MaxKIDLength {
		return fmt.Errorf("%w: kid length", ErrKeyring)
	}
	for i := 0; i < len(kid); i++ {
		b := kid[i]
		if (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') ||
			b == '.' || b == '_' || b == ':' || b == '-' {
			continue
		}
		return fmt.Errorf("%w: kid alphabet", ErrKeyring)
	}
	return nil
}

func clearKeys(keys map[string][]byte) {
	for _, key := range keys {
		clear(key)
	}
}
