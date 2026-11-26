package delivery

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PreHTTPReason is the bounded, low-cardinality reason for a pre-HTTP
// disposition. It never carries envelope bytes, key identifiers, or provider
// response text.
type PreHTTPReason string

// Approved pre-HTTP reason codes.
const (
	// ReasonSecretUnavailable is transient: the provider or its resolution
	// context is temporarily unavailable. It uses the paced B5 path.
	ReasonSecretUnavailable PreHTTPReason = "secret_unavailable"
	// ReasonSecretMissing is permanent: the stored secret is empty and the
	// unsigned gate does not permit a request.
	ReasonSecretMissing PreHTTPReason = "secret_missing"
	// ReasonSecretInvalid is permanent: the stored envelope, its key material,
	// its authentication tag, or its plaintext is unusable.
	ReasonSecretInvalid PreHTTPReason = "secret_invalid"
	// ReasonProviderDisallowed is permanent: the envelope provider tag is not
	// allowlisted by deployment configuration.
	ReasonProviderDisallowed PreHTTPReason = "provider_disallowed"
	// ReasonSigningInternal is permanent: request signing failed before HTTP.
	ReasonSigningInternal PreHTTPReason = "signing_internal"
	// ReasonDestinationDisallowed is permanent: the stored destination URL or a
	// resolved address violates the approved SSRF policy.
	ReasonDestinationDisallowed PreHTTPReason = "destination_disallowed"
	// ReasonHeadersInvalid is permanent: the stored request-header snapshot is
	// malformed and cannot be safely transmitted.
	ReasonHeadersInvalid PreHTTPReason = "headers_invalid"
)

// Valid reports whether the reason is approved by this build.
func (r PreHTTPReason) Valid() bool {
	switch r {
	case ReasonSecretUnavailable, ReasonSecretMissing, ReasonSecretInvalid,
		ReasonProviderDisallowed, ReasonSigningInternal, ReasonDestinationDisallowed,
		ReasonHeadersInvalid:
		return true
	default:
		return false
	}
}

// Transient reports whether the reason uses the paced B5 retry path.
func (r PreHTTPReason) Transient() bool { return r == ReasonSecretUnavailable }

// PreHTTPError is the typed pre-HTTP failure returned by request construction.
// It is never a completed HTTP attempt.
type PreHTTPError struct {
	Reason  PreHTTPReason
	Message string
}

// Error returns only the bounded reason code; it never embeds secrets.
func (e *PreHTTPError) Error() string {
	if e == nil {
		return "pre-HTTP delivery failure"
	}
	return string(e.Reason)
}

// PreHTTPFailureOptions are the explicit pre-HTTP disposition inputs.
type PreHTTPFailureOptions struct {
	Reason PreHTTPReason
	// Pacing is required only when Reason is transient.
	Pacing time.Duration
	// Error is normalized, credential-scrubbed, and bounded before storage.
	Error string
}

// PreHTTPFailureResult reports the durable pre-HTTP disposition.
type PreHTTPFailureResult struct {
	Applied       bool
	Fenced        bool
	Expired       bool
	NextAttemptAt time.Time
	RetrySequence int
	AckEligible   bool
	Reason        PreHTTPReason
}

