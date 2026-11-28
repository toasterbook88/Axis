package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/safety"
	"github.com/toasterbook88/axis/internal/ui"
)

// pipedToolObserver writes compact tool badges to stderr for -p runs:
// stdout carries only the assistant's text, stderr is the diagnostics
// channel, so `axis agent -p "…" | jq` stays clean. Receipts use the
// agent-measured execution time carried by the completion events (Track 3).
type pipedToolObserver struct {
	w       io.Writer
	verbose bool
}

var _ agent.Observer = (*pipedToolObserver)(nil)

// clipLine truncates to n runes, appending "…" when it bites.
func clipLine(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n-1]) + "…"
}

func (o *pipedToolObserver) ToolCalled(id, name, args string) {
	line := "→ " + name
	if first := strings.SplitN(strings.TrimSpace(args), "\n", 2)[0]; first != "" {
		line += " " + clipLine(first, 80)
	}
	fmt.Fprintf(o.w, "%s\n", line)
}

func (o *pipedToolObserver) ToolSkipped(id, name, reason string) {
	fmt.Fprintf(o.w, "  [dry-run] %s skipped: %s\n", name, reason)
}

func (o *pipedToolObserver) ToolSucceeded(id, name, summary string, resultLen int, elapsed time.Duration) {
	fmt.Fprintf(o.w, "✓ %s (%s, %d bytes)\n", name, elapsed.Truncate(time.Millisecond), resultLen)
}

func (o *pipedToolObserver) ToolFailed(id, name string, err error, elapsed time.Duration) {
	fmt.Fprintf(o.w, "✗ %s (%s, %s elapsed)\n", name, err.Error(), elapsed.Truncate(time.Millisecond))
}

func (o *pipedToolObserver) ShellExecuting(id, node, cwd, command string) {
	target := "local"
	switch {
	case node != "":
		target = node
	case cwd != "":
		target = cwd
	}
	fmt.Fprintf(o.w, "  ▶ %s: %s\n", target, command)
}

func (o *pipedToolObserver) TurnStarted(turn, max int) {
	if o.verbose {
		fmt.Fprintf(o.w, "turn %d/%d\n", turn, max)
	}
}

func (o *pipedToolObserver) CompactionSkipped(err error) {
	if o.verbose {
		fmt.Fprintf(o.w, "compaction skipped: %v\n", err)
	}
}

func (o *pipedToolObserver) MaxTurnsReached(max int) {
	fmt.Fprintf(o.w, "stopped at the %d-turn ceiling\n", max)
}

// pendingApproval is one confirm a piped run denied because nobody could
// answer it. The run reports these as its needs_input outcome.
type pendingApproval struct {
	Tool      string `json:"tool"`
	Request   string `json:"request"`
	RiskScore int    `json:"risk_score"`
	Clears    string `json:"clears"` // what would let a rerun proceed
}

// pipedApprovals collects the confirms a -p run denied. Confirms can arrive
// from concurrent tool dispatch, so it locks.
type pipedApprovals struct {
	mu    sync.Mutex
	items []pendingApproval
}

// add records item once: a model that retries the same denied request
// every round still counts as one step.
func (p *pipedApprovals) add(item pendingApproval) {
	if p == nil {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, have := range p.items {
		if have.Tool == item.Tool && have.Request == item.Request {
			return
		}
	}
	p.items = append(p.items, item)
}

func (p *pipedApprovals) list() []pendingApproval {
	if p == nil {
		return nil
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]pendingApproval(nil), p.items...)
}

// pipedConfirm is the fail-closed confirm for -p runs: a piped invocation
// cannot answer an interactive prompt, so everything reaching the confirm
// layer is denied. The --auto-approve and --autonomy wrappers sit ABOVE this
// base in the agent's confirm chain, so what reaches it is what those flags
// did not clear. Each notice, and each recorded denial's clears, names what
// would let a rerun through:
//   - external MCP tools ask in every autonomy mode unless nodes.yaml lists
//     them under mcp_servers.<server>.auto_approve_tools;
//   - consult_model and any command scoring ScoreAskAlways or more (a
//     specific safety rule's ask, or a safety-blocked override) ask even
//     under --autonomy full, so only an interactive run clears them;
//   - a run_shell, run_on_node or fleet_exec command a safety rule denies
//     outright (score 100) clears with nothing: guarded execution
//     re-evaluates the same rule set and blocks it even after an interactive
//     override;
//   - anything else clears with --autonomy full.
//
// Each denial is also recorded in rec (nil records nothing) so the run can
// report needs_input.
func pipedConfirm(w io.Writer, rec *pipedApprovals) agent.ConfirmFunc {
	return func(tool, desc string, score int) agent.ConfirmResult {
		item := pendingApproval{Tool: tool, Request: clipLine(desc, 400), RiskScore: score}
		switch {
		case strings.HasPrefix(tool, "mcp_"):
			fmt.Fprintf(w, "✗ %s denied: not an interactive session (external MCP tools ask in every autonomy mode; list it under mcp_servers.<server>.auto_approve_tools in nodes.yaml, or run interactively)\n", tool)
			item.Clears = pipedClearsMCPAllowlist
		case tool == "consult_model":
			fmt.Fprintf(w, "✗ %s denied: not an interactive session (consult_model still asks under --autonomy full and --auto-approve; run interactively)\n", tool)
			item.Clears = pipedClearsInteractive
		case score >= 100 && guardedRecheck(tool):
			fmt.Fprintf(w, "✗ %s denied: a safety rule denies it outright, and guarded execution enforces the same rule after any override (no approval clears it)\n", tool)
			item.Clears = pipedClearsNone
		case score >= 80:
			fmt.Fprintf(w, "✗ %s denied: not an interactive session, and safety blocked it (only an interactive override can run it)\n", tool)
			item.Clears = pipedClearsInteractive
		case score >= safety.ScoreAskAlways:
			fmt.Fprintf(w, "✗ %s denied: not an interactive session, and a specific safety rule flagged it (run interactively to approve)\n", tool)
			item.Clears = pipedClearsInteractive
		default:
			fmt.Fprintf(w, "✗ %s denied: not an interactive session (pass --autonomy full; --auto-approve only clears low-risk tools)\n", tool)
			item.Clears = pipedClearsAutonomyFull
		}
		rec.add(item)
		return agent.ConfirmNo
	}
}

