// Package publisher runs the fenced transactional-outbox relay.
package publisher

import (
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"time"
)

// Backoff implements min(base * 2^(claim-1), maximum) with full jitter over
// [minimum, capped base]. It is overflow-safe even for an untrusted claim count.
func Backoff(claimCount int, base, maximum, minimum time.Duration, random func(*big.Int) (*big.Int, error)) (time.Duration, error) {
	if claimCount <= 0 || base <= 0 || maximum <= 0 || minimum <= 0 || minimum > base || base > maximum {
		return 0, errors.New("invalid publisher backoff configuration")
	}
	if random == nil {
		return 0, errors.New("publisher jitter source is required")
	}

	bound := base
	for i := 1; i < claimCount; i++ {
		if bound >= maximum/2 {
			bound = maximum
			break
		}
		bound *= 2
	}
	if bound > maximum {
		bound = maximum
	}
	if bound < minimum {
		bound = minimum
	}

	width := int64(bound-minimum) + 1
	offset, err := random(new(big.Int).SetInt64(width))
	if err != nil {
		return 0, fmt.Errorf("derive publisher jitter: %w", err)
	}
	if !offset.IsInt64() || offset.Sign() < 0 || offset.Int64() >= width {
		return 0, errors.New("publisher jitter source returned an out-of-range value")
	}
	return minimum + time.Duration(offset.Int64()), nil
}

func cryptoRandomInt(upper *big.Int) (*big.Int, error) {
	return rand.Int(rand.Reader, upper)
}
