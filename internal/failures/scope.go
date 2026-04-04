package failures

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// HashScope generates a stable, unique key for a failure scope.
// Fields are normalized (lowercased, whitespace-trimmed) before hashing so that
// scopes that differ only by casing or surrounding spaces hash to the same key,
// matching the case-insensitive semantics of Match.
func HashScope(s models.FailureScope) string {
	parts := []string{
		"node:" + normalizeField(s.Node),
		"workload:" + normalizeField(string(s.Workload)),
		"tool:" + normalizeField(s.Tool),
		"backend:" + normalizeField(s.Backend),
		"surface:" + normalizeField(s.Surface),
	}
	joined := strings.Join(parts, "|")
	hash := sha256.Sum256([]byte(joined))
	return hex.EncodeToString(hash[:12]) // 24 chars is plenty for local collision resistance
}

// normalizeField returns a lowercased, whitespace-trimmed version of s so that
// HashScope and Match operate on the same effective comparison domain.
func normalizeField(s string) string {
	return strings.ToLower(strings.TrimSpace(s))
}

// Match checks if a specific target scope falls under a failure record's scope.
// A record scope matches a target if all non-empty fields in the record scope
// exactly match the target scope.
//
// For example:
// Record {Node: "alpha"} MATCHES Target {Node: "alpha", Tool: "git"}
// Record {Node: "alpha", Tool: "docker"} DOES NOT MATCH Target {Node: "alpha", Tool: "git"}
func Match(record models.FailureScope, target models.FailureScope) bool {
	if record.Node != "" && !strings.EqualFold(record.Node, target.Node) {
		return false
	}
	if record.Workload != "" && !strings.EqualFold(string(record.Workload), string(target.Workload)) {
		return false
	}
	if record.Tool != "" && !strings.EqualFold(record.Tool, target.Tool) {
		return false
	}
	if record.Backend != "" && !strings.EqualFold(record.Backend, target.Backend) {
		return false
	}
	if record.Surface != "" && !strings.EqualFold(record.Surface, target.Surface) {
		return false
	}
	return true
}

// NarrowestMatch returns the most specific failure record that applies to the target.
// Specificity is determined by the number of non-empty fields in the record's scope.
func (s Store) NarrowestMatch(target models.FailureScope) (*models.FailureRecord, bool) {
	now := time.Now().UTC()
	var bestMatch *models.FailureRecord
	bestScore := -1

	for _, rec := range s {
		if rec.OperatorOverride || !rec.ExpiresAt.After(now) {
			continue // expired or overridden records don't match
		}

		if Match(rec.Scope, target) {
			score := specificityScore(rec.Scope)
			if score > bestScore {
				recCopy := rec
				bestMatch = &recCopy
				bestScore = score
			}
		}
	}

	if bestMatch != nil {
		return bestMatch, true
	}
	return nil, false
}

func specificityScore(s models.FailureScope) int {
	score := 0
	if s.Node != "" {
		score++
	}
	if s.Workload != "" {
		score++
	}
	if s.Tool != "" {
		score++
	}
	if s.Backend != "" {
		score++
	}
	if s.Surface != "" {
		score++
	}
	return score
}
