package agent

import (
	"fmt"

	"github.com/toasterbook88/axis/internal/safety"
)

// shellSafety scores a run_shell command with its working directory, before
// confirmation, and returns the command line to run. The command is scored as
// before. A cwd is checked as "cd <cwd>" on its own and replaces the
// command's score only when it is a deny or a specific risk (an AskAlways
// prompt, such as a directory inside node_modules): an ordinary cwd is an
// unknown command and must not raise every command with a cwd to 70.
func (a *Agent) shellSafety(command, cwd string) (allowed bool, reason string, score int, cmdToRun string, err error) {
	allowed, reason, score = a.safety(command)
	cmdToRun = command
	if cwd == "" {
		return allowed, reason, score, cmdToRun, nil
	}
	cleanCwd, err := validateToolPath(cwd)
	if err != nil {
		return false, "", 0, "", err
	}
	cdCmd := "cd " + shellQuote(cleanCwd)
	cmdToRun = fmt.Sprintf("%s && %s", cdCmd, command)
	if cdAllowed, cdReason, cdScore := a.safety(cdCmd); (!cdAllowed || cdScore >= safety.ScoreAskAlways) && cdScore > score {
		allowed, reason, score = cdAllowed && allowed, cdReason, cdScore
	}
	return allowed, reason, score, cmdToRun, nil
}
