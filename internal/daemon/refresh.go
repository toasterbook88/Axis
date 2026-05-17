package daemon

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/execution"
)

const (
	RefreshTriggerManual       = "manual"
	RefreshTriggerStartup      = "startup"
	RefreshTriggerInterval     = "interval"
	RefreshTriggerConfigChange = "config-change"
	RefreshTriggerStateChange  = "state-change"
	RefreshTriggerSkillsChange = "skills-change"
	RefreshTriggerBeaconChange = "beacon-change"
	RefreshTriggerDiscovery    = "discovery"
	runtimeRefreshTimeout      = 30 * time.Second
)

// NormalizeRefreshTrigger validates and canonicalizes daemon refresh trigger
// labels. Empty input maps to the explicit manual trigger.
// Supports comma-separated coalesced triggers by validating, sorting, and deduping them.
func NormalizeRefreshTrigger(trigger string) (string, error) {
	trigger = strings.ToLower(strings.TrimSpace(trigger))
	if trigger == "" {
		return RefreshTriggerManual, nil
	}

	parts := strings.Split(trigger, ",")
	var normalized []string
	seen := make(map[string]bool)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		switch p {
		case RefreshTriggerManual,
			RefreshTriggerStartup,
			RefreshTriggerInterval,
			RefreshTriggerConfigChange,
			RefreshTriggerStateChange,
			RefreshTriggerSkillsChange,
			RefreshTriggerBeaconChange,
			RefreshTriggerDiscovery,
			execution.StateChangeExecutionReserved,
			execution.StateChangeExecutionFinished:
			if !seen[p] {
				seen[p] = true
				normalized = append(normalized, p)
			}
		default:
			return "", fmt.Errorf("unsupported refresh trigger %q", trigger)
		}
	}

	if len(normalized) == 0 {
		return RefreshTriggerManual, nil
	}

	sort.Strings(normalized)
	return strings.Join(normalized, ","), nil
}
