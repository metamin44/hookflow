package kek

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"

	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// MemoryProvider is the in-memory test provider. It implements the same local
// wrap format and failure classification as the development file provider.
type MemoryProvider struct {
	current string
	keys    map[string][]byte
	reader  io.Reader
}

// NewMemoryProvider clones the supplied key material. Keys must be 32 bytes.
func NewMemoryProvider(current string, keys map[string][]byte, reader io.Reader) (*MemoryProvider, error) {
	if current == "" {
		return nil, fmt.Errorf("%w: current kid is required", ErrKeyring)
	}
	if err := validKID(current); err != nil {
		return nil, err
	}
	cloned := make(map[string][]byte, len(keys))
	for kid, key := range keys {
		if err := validKID(kid); err != nil {
			clearKeys(cloned)
			return nil, err
		}
		if len(key) != secrets.DEKSize {
			clearKeys(cloned)
			return nil, fmt.Errorf("%w: key material length", ErrKeyring)
		}
		cloned[kid] = append([]byte(nil), key...)
	}
	if _, ok := cloned[current]; !ok {
		clearKeys(cloned)
		return nil, fmt.Errorf("%w: current kid is not present", ErrKeyring)
	}
	if reader == nil {
		reader = rand.Reader
	}
	return &MemoryProvider{current: current, keys: cloned, reader: reader}, nil
}

// CurrentKID returns the active test wrapping key id.
func (p *MemoryProvider) CurrentKID() string { return p.current }

// Wrap wraps dek under the current test KEK.
func (p *MemoryProvider) Wrap(ctx context.Context, dek []byte, c Context) (Wrapped, error) {
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

// Unwrap unwraps exactly the stored test blob under the recorded kid.
func (p *MemoryProvider) Unwrap(ctx context.Context, w Wrapped, c Context) ([]byte, error) {
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

// Rewrap re-wraps the same test DEK under the current KEK.
func (p *MemoryProvider) Rewrap(ctx context.Context, w Wrapped, c Context) (Wrapped, error) {
	dek, err := p.Unwrap(ctx, w, c)
	if err != nil {
		return Wrapped{}, err
	}
	defer clear(dek)
	return p.Wrap(ctx, dek, c)
}
