package execution

import (
	"strings"

	"github.com/toasterbook88/axis/internal/safety"
)

// applySafetyVerdict records the evaluator's decision on res and reports
// whether the command is blocked. A deny blocks with score 100. A prompt from
// a rule marked AskAlways (a specific risk) is not blocked but carries
// safety.ScoreAskAlways and the rule's reason, so full autonomy asks the
// operator before axis_run_task runs it. Any other verdict leaves res as is.
func applySafetyVerdict(res *GuardedExecutionResult, d safety.Decision) bool {
	switch {
	case d.Verdict == safety.VerdictDeny:
		res.Blocked = true
		res.BlockReason = strings.Join(d.Reasons, "; ")
		res.DumbScore = 100
		res.Error = res.BlockReason
		return true
	case d.Verdict == safety.VerdictPrompt && d.AskAlways:
		res.DumbScore = safety.ScoreAskAlways
		res.BlockReason = strings.Join(d.Reasons, "; ")
	}
	return false
}
