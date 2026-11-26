package apikey

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// ErrNotFound reports a missing api_keys row.
var ErrNotFound = errors.New("api key not found")

// Record is one api_keys row for persistence and authentication. It never
// contains a raw token; SecretHash is Argon2id verifier material that must not
// cross operator-facing boundaries. Use Metadata for operator output.
type Record struct {
	ID             string
	OrganizationID string
	Name           string
	KeyPrefix      string
	SecretHash     string
	HashAlgorithm  string
	Scopes         []string
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
	CreatedAt      time.Time
}

// Metadata is the operator-facing view of an API key. It intentionally does
// not embed Record and has no field for the raw token, salt, or PHC verifier.
type Metadata struct {
	ID             string
	OrganizationID string
	Name           string
	KeyPrefix      string
	HashAlgorithm  string
	Scopes         []string
	LastUsedAt     *time.Time
	ExpiresAt      *time.Time
	RevokedAt      *time.Time
	CreatedAt      time.Time
}

// Metadata copies the non-secret fields of a stored record.
func (r Record) Metadata() Metadata {
	return Metadata{
		ID:             r.ID,
		OrganizationID: r.OrganizationID,
		Name:           r.Name,
		KeyPrefix:      r.KeyPrefix,
		HashAlgorithm:  r.HashAlgorithm,
		Scopes:         r.Scopes,
		LastUsedAt:     r.LastUsedAt,
		ExpiresAt:      r.ExpiresAt,
		RevokedAt:      r.RevokedAt,
		CreatedAt:      r.CreatedAt,
	}
}

// AuthRecord couples the key row with the owning organization's status so the
// authentication path needs one query.
type AuthRecord struct {
	Record
	OrganizationStatus string
}

// Querier is satisfied by both pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// selectColumns is the authentication/persistence projection. It includes the
// verifier and must only feed Record or AuthRecord.
const selectColumns = `k.id::text, k.organization_id::text, k.name, k.key_prefix,
	k.secret_hash, k.hash_algorithm, k.scopes, k.last_used_at, k.expires_at,
	k.revoked_at, k.created_at`

// metadataColumns is the operator-facing projection. It must never include
// k.secret_hash (or any other authentication material).
const metadataColumns = `k.id::text, k.organization_id::text, k.name, k.key_prefix,
	k.hash_algorithm, k.scopes, k.last_used_at, k.expires_at,
	k.revoked_at, k.created_at`

// FindForAuth looks up the live-or-revoked row by prefix. Revoked and expired
// rows are deliberately returned so the caller still performs one verifier
// derivation before rejecting.
func FindForAuth(ctx context.Context, q Querier, prefix string) (AuthRecord, error) {
	const query = `SELECT ` + selectColumns + `, o.status
		FROM api_keys k
		JOIN organizations o ON o.id = k.organization_id
		WHERE k.key_prefix = $1`
	var record AuthRecord
	err := q.QueryRow(ctx, query, prefix).Scan(
		&record.ID, &record.OrganizationID, &record.Name, &record.KeyPrefix,
		&record.SecretHash, &record.HashAlgorithm, &record.Scopes,
		&record.LastUsedAt, &record.ExpiresAt, &record.RevokedAt, &record.CreatedAt,
		&record.OrganizationStatus,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return AuthRecord{}, ErrNotFound
	}
	if err != nil {
		return AuthRecord{}, fmt.Errorf("look up api key: %w", err)
	}
	return record, nil
}

// Insert writes one api_keys row inside the caller's transaction and returns
// the database-generated created_at value.
func Insert(ctx context.Context, q Querier, record Record) (time.Time, error) {
	const statement = `INSERT INTO api_keys
		(id, organization_id, name, key_prefix, secret_hash, hash_algorithm, scopes, expires_at)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7, $8)
		RETURNING id, created_at`
	var inserted string
	var createdAt time.Time
	if err := q.QueryRow(ctx, statement,
		record.ID, record.OrganizationID, record.Name, record.KeyPrefix,
		record.SecretHash, record.HashAlgorithm, record.Scopes, record.ExpiresAt,
	).Scan(&inserted, &createdAt); err != nil {
		return time.Time{}, fmt.Errorf("insert api key: %w", err)
	}
	return createdAt, nil
}

// List returns the organization's keys, newest first. Revoked keys stay
// visible. The query and scan are metadata-only; verifier material never enters
// this result.
func List(ctx context.Context, q Querier, organizationID string) ([]Metadata, error) {
	const query = `SELECT ` + metadataColumns + `
		FROM api_keys k
		WHERE k.organization_id = $1::uuid
		ORDER BY k.created_at DESC, k.id DESC`
	rows, err := q.Query(ctx, query, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	defer rows.Close()
	records := make([]Metadata, 0, 8)
	for rows.Next() {
		var record Metadata
		if err := rows.Scan(
			&record.ID, &record.OrganizationID, &record.Name, &record.KeyPrefix,
			&record.HashAlgorithm, &record.Scopes,
			&record.LastUsedAt, &record.ExpiresAt, &record.RevokedAt, &record.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scan api key: %w", err)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list api keys: %w", err)
	}
	return records, nil
}

// Revoke marks a live key revoked. It returns the updated row as metadata; the
// boolean is false when the key was already revoked (no mutation happened).
func Revoke(ctx context.Context, q Querier, organizationID, keyID string) (Metadata, bool, error) {
	const statement = `UPDATE api_keys k
		SET revoked_at = now(), updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND revoked_at IS NULL
		RETURNING ` + metadataColumns
	var record Metadata
	err := q.QueryRow(ctx, statement, organizationID, keyID).Scan(
		&record.ID, &record.OrganizationID, &record.Name, &record.KeyPrefix,
		&record.HashAlgorithm, &record.Scopes,
		&record.LastUsedAt, &record.ExpiresAt, &record.RevokedAt, &record.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, getErr := get(ctx, q, organizationID, keyID)
		if getErr != nil {
			return Metadata{}, false, getErr
		}
		return existing, false, nil
	}
	if err != nil {
		return Metadata{}, false, fmt.Errorf("revoke api key: %w", err)
	}
	return record, true, nil
}

func get(ctx context.Context, q Querier, organizationID, keyID string) (Metadata, error) {
	const query = `SELECT ` + metadataColumns + `
		FROM api_keys k
		WHERE k.organization_id = $1::uuid AND k.id = $2::uuid`
	var record Metadata
	err := q.QueryRow(ctx, query, organizationID, keyID).Scan(
		&record.ID, &record.OrganizationID, &record.Name, &record.KeyPrefix,
		&record.HashAlgorithm, &record.Scopes,
		&record.LastUsedAt, &record.ExpiresAt, &record.RevokedAt, &record.CreatedAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return Metadata{}, ErrNotFound
	}
	if err != nil {
		return Metadata{}, fmt.Errorf("load api key: %w", err)
	}
	return record, nil
}
