package state

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

const ObservationStaleAfter = 7 * 24 * time.Hour

func normalizeObservationField(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func ObservationKey(scope models.ObservationScope) string {
	parts := []string{
		"node:" + normalizeObservationField(scope.Node),
		"workload:" + normalizeObservationField(string(scope.Workload)),
		"backend:" + normalizeObservationField(scope.Backend),
		"tool:" + normalizeObservationField(scope.Tool),
	}
	hash := sha256.Sum256([]byte(strings.Join(parts, "|")))
	return hex.EncodeToString(hash[:12])
}

func normalizeObservation(obs models.ExecutionObservation) models.ExecutionObservation {
	obs.Scope.Node = strings.TrimSpace(obs.Scope.Node)
	obs.Scope.Backend = strings.TrimSpace(obs.Scope.Backend)
	obs.Scope.Tool = strings.TrimSpace(obs.Scope.Tool)
	obs.ModelName = strings.TrimSpace(obs.ModelName)
	if obs.ObservedAt.IsZero() {
		obs.ObservedAt = time.Now().UTC()
	} else {
		obs.ObservedAt = obs.ObservedAt.UTC()
	}
	if obs.SampleCount <= 0 {
		obs.SampleCount = 1
	}
	if obs.WallTimeMS <= 0 {
		obs.WallTimeMS = 1
	}
	if obs.PeakRAMMB < 0 {
		obs.PeakRAMMB = 0
	}
	if obs.PeakVRAMMB < 0 {
		obs.PeakVRAMMB = 0
	}
	return obs
}

func weightedAverage(current int64, currentSamples int, next int64, nextSamples int) int64 {
	if current <= 0 {
		return next
	}
	if next <= 0 {
		return current
	}
	totalSamples := currentSamples + nextSamples
	if totalSamples <= 0 {
		return next
	}
	total := current*int64(currentSamples) + next*int64(nextSamples)
	if total <= 0 {
		return next
	}
	return total / int64(totalSamples)
}

func mergeObservation(existing, next models.ExecutionObservation) models.ExecutionObservation {
	next = normalizeObservation(next)
	if existing.SampleCount <= 0 {
		return next
	}

	merged := existing
	merged.ObservedAt = next.ObservedAt
	merged.LastSuccess = next.LastSuccess
	merged.SampleCount = existing.SampleCount + next.SampleCount
	merged.WallTimeMS = weightedAverage(existing.WallTimeMS, existing.SampleCount, next.WallTimeMS, next.SampleCount)
	if next.PeakRAMMB > merged.PeakRAMMB {
		merged.PeakRAMMB = next.PeakRAMMB
	}
	if next.PeakVRAMMB > merged.PeakVRAMMB {
		merged.PeakVRAMMB = next.PeakVRAMMB
	}
	if next.ModelName != "" {
		merged.ModelName = next.ModelName
	}
	return merged
}

func (s *ClusterState) RecordObservation(obs models.ExecutionObservation) bool {
	if s == nil {
		return false
	}
	if s.Observations == nil {
		s.Observations = make(map[string]models.ExecutionObservation)
	}
	obs = normalizeObservation(obs)
	key := ObservationKey(obs.Scope)
	existing, ok := s.Observations[key]
	if ok {
		s.Observations[key] = mergeObservation(existing, obs)
		return true
	}
	s.Observations[key] = obs
	return true
}

func (s *ClusterState) Observation(scope models.ObservationScope) (*models.ExecutionObservation, bool) {
	if s == nil || len(s.Observations) == 0 {
		return nil, false
	}
	obs, ok := s.Observations[ObservationKey(scope)]
	if !ok {
		return nil, false
	}
	obsCopy := obs
	return &obsCopy, true
}

func ObservationIsFresh(obs models.ExecutionObservation, now time.Time) bool {
	if obs.ObservedAt.IsZero() {
		return false
	}
	if now.IsZero() {
		now = time.Now().UTC()
	}
	return now.Sub(obs.ObservedAt.UTC()) <= ObservationStaleAfter
}
