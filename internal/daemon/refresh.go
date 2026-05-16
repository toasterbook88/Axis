package daemon

import (
	"fmt"
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
func NormalizeRefreshTrigger(trigger string) (string, error) {
	trigger = strings.ToLower(strings.TrimSpace(trigger))
	if trigger == "" {
		return RefreshTriggerManual, nil
	}

	switch trigger {
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
		return trigger, nil
	default:
		return "", fmt.Errorf("unsupported refresh trigger %q", trigger)
	}
}
