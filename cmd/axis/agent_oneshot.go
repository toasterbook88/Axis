package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
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

// pipedConfirm is the fail-closed confirm for -p runs: a piped invocation
// cannot answer an interactive prompt, so everything reaching the confirm
// layer is denied with a notice pointing at the flags that suppress the
// question (--auto-approve or --autonomy full). Those wrappers sit ABOVE
// this base in the agent's confirm chain, so safe commands under
// --auto-approve never reach it.
func pipedConfirm(w io.Writer) agent.ConfirmFunc {
	return func(tool, desc string, score int) agent.ConfirmResult {
		fmt.Fprintf(w, "✗ %s denied: not an interactive session (pass --autonomy full; --auto-approve only clears low-risk tools)\n", tool)
		return agent.ConfirmNo
	}
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
func confirmForPipedMode(printMode bool, errW io.Writer) agent.ConfirmFunc {
	if !printMode {
		return nil
	}
	return pipedConfirm(errW)
}

// runOneShotPiped runs one instruction with the piped output contract:
// assistant text on stdout (streamed through the agent's ColorWriter as
// usual), tool badges and the session summary on stderr, no session banner.
// The confirm layer is fail-closed (pipedConfirm) so a piped run never
// blocks on an interactive prompt.
func runOneShotPiped(ctx context.Context, a *agent.Agent, instruction string, errW io.Writer, timeout time.Duration, historyPath string) error {
	ctx2, cancel := agentRequestContext(ctx, timeout)
	defer cancel()
	start := time.Now()
	if err := a.Run(ctx2, instruction); err != nil {
		fmt.Fprintf(errW, "error: Agent failed: %v\n", err)
		return ExitCodeError{Code: ExitErrCommandFail, Message: fmt.Sprintf("agent failed: %v", err)}
	}
	fmt.Fprintf(errW, "── ~%d tokens (estimate) · %s elapsed ──\n",
		a.ContextTokens(), time.Since(start).Round(time.Second))
	if historyPath != "" {
		_ = saveAgentConversation(a.Conversation(), historyPath, errW)
	}
	return nil
}
