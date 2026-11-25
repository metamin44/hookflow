package endpoint

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgtype"
)

// ErrNotFound reports a missing or soft-deleted endpoint.
var ErrNotFound = errors.New("endpoint not found")

// Querier is satisfied by pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

const columns = `id::text, organization_id::text, name, description, url,
	secret_encrypted, signing_scheme, http_headers, status, deleted_at`

// Lock takes the approved endpoint management lock scoped to the organization.
// Soft-deleted rows are returned so the caller can reject them explicitly.
func Lock(ctx context.Context, q Querier, organizationID, endpointID string) (Record, error) {
	const query = `SELECT ` + columns + `
		FROM endpoints
		WHERE organization_id = $1::uuid AND id = $2::uuid
		FOR NO KEY UPDATE`
	record, err := scanRecord(q.QueryRow(ctx, query, organizationID, endpointID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("lock endpoint: %w", err)
	}
	return record, nil
}

// Get reads a live endpoint without taking a lock.
func Get(ctx context.Context, q Querier, organizationID, endpointID string) (Record, error) {
	const query = `SELECT ` + columns + `
		FROM endpoints
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL`
	record, err := scanRecord(q.QueryRow(ctx, query, organizationID, endpointID))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("read endpoint: %w", err)
	}
	return record, nil
}

// List returns the live endpoints of one organization.
func List(ctx context.Context, q Querier, organizationID string) ([]Record, error) {
	const query = `SELECT ` + columns + `
		FROM endpoints
		WHERE organization_id = $1::uuid AND deleted_at IS NULL
		ORDER BY name, id`
	rows, err := q.Query(ctx, query, organizationID)
	if err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	defer rows.Close()
	records := make([]Record, 0, 8)
	for rows.Next() {
		record, scanErr := scanRecord(rows)
		if scanErr != nil {
			return nil, fmt.Errorf("scan endpoint: %w", scanErr)
		}
		records = append(records, record)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list endpoints: %w", err)
	}
	return records, nil
}

// CountActive counts the live, enabled endpoints under the caller's
// organization lock.
func CountActive(ctx context.Context, q Querier, organizationID string) (int, error) {
	const query = `SELECT count(*) FROM endpoints
		WHERE organization_id = $1::uuid AND deleted_at IS NULL AND status = 'active'`
	var count int
	if err := q.QueryRow(ctx, query, organizationID).Scan(&count); err != nil {
		return 0, fmt.Errorf("count active endpoints: %w", err)
	}
	return count, nil
}

// Insert writes a new endpoint. signing_scheme keeps its schema default.
func Insert(ctx context.Context, q Querier, record Record) (Record, error) {
	const statement = `INSERT INTO endpoints
		(id, organization_id, name, description, url, secret_encrypted, http_headers, status)
		VALUES ($1::uuid, $2::uuid, $3, $4, $5, $6, $7::jsonb, 'active')
		RETURNING ` + columns
	headers, err := encodeHeaders(record.HTTPHeaders)
	if err != nil {
		return Record{}, err
	}
	inserted, err := scanRecord(q.QueryRow(ctx, statement,
		record.ID, record.OrganizationID, record.Name, record.Description, record.URL,
		record.SecretEncrypted, headers,
	))
	if err != nil {
		return Record{}, fmt.Errorf("insert endpoint: %w", err)
	}
	return inserted, nil
}

// Update writes every public writable field of the locked row.
func Update(ctx context.Context, q Querier, record Record) (Record, error) {
	const statement = `UPDATE endpoints
		SET name = $3, description = $4, url = $5, http_headers = $6::jsonb,
		    status = $7, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
		RETURNING ` + columns
	headers, err := encodeHeaders(record.HTTPHeaders)
	if err != nil {
		return Record{}, err
	}
	updated, err := scanRecord(q.QueryRow(ctx, statement,
		record.OrganizationID, record.ID, record.Name, record.Description, record.URL,
		headers, record.Status,
	))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("update endpoint: %w", err)
	}
	return updated, nil
}

// UpdateSecret replaces only the encrypted envelope.
func UpdateSecret(ctx context.Context, q Querier, organizationID, endpointID, secretEncrypted string) (Record, error) {
	const statement = `UPDATE endpoints
		SET secret_encrypted = $3, updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
		RETURNING ` + columns
	updated, err := scanRecord(q.QueryRow(ctx, statement, organizationID, endpointID, secretEncrypted))
	if errors.Is(err, pgx.ErrNoRows) {
		return Record{}, ErrNotFound
	}
	if err != nil {
		return Record{}, fmt.Errorf("update endpoint secret: %w", err)
	}
	return updated, nil
}

// SoftDelete sets deleted_at without touching the secret envelope or any
// subscription row.
func SoftDelete(ctx context.Context, q Querier, organizationID, endpointID string) (string, error) {
	const statement = `UPDATE endpoints
		SET deleted_at = now(), updated_at = now()
		WHERE organization_id = $1::uuid AND id = $2::uuid AND deleted_at IS NULL
		RETURNING id::text`
	var id string
	if err := q.QueryRow(ctx, statement, organizationID, endpointID).Scan(&id); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("delete endpoint: %w", err)
	}
	return id, nil
}

// IsDuplicateName reports the live-name unique violation.
func IsDuplicateName(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "uq_endpoints_live_name"
}

func encodeHeaders(headers map[string]string) ([]byte, error) {
	if headers == nil {
		headers = map[string]string{}
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return nil, fmt.Errorf("encode endpoint headers: %w", err)
	}
	return encoded, nil
}

func scanRecord(row pgx.Row) (Record, error) {
	var record Record
	var description pgtype.Text
	var deleted pgtype.Timestamptz
	var rawHeaders []byte
	err := row.Scan(
		&record.ID, &record.OrganizationID, &record.Name, &description, &record.URL,
		&record.SecretEncrypted, &record.SigningScheme, &rawHeaders, &record.Status, &deleted,
	)
	if err != nil {
		return Record{}, err
	}
	if description.Valid {
		value := description.String
		record.Description = &value
	}
	if deleted.Valid {
		value := deleted.Time
		record.DeletedAt = &value
	}
	record.HTTPHeaders = map[string]string{}
	if len(rawHeaders) > 0 {
		if err := json.Unmarshal(rawHeaders, &record.HTTPHeaders); err != nil {
			return Record{}, fmt.Errorf("decode endpoint headers: %w", err)
		}
	}
	return record, nil
}

// Deleted reports the soft-deletion state.
func (r Record) Deleted() bool { return r.DeletedAt != nil }
