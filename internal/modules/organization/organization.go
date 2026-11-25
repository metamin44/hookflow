// Package organization owns the tenants root read used by management paths.
package organization

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
)

// Organization statuses.
const (
	StatusActive    = "active"
	StatusSuspended = "suspended"
	StatusArchived  = "archived"
)

// ErrNotFound reports a missing organization.
var ErrNotFound = errors.New("organization not found")

// Querier is satisfied by pgxpool.Pool and pgx.Tx.
type Querier interface {
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

// Lock takes the approved organization management lock and returns its status.
// The lock is FOR NO KEY UPDATE: it serializes management writers while staying
// compatible with the KEY SHARE locks taken by foreign-key checks in the
// delivery pipeline.
func Lock(ctx context.Context, q Querier, organizationID string) (string, error) {
	const query = `SELECT status FROM organizations WHERE id = $1::uuid FOR NO KEY UPDATE`
	var status string
	if err := q.QueryRow(ctx, query, organizationID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("lock organization: %w", err)
	}
	return status, nil
}

// LockShare takes the approved organization ingestion lock and returns its
// status. The lock is FOR SHARE: it serializes against management writers (which
// take FOR NO KEY UPDATE first) while allowing concurrent ingestion for the
// same organization, and it is compatible with the KEY SHARE locks taken by
// foreign-key checks.
func LockShare(ctx context.Context, q Querier, organizationID string) (string, error) {
	const query = `SELECT status FROM organizations WHERE id = $1::uuid FOR SHARE`
	var status string
	if err := q.QueryRow(ctx, query, organizationID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("lock organization for ingestion: %w", err)
	}
	return status, nil
}

// Read returns the status without taking a lock.
func Read(ctx context.Context, q Querier, organizationID string) (string, error) {
	const query = `SELECT status FROM organizations WHERE id = $1::uuid`
	var status string
	if err := q.QueryRow(ctx, query, organizationID).Scan(&status); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return "", ErrNotFound
		}
		return "", fmt.Errorf("read organization: %w", err)
	}
	return status, nil
}
