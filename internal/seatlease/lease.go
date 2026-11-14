// Package seatlease decides whether one private job may call a desk seat.
// It reads a lease the caller already has. It does not discover a host and
// it does not substitute another backend.
package seatlease

import "time"

const (
	RefusalMissing     = "missing"
	RefusalUnloaded    = "unloaded"
	RefusalEnded       = "ended"
	RefusalAsleep      = "asleep"
	RefusalBusy        = "busy"
	RefusalStale       = "stale"
	RefusalUnavailable = "unavailable"
)

// Lease is the desk seat document supplied by the caller.
type Lease struct {
	State         string    `json:"state"`
	Busy          bool      `json:"busy"`
	Foreground    bool      `json:"foreground"`
	ExpiresAt     time.Time `json:"expiresAt"`
	CompletionURL string    `json:"completionURL"`
	Token         string    `json:"token,omitempty"`
}

// Decide returns a refusal name, or ok when the lease may take one prompt.
// A nil lease is missing. Ended, asleep, busy, and stale are checked before
// a fresh awake lease is accepted.
func Decide(lease *Lease, now time.Time) (string, bool) {
	if lease == nil {
		return RefusalMissing, false
	}
	switch lease.State {
	case "notLoaded", "loading", "":
		return RefusalUnloaded, false
	case "ended":
		return RefusalEnded, false
	case "awake", "answering":
	default:
		return RefusalUnloaded, false
	}
	if !lease.Foreground {
		return RefusalAsleep, false
	}
	if lease.Busy || lease.State == "answering" {
		return RefusalBusy, false
	}
	if lease.ExpiresAt.IsZero() || !now.Before(lease.ExpiresAt) {
		return RefusalStale, false
	}
	if lease.CompletionURL == "" {
		return RefusalMissing, false
	}
	return "", true
}
