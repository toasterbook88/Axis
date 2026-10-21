package main

import (
	"fmt"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/persist"
)

// Open from the background job on the console path so local provenance probes
// cannot delay its first paint. Both CLI paths use the same private log policy.
func openAgentSessionLog(target ModelChoice, resumed bool, warn func(string)) *agent.DailySessionLog {
	l, err := agent.OpenDailySessionLog(persist.AxisPath("logs"))
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("session log disabled: %v", err))
		}
		return nil
	}
	l.SetWarn(warn)
	l.SetResumed(resumed)
	l.SetProvenance(agent.CaptureSessionLogProvenance(target))
	return l
}
