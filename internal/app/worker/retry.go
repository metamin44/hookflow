package worker

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/metamin44/hookflow/internal/app/delivery"
)

const (
	maxRetryAttempts = 8
	maxRetryHorizon  = 12 * time.Hour
	maxRetryAfter    = time.Hour
)

var retryLadder = [...]time.Duration{
	10 * time.Second,
	30 * time.Second,
	2 * time.Minute,
	5 * time.Minute,
	15 * time.Minute,
	time.Hour,
	4 * time.Hour,
	12 * time.Hour,
}

// RetryPolicy is the approved, fixed delivery retry policy.
type RetryPolicy struct {
	MaxAttempts int
	Horizon     time.Duration
}

// NewRetryPolicy validates the attempt cap and scheduling horizon.
func NewRetryPolicy(maxAttempts int, horizon time.Duration) (*RetryPolicy, error) {
	if maxAttempts < 1 || maxAttempts > maxRetryAttempts {
		return nil, errors.New("retry maximum attempts must be between 1 and 8")
	}
	if horizon <= 0 || horizon > maxRetryHorizon {
		return nil, errors.New("retry horizon must be positive and no greater than 12 hours")
	}
	return &RetryPolicy{MaxAttempts: maxAttempts, Horizon: horizon}, nil
}

// Decision returns the approved delay after a 1-based failed attempt. The
// caller adds the returned duration to its durable completion timestamp.
func (p *RetryPolicy) Decision(deliveryID string, attemptNumber int, responseStatus *int, retryAfter string) delivery.RetryDecision {
	if p == nil || p.MaxAttempts < 1 || p.MaxAttempts > maxRetryAttempts ||
		attemptNumber < 1 || attemptNumber >= p.MaxAttempts ||
		attemptNumber > len(retryLadder) || p.Horizon <= 0 || p.Horizon > maxRetryHorizon {
		return delivery.RetryDecision{}
	}

	step := retryLadder[attemptNumber-1]
	backoff := deterministicJitter(deliveryID, attemptNumber, step)
	if responseStatus != nil && *responseStatus == http.StatusTooManyRequests {
		if delay, ok := parseRetryAfter(retryAfter, time.Now()); ok {
			backoff = delay
		}
	}
	if backoff > p.Horizon {
		backoff = p.Horizon
	}
	return delivery.RetryDecision{Retry: true, Backoff: backoff}
}

func deterministicJitter(deliveryID string, attemptNumber int, base time.Duration) time.Duration {
	lower := base / 2
	span := base - lower
	if span == 0 {
		return lower
	}
	digest := sha256.Sum256([]byte(deliveryID + ":" + strconv.Itoa(attemptNumber)))
	offset := time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(span+1)) //nolint:gosec // span is positive and bounded by MaxInt64/2.
	return saturatedDurationAdd(lower, offset)
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if decimalDigits(value) {
		// Compare before multiplication so arbitrarily large, syntactically
		// valid delta-seconds cap cleanly instead of overflowing.
		canonical := strings.TrimLeft(value, "0")
		if canonical == "" {
			// Durable retry backoff must be positive. A valid zero-second
			// Retry-After therefore becomes the smallest positive duration.
			return time.Nanosecond, true
		}
		if len(canonical) > len(strconv.FormatUint(uint64(maxRetryAfter/time.Second), 10)) ||
			(len(canonical) == len(strconv.FormatUint(uint64(maxRetryAfter/time.Second), 10)) &&
				canonical > strconv.FormatUint(uint64(maxRetryAfter/time.Second), 10)) {
			return maxRetryAfter, true
		}
		seconds, err := strconv.ParseUint(canonical, 10, 64)
		if err != nil {
			return 0, false
		}
		return saturatedDurationAdd(time.Duration(seconds)*time.Second, 0), true //nolint:gosec // Decimal comparison above caps seconds at 3600.
	}

	date, err := http.ParseTime(value)
	if err != nil || !date.After(now) {
		return 0, false
	}
	delay := date.Sub(now)
	if delay > maxRetryAfter {
		return maxRetryAfter, true
	}
	return delay, true
}

func decimalDigits(value string) bool {
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// DecryptFailurePacing exposes the fixed pacing helper through a policy value
// for runtime callers that already hold a RetryPolicy.
func (p *RetryPolicy) DecryptFailurePacing(deliveryID string, base time.Duration) time.Duration {
	return DecryptFailurePacing(deliveryID, base)
}

// DecryptFailurePacing returns the approved base plus deterministic jitter in
// [0%, 20%]. It returns zero for an invalid base and saturates on overflow.
func DecryptFailurePacing(deliveryID string, base time.Duration) time.Duration {
	if base <= 0 {
		return 0
	}
	maximumExtra := base / 5
	digest := sha256.Sum256([]byte(deliveryID))
	extra := time.Duration(binary.BigEndian.Uint64(digest[:8]) % uint64(maximumExtra+1)) //nolint:gosec // maximumExtra is positive and bounded by MaxInt64/5.
	return saturatedDurationAdd(base, extra)
}

func saturatedDurationAdd(left, right time.Duration) time.Duration {
	if right > 0 && left > time.Duration(math.MaxInt64)-right {
		return time.Duration(math.MaxInt64)
	}
	return left + right
}
