// Package accesskeys implements the operator API-key lifecycle: create, list
// and revoke. It is the only place a raw API key is produced, and the caller
// (the CLI) prints it exactly once.
package accesskeys

import (
	"context"
	"errors"
	"fmt"
	"io"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/metamin44/hookflow/internal/modules/apikey"
	"github.com/metamin44/hookflow/internal/modules/audit"
	"github.com/metamin44/hookflow/internal/modules/organization"
	"github.com/metamin44/hookflow/internal/platform/id"
	"github.com/metamin44/hookflow/internal/platform/secrets"
)

// MaxNameLength mirrors chk_api_keys_name_len.
const MaxNameLength = 80

// Operator-facing errors. They are static and never carry key material.
var (
	ErrOrganizationNotFound  = errors.New("organization not found")
	ErrOrganizationSuspended = errors.New("suspended organizations cannot create api keys")
	ErrOrganizationArchived  = errors.New("archived organizations reject api key changes")
	ErrKeyNotFound           = errors.New("api key not found")
	ErrNameConflict          = errors.New("an active api key with this name already exists")
	ErrPrefixCollision       = errors.New("api key prefix collision; retry the command")
	ErrInvalidName           = errors.New("api key name must be 1 to 80 characters without control characters")
)

// Database is the pool surface the operator service needs.
type Database interface {
	BeginTx(ctx context.Context, opts pgx.TxOptions) (pgx.Tx, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// Service owns the operator API-key use cases.
type Service struct {
	pool   Database
	random io.Reader
	newID  func() (string, error)
}

// NewService builds the operator API-key service.
func NewService(pool Database, random io.Reader) (*Service, error) {
	if pool == nil {
		return nil, errors.New("accesskeys: database pool is required")
	}
	if random == nil {
		return nil, errors.New("accesskeys: randomness source is required")
	}
	return &Service{pool: pool, random: random, newID: id.NewV7}, nil
}

// Create mints one API key and returns the raw token exactly once together with
// its operator-facing metadata. The verifier may pass through the persistence
// and authentication paths, but it must never appear in an operator-facing
// service result. Operator-facing methods return metadata-only values.
func (s *Service) Create(ctx context.Context, organizationID, name string, scopes []string, expiresAt *time.Time) (string, apikey.Metadata, error) {
	if _, err := secrets.ParseUUID(organizationID); err != nil {
		return "", apikey.Metadata{}, ErrOrganizationNotFound
	}
	if !validName(name) {
		return "", apikey.Metadata{}, ErrInvalidName
	}
	token, prefix, err := apikey.Generate(s.random)
	if err != nil {
		return "", apikey.Metadata{}, err
	}
	verifier, err := apikey.Hash(token)
	if err != nil {
		return "", apikey.Metadata{}, err
	}
	keyID, err := s.newID()
	if err != nil {
		return "", apikey.Metadata{}, err
	}
	granted := apikey.Parse(scopes).CanonicalStrings()
	record := apikey.Record{
		ID:             keyID,
		OrganizationID: organizationID,
		Name:           name,
		KeyPrefix:      prefix,
		SecretHash:     verifier,
		HashAlgorithm:  apikey.Algorithm(),
		Scopes:         granted,
		ExpiresAt:      expiresAt,
	}
	var createdAt time.Time
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, organizationID)
		if err != nil {
			if errors.Is(err, organization.ErrNotFound) {
				return ErrOrganizationNotFound
			}
			return err
		}
		if status == organization.StatusArchived {
			return ErrOrganizationArchived
		}
		if status == organization.StatusSuspended {
			return ErrOrganizationSuspended
		}
		insertedAt, err := apikey.Insert(ctx, tx, record)
		if err != nil {
			if isNameConflict(err) {
				return ErrNameConflict
			}
			if isPrefixConflict(err) {
				return ErrPrefixCollision
			}
			return err
		}
		createdAt = insertedAt
		return s.writeAudit(ctx, tx, organizationID, "apikey.created", &keyID, map[string]any{
			"name":   name,
			"scopes": granted,
		})
	})
	if txErr != nil {
		return "", apikey.Metadata{}, txErr
	}
	record.CreatedAt = createdAt
	return token, record.Metadata(), nil
}

// List returns the organization's keys newest first. Revoked keys stay visible.
func (s *Service) List(ctx context.Context, organizationID string) ([]apikey.Metadata, error) {
	if _, err := secrets.ParseUUID(organizationID); err != nil {
		return nil, ErrOrganizationNotFound
	}
	if _, err := organization.Read(ctx, s.pool, organizationID); err != nil {
		if errors.Is(err, organization.ErrNotFound) {
			return nil, ErrOrganizationNotFound
		}
		return nil, err
	}
	return apikey.List(ctx, s.pool, organizationID)
}

// Revoke marks one key revoked. Repeating it is a no-op; suspended
// organizations may revoke, archived organizations may not.
func (s *Service) Revoke(ctx context.Context, organizationID, keyID string) (apikey.Metadata, bool, error) {
	if _, err := secrets.ParseUUID(organizationID); err != nil {
		return apikey.Metadata{}, false, ErrOrganizationNotFound
	}
	if _, err := secrets.ParseUUID(keyID); err != nil {
		return apikey.Metadata{}, false, ErrKeyNotFound
	}
	var record apikey.Metadata
	var changed bool
	txErr := s.inTx(ctx, func(tx pgx.Tx) error {
		status, err := organization.Lock(ctx, tx, organizationID)
		if err != nil {
			if errors.Is(err, organization.ErrNotFound) {
				return ErrOrganizationNotFound
			}
			return err
		}
		if status == organization.StatusArchived {
			return ErrOrganizationArchived
		}
		record, changed, err = apikey.Revoke(ctx, tx, organizationID, keyID)
		if errors.Is(err, apikey.ErrNotFound) {
			return ErrKeyNotFound
		}
		if err != nil {
			return err
		}
		if !changed {
			return nil
		}
		return s.writeAudit(ctx, tx, organizationID, "apikey.revoked", &keyID, nil)
	})
	if txErr != nil {
		return apikey.Metadata{}, false, txErr
	}
	return record, changed, nil
}

func (s *Service) inTx(ctx context.Context, fn func(pgx.Tx) error) error {
	tx, err := s.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return fmt.Errorf("begin api key transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()
	if err := fn(tx); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit api key transaction: %w", err)
	}
	return nil
}

func (s *Service) writeAudit(ctx context.Context, tx pgx.Tx, organizationID, action string, targetID *string, metadata map[string]any) error {
	auditID, err := s.newID()
	if err != nil {
		return err
	}
	return audit.Insert(ctx, tx, audit.Record{
		ID:             auditID,
		OrganizationID: organizationID,
		ActorType:      audit.ActorService,
		Action:         action,
		TargetType:     audit.TargetAPIKey,
		TargetID:       targetID,
		Metadata:       metadata,
	})
}

func isNameConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_api_keys_org_name"
}

func isPrefixConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_api_keys_prefix"
}

func validName(name string) bool {
	if !utf8.ValidString(name) {
		return false
	}
	count := utf8.RuneCountInString(name)
	if count < 1 || count > MaxNameLength {
		return false
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return false
		}
	}
	return true
}