// Values of pendingApproval.Clears.
const (
	pipedClearsAutonomyFull = "--autonomy full"
	pipedClearsInteractive  = "interactive approval"
	pipedClearsMCPAllowlist = "auto_approve_tools"
	pipedClearsNone         = "none"
)

// guardedRecheck reports whether tool's command runs through the guarded
// runner (guardedAgentCommandRunner: run_shell as RunShell, run_on_node and
// each fleet_exec node as RunOnNode), whose PrepareGuardedExecution applies
// the same safety rule set again, so an override of a deny still ends blocked.
func guardedRecheck(tool string) bool {
	switch tool {
	case "run_shell", "run_on_node", "fleet_exec":
		return true
	}
	return false
}

// observerForPipedMode returns the stderr badge observer for -p runs and nil
// otherwise (nil keeps the agent's long-standing stdout fallback formatting
// for positional one-shot and interactive use).
func observerForPipedMode(printMode bool, errW io.Writer, verbose bool) agent.Observer {
	if !printMode {
		return nil
	}
	return &pipedToolObserver{w: errW, verbose: verbose}
}

// legacyOneShotBanner prints the session framing the pre-Track-5 one-shot
// showed. -p suppresses it; the positional branch keeps it byte-for-byte.
func legacyOneShotBanner(w io.Writer, model string, maxTurns int) {
	fmt.Fprintf(w, "Agent [%s] — max %d turns\n\n", ui.Bold(model), maxTurns)
}

// confirmForPipedMode returns the fail-closed confirm for -p runs and nil
// otherwise (nil keeps the agent's StdinConfirm default for interactive and
// positional use).
func confirmForPipedMode(printMode bool, errW io.Writer, rec *pipedApprovals) agent.ConfirmFunc {
	if !printMode {
		return nil
	}
	return pipedConfirm(errW, rec)
}

// Outcomes of a -p run. They are the "status" of the --format json document
// and each maps to its own exit code (classifyPipedRun).
const (
	pipedOutcomeAnswered    = "answered"
	pipedOutcomeTurnCeiling = "turn_ceiling"
	pipedOutcomeNeedsInput  = "needs_input"
	pipedOutcomeFailed      = "failed"
)

// classifyPipedRun turns how a -p run ended into its outcome and exit code.
// An error wins. Then a denied approval: the run could not do part of what
// was asked, and a rerun with approval may finish it, whether or not the
// model went on to answer or ran out of turns. Then the turn ceiling.
func classifyPipedRun(runErr error, stop agent.RunStop, pending int) (string, int) {
	switch {
	case runErr != nil || stop == agent.RunStopFailed:
		return pipedOutcomeFailed, ExitErrCommandFail
	case pending > 0:
		return pipedOutcomeNeedsInput, ExitErrAgentNeedsInput
	case stop == agent.RunStopTurnCeiling:
		return pipedOutcomeTurnCeiling, ExitErrAgentTurnCeiling
	default:
		return pipedOutcomeAnswered, ExitOK
	}
}

// pipedRunResult is the data payload of `axis agent -p --format json`.
type pipedRunResult struct {
	Outcome          string            `json:"outcome"`
	ExitCode         int               `json:"exit_code"`
	Turns            int               `json:"turns"`
	MaxTurns         int               `json:"max_turns"`
	FinalText        string            `json:"final_text"`
	PendingApprovals []pendingApproval `json:"pending_approvals"`
	Error            string            `json:"error,omitempty"`
	Usage            pipedRunUsage     `json:"usage"`
	ElapsedMS        int64             `json:"elapsed_ms"`
}

type pipedRunUsage struct {
	TokensIn  int  `json:"tokens_in"`
	TokensOut int  `json:"tokens_out"`
	Estimated bool `json:"estimated"`
}

