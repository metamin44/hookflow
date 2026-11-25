package eventapp

import (
	"errors"
	"fmt"
)

// ErrIdempotencyConflict reports a different request body for an idempotency
// key that is still within its 72-hour window. The callers map it to the
// approved 409 idempotency_key_conflict response.
var ErrIdempotencyConflict = errors.New("idempotency key conflict")

// InactiveOrganizationError reports the organization state observed under the
// ingestion share lock. Suspended and archived organizations are rejected
// before any idempotency or event write.
type InactiveOrganizationError struct {
	Status string
}

func (e *InactiveOrganizationError) Error() string {
	return fmt.Sprintf("organization is %s", e.Status)
}

// errLinkedEventMissing reports a committed idempotency record whose Event row
// cannot be read. It indicates database damage, never a client error.
var errLinkedEventMissing = errors.New("linked event row is missing")
