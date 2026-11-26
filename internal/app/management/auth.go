package management

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/metamin44/hookflow/internal/modules/apikey"
)

// Authenticator resolves a Bearer API key to a verified tenant identity.
// It deliberately performs one Argon2id derivation for unknown prefixes and
// for rejected keys so response timing does not reveal key existence.
type Authenticator struct {
	keys     apikey.Querier
	verifier *apikey.Verifier
	now      func() time.Time
}

// NewAuthenticator builds the authentication boundary.
func NewAuthenticator(keys apikey.Querier, verifier *apikey.Verifier) (*Authenticator, error) {
	if keys == nil {
		return nil, errors.New("management: api key store is required")
	}
	if verifier == nil {
		return nil, errors.New("management: api key verifier is required")
	}
	return &Authenticator{keys: keys, verifier: verifier, now: time.Now}, nil
}

// Authenticate verifies the Authorization header and returns the tenant
// identity plus its effective scopes. Organization state is reported to the
// caller; suspended and archived organizations authenticate so that their
// read-only operations keep working, and writes are enforced per operation.
func (a *Authenticator) Authenticate(ctx context.Context, authorization string) (Identity, apikey.Set, error) {
	token, ok := bearerToken(authorization)
	if !ok {
		return Identity{}, apikey.Set{}, unauthorized("invalid_api_key", "a valid Bearer API key is required")
	}
	prefix, err := apikey.ParseToken(token)
	if err != nil {
		_ = a.verifier.DummyVerify(ctx, probeToken(token))
		return Identity{}, apikey.Set{}, unauthorized("invalid_api_key", "a valid Bearer API key is required")
	}
	record, err := apikey.FindForAuth(ctx, a.keys, prefix)
	if errors.Is(err, apikey.ErrNotFound) {
		_ = a.verifier.DummyVerify(ctx, probeToken(token))
		return Identity{}, apikey.Set{}, unauthorized("invalid_api_key", "a valid Bearer API key is required")
	}
	if err != nil {
		return Identity{}, apikey.Set{}, err
	}
	// Revoked and expired rows still pay one full derivation before rejection.
	if err := a.verifier.Verify(ctx, token, record.SecretHash); err != nil {
		if errors.Is(err, apikey.ErrMismatch) || errors.Is(err, apikey.ErrInvalidVerifier) {
			return Identity{}, apikey.Set{}, unauthorized("invalid_api_key", "a valid Bearer API key is required")
		}
		return Identity{}, apikey.Set{}, err
	}
	if record.RevokedAt != nil {
		return Identity{}, apikey.Set{}, unauthorized("invalid_api_key", "a valid Bearer API key is required")
	}
	if record.ExpiresAt != nil && !record.ExpiresAt.After(a.now()) {
		return Identity{}, apikey.Set{}, unauthorized("invalid_api_key", "a valid Bearer API key is required")
	}
	identity := Identity{
		APIKeyID:           record.ID,
		OrganizationID:     record.OrganizationID,
		OrganizationStatus: record.OrganizationStatus,
	}
	return identity, apikey.Parse(record.Scopes), nil
}

func bearerToken(authorization string) (string, bool) {
	const scheme = "bearer "
	if len(authorization) <= len(scheme) || !strings.EqualFold(authorization[:len(scheme)], scheme) {
		return "", false
	}
	token := authorization[len(scheme):]
	if strings.ContainsAny(token, " \t") {
		return "", false
	}
	return token, true
}

// probeToken bounds the dummy-derivation input so an oversized header cannot
// amplify work; it stays deterministic for a given oversized token.
func probeToken(token string) string {
	if len(token) > apikey.MaxPHCLength {
		return token[:apikey.MaxPHCLength]
	}
	return token
}