// lastAssistantText returns the content of the newest assistant message. At
// the turn ceiling that is whatever the model said alongside its last tool
// calls, often empty.
func lastAssistantText(a *agent.Agent) string {
	msgs := a.Conversation().Messages()
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == chat.RoleAssistant {
			return msgs[i].Content
		}
	}
	return ""
}

// pipedRunOptions carries what runOneShotPiped needs beyond the agent.
type pipedRunOptions struct {
	Out         io.Writer // stdout: the --format json document
	ErrW        io.Writer
	Timeout     time.Duration
	HistoryPath string
	Format      string // "text" (default) or "json"
	Approvals   *pipedApprovals
}

// runOneShotPiped runs one instruction with the piped output contract:
// assistant text on stdout (streamed through the agent's ColorWriter as
// usual), tool badges and the session summary on stderr, no session banner.
// The confirm layer is fail-closed (pipedConfirm) so a piped run never
// blocks on an interactive prompt. With --format json the caller has sent
// the stream to stderr, and stdout carries one axis.output/v1 document.
//
// Exit codes: 0 answered, 8 turn ceiling, 9 needs input, 4 failed.
func runOneShotPiped(ctx context.Context, a *agent.Agent, instruction string, opts pipedRunOptions) error {
	errW := opts.ErrW
	ctx2, cancel := agentRequestContext(ctx, opts.Timeout)
	defer cancel()
	start := time.Now()
	runErr := a.Run(ctx2, instruction)
	ro := a.LastRunOutcome()
	if runErr == nil && ro.Stop == agent.RunStopTurnCeiling && ctx2.Err() != nil {
		// The deadline or a signal cut the last round's tools short; that
		// is a failed run, not one that used its turns.
		runErr = ctx2.Err()
	}
	if runErr != nil {
		fmt.Fprintf(errW, "error: Agent failed: %v\n", runErr)
	}
	// A call the autonomy mode hid never reached the confirm, but a rerun
	// with --autonomy full would let it run, so it counts the same way.
	for _, tool := range ro.ScopeRefused {
		opts.Approvals.add(pendingApproval{
			Tool: tool, Request: "hidden by the current --autonomy mode", Clears: pipedClearsAutonomyFull,
		})
	}
	pending := opts.Approvals.list()
	outcome, code := classifyPipedRun(runErr, ro.Stop, len(pending))

	usageIn, usageOut, usageTurns := a.UsageStats()
	if runErr == nil {
		if usageTurns > 0 {
			fmt.Fprintf(errW, "── %d tokens in · %d out (real, %d turns) · %s elapsed ──\n",
				usageIn, usageOut, usageTurns, time.Since(start).Round(time.Second))
		} else {
			fmt.Fprintf(errW, "── ~%d tokens (estimate) · %s elapsed ──\n",
				a.ContextTokens(), time.Since(start).Round(time.Second))
		}
		if opts.HistoryPath != "" {
			_ = saveAgentConversation(a.Conversation(), opts.HistoryPath, errW)
		}
	}

	if opts.Format == "json" {
		res := pipedRunResult{
			Outcome:          outcome,
			ExitCode:         code,
			Turns:            ro.Turns,
			MaxTurns:         ro.MaxTurns,
			FinalText:        lastAssistantText(a),
			PendingApprovals: pending,
			Usage:            pipedRunUsage{TokensIn: usageIn, TokensOut: usageOut},
			ElapsedMS:        time.Since(start).Milliseconds(),
		}
		if res.PendingApprovals == nil {
			res.PendingApprovals = []pendingApproval{}
		}
		if usageTurns == 0 {
			res.Usage = pipedRunUsage{TokensIn: a.ContextTokens(), Estimated: true}
		}
		if runErr != nil {
			res.Error = runErr.Error()
			res.FinalText = ""
		}
		if err := writeMachineOutput(opts.Out, "agent", outcome, code == ExitOK, res, nil); err != nil {
			return ExitCodeError{Code: ExitErrIO, Message: fmt.Sprintf("write agent result: %v", err)}
		}
	}

	switch outcome {
	case pipedOutcomeFailed:
		return ExitCodeError{Code: code, Message: fmt.Sprintf("agent failed: %v", runErr)}
	case pipedOutcomeNeedsInput:
		return ExitCodeError{Code: code, Message: fmt.Sprintf("agent needs input: %d step(s) need an approval a piped run cannot give", len(pending))}
	case pipedOutcomeTurnCeiling:
		return ExitCodeError{Code: code, Message: fmt.Sprintf("agent stopped at the %d-turn ceiling without a final answer", ro.MaxTurns)}
	}
	return nil
}

// writePipedStartupFailure writes the --format json document for a -p run
// that failed before the agent ran (config, model or backend startup), so a
// caller parsing stdout still gets one document. exit_code is the code the
// process exits with.
func writePipedStartupFailure(out io.Writer, err error, maxTurns int) {
	code := ExitCode(err)
	res := pipedRunResult{
		Outcome:          pipedOutcomeFailed,
		ExitCode:         code,
		MaxTurns:         maxTurns,
		PendingApprovals: []pendingApproval{},
		Error:            err.Error(),
	}
	_ = writeMachineOutput(out, "agent", pipedOutcomeFailed, false, res, nil)
}
