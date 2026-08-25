package agent

import (
	"errors"
	"fmt"

	"github.com/toasterbook88/axis/internal/ui"
)

// Observer receives structured agent-loop events.
//
// The agent has two output channels with different shapes. The assistant's own
// token stream is unstructured text and keeps flowing through Config.Output
// (wrapped by ColorWriter). Everything else — tool calls, results, failures,
// shell execution, turn boundaries — is structured, and a surface that renders
// its own transcript needs it as events rather than pre-formatted lines.
//
// When Config.Observer is nil the agent formats these events to Config.Output
// in its long-standing shape. Note that the fallback is no longer byte-for-byte
// what it used to print: redaction now applies to both paths, so a
// credential-shaped value is masked for the plain CLI too.
//
// Redacted at this boundary, for both paths:
//
//	ToolCalled.args        ToolSucceeded.summary
//	ToolFailed.err         ShellExecuting.command
//	ToolSkipped.reason     CompactionSkipped.err
//
// Not redacted, and deliberately so:
//
//	the assistant's token stream, which flows through Config.Output rather
//	than through an Observer. It is model output rather than command or
//	credential text, and it is still an egress path into scrollback — a
//	surface that renders it is responsible for treating it as untrusted.
//
// Implementations must be safe for concurrent use: tool results are reported
// from the parallel dispatch goroutines. An implementation must not block for
// long and must not write to the terminal directly — a surface that owns the
// screen has to route these through its own render loop.
type Observer interface {
	// TurnStarted reports the start of agent loop iteration turn of max.
	// Only emitted in verbose mode.
	TurnStarted(turn, max int)

	// CompactionSkipped reports that context compaction did not run.
	// Only emitted in verbose mode. The error text is redacted.
	CompactionSkipped(err error)

	// ToolCalled reports a tool the model asked for. id is the tool-call
	// identifier, stable across the call and its result, which is how a
	// completion is matched to its invocation when tools run in parallel.
	// args is the redacted argument blob and may be empty.
	ToolCalled(id, name, args string)

	// ToolSkipped reports a call not executed, such as under --dry-run.
	// reason is redacted.
	ToolSkipped(id, name, reason string)

	// ToolSucceeded reports a completed call. summary is the one-line human
	// summary; resultLen is the full result size in bytes.
	ToolSucceeded(id, name, summary string, resultLen int)

	// ToolFailed reports a call that returned an error.
	ToolFailed(id, name string, err error)

	// ShellExecuting reports a shell command about to run. node is empty for
	// the local machine; cwd may be empty. command is redacted.
	ShellExecuting(id, node, cwd, command string)

	// MaxTurnsReached reports that the loop stopped at its turn ceiling.
	MaxTurnsReached(max int)
}

// redactForSurface strips control characters and masks credential-shaped
// values before text leaves the agent. It uses the same seam as execution
// evidence so every egress path redacts identically.
//
// Pattern-based masking is deliberate: blanking every quoted argument would
// make the transcript useless as an audit trail, which is the reason the
// operator is being shown the command at all.
func redactForSurface(s string) string {
	if s == "" {
		return ""
	}
	return sanitizeAndRedactEvidence(s)
}

// redactErrorForSurface applies the same masking to an error's text. Errors
// routinely quote the command or payload that failed.
func redactErrorForSurface(err error) error {
	if err == nil {
		return nil
	}
	redacted := redactForSurface(err.Error())
	if redacted == err.Error() {
		return err
	}
	return errors.New(redacted)
}

// The emit* helpers are the single place the agent decides between an observer
// and formatted output, and the single place redaction is applied. Keeping the
// fallback Fprintf calls here means the default rendering lives in one file
// rather than scattered through the loop.

func (a *Agent) emitTurnStarted(turn, max int) {
	if a.observer != nil {
		a.observer.TurnStarted(turn, max)
		return
	}
	fmt.Fprintf(a.output, "\n%s\n", ui.Dim(fmt.Sprintf("─── Turn %d/%d ──────────────────────────────────────────────────", turn, max)))
}

func (a *Agent) emitCompactionSkipped(err error) {
	err = redactErrorForSurface(err)
	if a.observer != nil {
		a.observer.CompactionSkipped(err)
		return
	}
	fmt.Fprintf(a.output, "  %s compaction skipped: %v\n", ui.Dim("♻"), err)
}

func (a *Agent) emitToolCalled(id, name, args string) {
	args = redactForSurface(args)
	if a.observer != nil {
		a.observer.ToolCalled(id, name, args)
		return
	}
	fmt.Fprintf(a.output, "\n%s Calling %s...\n", ui.Cyan("▶"), ui.Bold(name))
	if a.verbose && args != "" {
		fmt.Fprintf(a.output, "  %s Parameters: %s\n", ui.Dim("→"), args)
	}
}

func (a *Agent) emitToolSkipped(id, name, reason string) {
	reason = redactForSurface(reason)
	if a.observer != nil {
		a.observer.ToolSkipped(id, name, reason)
		return
	}
	fmt.Fprintf(a.output, "  %s Skipped execution of %s\n", ui.Yellow("[dry-run]"), name)
}

func (a *Agent) emitToolSucceeded(id, name, summary string, resultLen int) {
	summary = redactForSurface(summary)
	if a.observer != nil {
		a.observer.ToolSucceeded(id, name, summary, resultLen)
		return
	}
	fmt.Fprintf(a.output, "%s %s\n", ui.Green("✓"), summary)
	if a.verbose {
		fmt.Fprintf(a.output, "  %s Result: %d chars\n", ui.Dim("←"), resultLen)
	}
}

func (a *Agent) emitToolFailed(id, name string, err error) {
	err = redactErrorForSurface(err)
	if a.observer != nil {
		a.observer.ToolFailed(id, name, err)
		return
	}
	fmt.Fprintf(a.output, "  %s %s\n", ui.Red("⚠"),
		fmt.Sprintf("Error executing tool %q: %s", name, err.Error()))
}

func (a *Agent) emitShellExecuting(id, node, cwd, command string) {
	command = redactForSurface(command)
	if a.observer != nil {
		a.observer.ShellExecuting(id, node, cwd, command)
		return
	}
	switch {
	case node != "":
		fmt.Fprintf(a.output, "\n▶ Executing on %s: %s\n", node, command)
	case cwd != "":
		fmt.Fprintf(a.output, "\n▶ Executing shell (in %s): %s\n", cwd, command)
	default:
		fmt.Fprintf(a.output, "\n▶ Executing shell: %s\n", command)
	}
}

func (a *Agent) emitMaxTurnsReached(max int) {
	if a.observer != nil {
		a.observer.MaxTurnsReached(max)
		return
	}
	fmt.Fprintf(a.output, "\n⚠ Agent reached maximum turns (%d). Stopping.\n", max)
}
