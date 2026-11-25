// Package kek defines the approved D6/U25 key-encryption-key boundary.
//
// The core never sees KEK material. Providers wrap and unwrap per-secret data
// keys, classify their own failures as transient or permanent, and never embed
// key material, wrapped bytes, or provider response bodies in an error.
package kek

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"errors"
	"fmt"

	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// Context is the non-secret tenant identity bound to a wrap operation.
type Context struct {
	OrganizationID [16]byte
	EndpointID     [16]byte
}

// Wrapped is the stored, provider-opaque wrapped DEK.
type Wrapped struct {
	KID      string
	Provider byte
	Blob     []byte
}

// Provider is the three-method core-facing interface. It never exposes KEK
// material and never lists or deletes keys.
type Provider interface {
	Wrap(ctx context.Context, dek []byte, c Context) (Wrapped, error)
	Unwrap(ctx context.Context, w Wrapped, c Context) ([]byte, error)
	CurrentKID() string
}

// Rewrapper is the optional rewrap capability. Providers that do not implement
// it are rewrapped by Unwrap followed by Wrap.
type Rewrapper interface {
	Rewrap(ctx context.Context, w Wrapped, c Context) (Wrapped, error)
}

// Provider identifiers. Only the local development provider is implemented by
// this build; the tags are reserved by the v1 envelope.
const (
	ProviderLocalFile = "file"
	ProviderAWSKMS    = "aws_kms"
	ProviderGCPKMS    = "gcp_kms"
	ProviderVault     = "vault_transit"
)

// NameForProvider maps a v1 provider tag to its configuration name.
func NameForProvider(provider byte) string {
	switch provider {
	case secrets.ProviderLocalFile:
		return ProviderLocalFile
	case secrets.ProviderAWSKMS:
		return ProviderAWSKMS
	case secrets.ProviderGCPKMS:
		return ProviderGCPKMS
	case secrets.ProviderVault:
		return ProviderVault
	default:
		return ""
	}
}

// ProviderTag maps a configuration name to its v1 provider tag.
func ProviderTag(name string) (byte, bool) {
	switch name {
	case ProviderLocalFile:
		return secrets.ProviderLocalFile, true
	case ProviderAWSKMS:
		return secrets.ProviderAWSKMS, true
	case ProviderGCPKMS:
		return secrets.ProviderGCPKMS, true
	case ProviderVault:
		return secrets.ProviderVault, true
	default:
		return 0, false
	}
}

// AvailableProvider reports whether this build implements the named adapter.
// Only the development file provider is implemented; cloud adapters are
// reserved identifiers and require a future deployment adapter.
func AvailableProvider(name string) bool { return name == ProviderLocalFile }

// Failure classes. A provider adapter must classify every error it returns as
// exactly one of these; callers must not infer permanence from message text.
var (
	ErrTransient           = errors.New("key encryption provider is temporarily unavailable")
	ErrPermanent           = errors.New("key encryption provider rejected the request")
	ErrUnknownKID          = errors.New("key encryption key id is unknown")
	ErrUnsupportedProvider = errors.New("key encryption provider tag is unsupported")
	ErrInvalidWrappedKey   = errors.New("wrapped data key is invalid")
	ErrAuthentication      = errors.New("wrapped data key authentication failed")
	ErrKeyring             = errors.New("key encryption keyring is unusable")
)

// IsTransient reports whether err carries the approved transient class.
func IsTransient(err error) bool { return errors.Is(err, ErrTransient) }

// IsPermanent reports whether err carries an explicit permanent class.
func IsPermanent(err error) bool { return errors.Is(err, ErrPermanent) }

// wrapLocal wraps a DEK under a local 32-byte KEK using the approved local blob
// layout: 0x01 || wrap_nonce(12) || AES-256-GCM(KEK, DEK).
func wrapLocal(kek, dek []byte, nonce, aad []byte) ([]byte, error) {
	if len(kek) != secrets.DEKSize {
		return nil, fmt.Errorf("%w: KEK length", ErrKeyring)
	}
	if len(dek) != secrets.DEKSize {
		return nil, fmt.Errorf("%w: data key length", ErrInvalidWrappedKey)
	}
	if len(nonce) != secrets.NonceSize {
		return nil, fmt.Errorf("%w: wrap nonce length", ErrInvalidWrappedKey)
	}
	aead, err := newLocalGCM(kek)
	if err != nil {
		return nil, err
	}
	sealed := aead.Seal(nil, nonce, dek, aad)
	blob := make([]byte, 0, 1+len(nonce)+len(sealed))
	blob = append(blob, 0x01)
	blob = append(blob, nonce...)
	blob = append(blob, sealed...)
	return blob, nil
}

// unwrapLocal reverses wrapLocal. Any authentication failure is ErrAuthentication.
func unwrapLocal(kek, blob, aad []byte) ([]byte, error) {
	if len(kek) != secrets.DEKSize {
		return nil, fmt.Errorf("%w: KEK length", ErrKeyring)
	}
	const localBlobLength = 1 + secrets.NonceSize + secrets.DEKSize + secrets.TagSize
	if len(blob) != localBlobLength {
		return nil, fmt.Errorf("%w: local wrapped length", ErrInvalidWrappedKey)
	}
	if blob[0] != 0x01 {
		return nil, fmt.Errorf("%w: local wrapped version", ErrInvalidWrappedKey)
	}
	nonce := blob[1 : 1+secrets.NonceSize]
	sealed := blob[1+secrets.NonceSize:]
	aead, err := newLocalGCM(kek)
	if err != nil {
		return nil, err
	}
	dek, err := aead.Open(nil, nonce, sealed, aad)
	if err != nil {
		return nil, ErrAuthentication
	}
	return dek, nil
}

func newLocalGCM(kek []byte) (cipher.AEAD, error) {
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, fmt.Errorf("%w: cipher construction", ErrKeyring)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: GCM construction", ErrKeyring)
	}
	return aead, nil
}

func contextDone(ctx context.Context) error {
	if ctx == nil {
		return errors.New("key provider context is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: context", ErrTransient)
	}
	return nil
}
