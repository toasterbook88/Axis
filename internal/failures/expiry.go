package failures

import (
	"time"
)

const (
	baseExpiry = 24 * time.Hour
	maxExpiry  = 7 * 24 * time.Hour
)

// CalculateExpiry returns the backoff duration based on the consecutive failure count.
// Base is 24h, doubles every count, capped at 7 days.
func CalculateExpiry(failCount int) time.Duration {
	if failCount <= 0 {
		return baseExpiry
	}

	expiry := baseExpiry
	for i := 1; i < failCount; i++ {
		expiry *= 2
		if expiry > maxExpiry {
			return maxExpiry
		}
	}
	return expiry
}
