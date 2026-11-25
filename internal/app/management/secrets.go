package management

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"

	"github.com/metamin44/hookflow/internal/platform/kek"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// secretTokenPrefix is the customer-facing one-time secret prefix.
const secretTokenPrefix = "whsec_"

// secretMaterial holds the one-time token and the persisted envelope. The raw
// key is cleared before return.
type secretMaterial struct {
	Token    string
	Envelope string
}

// newSecret builds a fresh 32-byte webhook secret, encrypts it under a fresh
// DEK, and wraps the DEK through the configured provider. Provider I/O happens
// with no database lock held.
func (s *Service) newSecret(ctx context.Context, organizationID, endpointID string) (secretMaterial, error) {
	orgID, err := secrets.ParseUUID(organizationID)
	if err != nil {
		return secretMaterial{}, err
	}
	epID, err := secrets.ParseUUID(endpointID)
	if err != nil {
		return secretMaterial{}, err
	}
	raw := make([]byte, secrets.DEKSize)
	if _, err := io.ReadFull(s.random, raw); err != nil {
		return secretMaterial{}, fmt.Errorf("generate webhook secret: %w", err)
	}
	defer clear(raw)

	dek, err := secrets.NewDEK(s.random)
	if err != nil {
		return secretMaterial{}, err
	}
	defer clear(dek)
	nonce, err := secrets.NewNonce(s.random)
	if err != nil {
		return secretMaterial{}, err
	}
	aad, err := secrets.BuildAAD(secrets.AlgorithmAES256GCM, secrets.PurposeWebhookSecret, nonce, orgID, epID)
	if err != nil {
		return secretMaterial{}, err
	}
	ciphertext, tag, err := secrets.Seal(dek, nonce, raw, aad[:])
	if err != nil {
		return secretMaterial{}, err
	}
	wrapped, err := s.provider.Wrap(ctx, dek, kek.Context{OrganizationID: [16]byte(orgID), EndpointID: [16]byte(epID)})
	if err != nil {
		return secretMaterial{}, fmt.Errorf("wrap webhook secret: %w", err)
	}
	envelope, err := (secrets.Envelope{
		Version:    secrets.EnvelopeVersion,
		Algorithm:  secrets.AlgorithmAES256GCM,
		Purpose:    secrets.PurposeWebhookSecret,
		KID:        wrapped.KID,
		Provider:   wrapped.Provider,
		WrappedDEK: wrapped.Blob,
		Nonce:      nonce,
		Ciphertext: ciphertext,
		Tag:        tag,
	}).Encode()
	if err != nil {
		return secretMaterial{}, err
	}
	return secretMaterial{
		Token:    secretTokenPrefix + base64.StdEncoding.EncodeToString(raw),
		Envelope: envelope,
	}, nil
}
