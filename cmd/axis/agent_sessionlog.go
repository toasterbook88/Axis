package main

import (
	"fmt"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/persist"
)

// Open from the background job on the console path so local provenance probes
// cannot delay its first paint. Both CLI paths use the same private log policy.
// The log starts unresumed; resumeConversation marks it once history loads.
func openAgentSessionLog(target ModelChoice, warn func(string)) *agent.DailySessionLog {
	l, err := agent.OpenDailySessionLog(persist.AxisPath("logs"))
	if err != nil {
		if warn != nil {
			warn(fmt.Sprintf("session log disabled: %v", err))
		}
		return nil
	}
	l.SetWarn(warn)
	l.SetProvenance(agent.CaptureSessionLogProvenance(target))
	return l
}

// resumeConversation loads the interactive history into a and returns the
// number of prior messages. The session log is marked resumed only when
// messages loaded, so a missing, unreadable or empty history file does not
// label a fresh run as a resume. A nil log is ignored.
func resumeConversation(a *agent.Agent, historyPath string, sessionLog *agent.DailySessionLog) (int, error) {
	if err := a.Conversation().LoadFromFile(historyPath); err != nil {
		return 0, err
	}
	n := a.Conversation().HistoryCount()
	if n > 0 {
		sessionLog.SetResumed(true)
	}
	return n, nil
}