// RecordPreHTTPFailure persists one pre-HTTP disposition.
//
// Transient reasons reuse the approved paced B5 mechanics: no attempt row, no
// attempt_count increment, event-expiry precedence, one fenced retry outbox row
// when a dispatch is scheduled, and exactly one dispatch-generation advance.
//
// Permanent reasons terminalize the delivery in the existing `failed` state
// with completed_at set, next_attempt_at NULL, and the lease cleared. They write
// no attempt row, do not increment attempt_count, do not advance dispatch_seq,
// and create no retry outbox row.
func (s *Store) RecordPreHTTPFailure(ctx context.Context, claim Claim, opts PreHTTPFailureOptions) (PreHTTPFailureResult, error) {
	if err := validateClaim(claim); err != nil {
		return PreHTTPFailureResult{}, err
	}
	if !opts.Reason.Valid() {
		return PreHTTPFailureResult{}, errors.New("pre-HTTP failure reason is not approved")
	}
	if opts.Reason.Transient() && opts.Pacing <= 0 {
		return PreHTTPFailureResult{}, errors.New("pre-HTTP transient pacing must be positive")
	}
	diagnostic := boundedDiagnostic(opts.Error)
	if diagnostic == "" {
		diagnostic = string(opts.Reason)
	}

	tx, err := s.db.Begin(ctx)
	if err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("begin pre-HTTP disposition transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	locked, found, err := lockDelivery(ctx, tx, claim.OrganizationID, claim.DeliveryID)
	if err != nil {
		return PreHTTPFailureResult{}, err
	}
	if !found || completionFenced(locked, claim) {
		if err := tx.Commit(ctx); err != nil {
			return PreHTTPFailureResult{}, fmt.Errorf("commit fenced pre-HTTP transaction: %w", err)
		}
		s.logger.Warn("pre-HTTP disposition fenced", "reason", fenceReason(locked, found, claim))
		return PreHTTPFailureResult{Fenced: true, AckEligible: true}, nil
	}

	t, err := sampleTime(ctx, tx)
	if err != nil {
		return PreHTTPFailureResult{}, err
	}

	// Event-horizon precedence applies to every pre-HTTP disposition.
	if !locked.eventExpiresAt.After(t) {
		result, err := s.expirePreHTTPFailure(ctx, tx, claim, t, diagnostic, "event horizon")
		if result.AckEligible {
			result.Reason = opts.Reason
		}
		return result, err
	}

	if opts.Reason.Transient() {
		if locked.leaseExpired(t) {
			result, err := s.expirePreHTTPFailure(ctx, tx, claim, t, diagnostic, "worker lease expired")
			if result.AckEligible {
				result.Reason = opts.Reason
			}
			return result, err
		}
		return s.recordPacedPreHTTPFailure(ctx, tx, claim, locked, opts, diagnostic, t)
	}

	return s.recordTerminalPreHTTPFailure(ctx, tx, claim, opts.Reason, diagnostic, t)
}

func (s *Store) recordPacedPreHTTPFailure(ctx context.Context, tx pgx.Tx, claim Claim, locked lockedDelivery, opts PreHTTPFailureOptions, diagnostic string, t time.Time) (PreHTTPFailureResult, error) {
	nextAttemptAt := t.Add(opts.Pacing)
	retrySequence := locked.dispatchSeq
	dispatch := nextAttemptAt.Before(locked.eventExpiresAt)
	if !dispatch {
		// A paced retry at or beyond the event horizon must not create a
		// dispatch that can never be attempted.
		nextAttemptAt = locked.eventExpiresAt
	}

	var message Message
	var err error
	if dispatch {
		retrySequence = locked.dispatchSeq + 1
		message, err = s.opts.BuildRetryMessage(RetryMessageInput{
			OrganizationID: claim.OrganizationID,
			DeliveryID:     claim.DeliveryID,
			EventID:        claim.EventID,
			Sequence:       retrySequence,
			NextAttemptAt:  nextAttemptAt,
		})
		if err != nil {
			return PreHTTPFailureResult{}, fmt.Errorf("build pre-HTTP retry message: %w", err)
		}
		if err := validateMessage(message, retrySequence); err != nil {
			return PreHTTPFailureResult{}, err
		}
	}

	tag, err := tx.Exec(ctx, decryptFailureUpdateSQL,
		claim.OrganizationID, claim.DeliveryID, claim.DispatchSeq, claim.LeaseToken,
		nextAttemptAt, diagnostic, t, retrySequence,
	)
	if err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("persist pre-HTTP retry disposition: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return PreHTTPFailureResult{}, errors.New("pre-HTTP retry fence was lost after validation")
	}

	if dispatch {
		if _, err := tx.Exec(ctx, insertOutboxSQL,
			message.ID, claim.OrganizationID, claim.DeliveryID, message.Sequence,
			message.Kind, message.Topic, message.Payload, nullableJSON(message.Headers),
			nextAttemptAt, t,
		); err != nil {
			return PreHTTPFailureResult{}, fmt.Errorf("insert pre-HTTP retry outbox row: %w", err)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("commit pre-HTTP retry transaction: %w", err)
	}

	s.logger.Info("pre-HTTP transient disposition applied",
		"reason", opts.Reason,
		"dispatch", dispatch,
		"pacing", opts.Pacing.String(),
	)
	return PreHTTPFailureResult{
		Applied:       true,
		NextAttemptAt: nextAttemptAt,
		RetrySequence: retrySequence,
		AckEligible:   true,
		Reason:        opts.Reason,
	}, nil
}

func (s *Store) recordTerminalPreHTTPFailure(ctx context.Context, tx pgx.Tx, claim Claim, reason PreHTTPReason, diagnostic string, t time.Time) (PreHTTPFailureResult, error) {
	tag, err := tx.Exec(ctx, terminalPreHTTPFailureSQL,
		claim.OrganizationID, claim.DeliveryID, claim.DispatchSeq, claim.LeaseToken, t, diagnostic,
	)
	if err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("persist terminal pre-HTTP disposition: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return PreHTTPFailureResult{}, errors.New("pre-HTTP terminal fence was lost after validation")
	}
	if err := tx.Commit(ctx); err != nil {
		return PreHTTPFailureResult{}, fmt.Errorf("commit terminal pre-HTTP transaction: %w", err)
	}
	s.logger.Info("pre-HTTP permanent disposition applied", "reason", reason)
	return PreHTTPFailureResult{Applied: true, AckEligible: true, Reason: reason}, nil
}

const terminalPreHTTPFailureSQL = `
UPDATE deliveries
   SET state = 'failed',
       completed_at = $5::timestamptz,
       next_attempt_at = NULL,
       last_error = $6,
       updated_at = $5::timestamptz,
       dispatch_lease_token = NULL,
       dispatch_lease_expires_at = NULL
 WHERE organization_id = $1::uuid
   AND id = $2::uuid
   AND dispatch_seq = $3
   AND dispatch_lease_token = $4::uuid
   AND state IN ('pending', 'retrying')`
