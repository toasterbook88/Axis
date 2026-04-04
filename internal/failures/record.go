package failures

import (
	"crypto/rand"
	"encoding/hex"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// Store represents a collection of failure records.
// In AXIS, this is typically embedded in state.ClusterState.
type Store map[string]models.FailureRecord

// NewStore initializes a new empty failure store.
func NewStore() Store {
	return make(Store)
}

// Record creates or escalates a failure record for a specific scope.
// It applies exponential backoff for recurring failures.
func (s Store) Record(class models.FailureClass, scope models.FailureScope, reason string, evidence []string) (models.FailureRecord, bool) {
	key := HashScope(scope)
	now := time.Now().UTC()

	entry, exists := s[key]
	if !exists {
		idBytes := make([]byte, 8)
		_, _ = rand.Read(idBytes)
		entry = models.FailureRecord{
			ID:         hex.EncodeToString(idBytes),
			Class:      class,
			Scope:      scope,
			OccurredAt: now,
			Count:      0,
		}
	}

	// Update mutable fields
	entry.Count++
	entry.Class = class // update to latest class
	entry.Reason = reason
	entry.Evidence = evidence
	entry.OccurredAt = now
	entry.OperatorOverride = false
	entry.OperatorNote = ""

	// Calculate new expiry
	entry.ExpiresAt = now.Add(CalculateExpiry(entry.Count))

	s[key] = entry
	return entry, !exists
}

// RecordSuccess clears a failure or reduces its penalty.
func (s Store) RecordSuccess(scope models.FailureScope) bool {
	key := HashScope(scope)
	if _, exists := s[key]; !exists {
		return false
	}
	delete(s, key)
	return true
}

// ClearOverride removes a failure completely if an operator overrides it.
func (s Store) ClearOverride(scope models.FailureScope, note string) bool {
	key := HashScope(scope)
	entry, exists := s[key]
	if !exists {
		return false
	}
	entry.OperatorOverride = true
	entry.OperatorNote = note
	entry.ExpiresAt = time.Now().UTC() // expire immediately
	s[key] = entry
	return true
}

// Delete permanently removes a record.
func (s Store) Delete(scope models.FailureScope) bool {
	key := HashScope(scope)
	if _, exists := s[key]; !exists {
		return false
	}
	delete(s, key)
	return true
}

// Prune removes all expired records and returns the number removed.
func (s Store) Prune() int {
	now := time.Now().UTC()
	removed := 0
	for k, v := range s {
		if v.OperatorOverride || now.After(v.ExpiresAt) || now.Equal(v.ExpiresAt) {
			delete(s, k)
			removed++
		}
	}
	return removed
}
