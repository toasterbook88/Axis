package agent

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/git"
)

// injectTurnContext puts per-turn context (prompt evidence, the live cluster
// snapshot) at the start of the newest user message instead of inside the
// system prompt.
//
// Why: the system prompt and the tool definitions are the same on every run
// and every turn (about 10k tokens with the default tool set). Local servers
// such as llama.cpp reuse a cached prompt only up to the first byte that
// differs. Text that changes every turn (the snapshot's age, free memory)
// inside the system prompt makes everything after it, including all tool
// definitions, look new, so the whole prompt is re-read on every turn.
// Measured on a 27B model on an 11 GB GPU: 12,008 prompt tokens re-read in 16.5 s
// per turn with the snapshot in the system prompt; 1,048 tokens in 1.8 s once
// it moved next to the user message.
//
// Blank blocks are dropped. If there is no user message (callers normally
// guarantee one), the context falls back to the first system message, then
// to a new leading system message, matching the previous behaviour.
func injectTurnContext(msgs []chat.Message, blocks ...string) []chat.Message {
	var parts []string
	for _, b := range blocks {
		if strings.TrimSpace(b) != "" {
			parts = append(parts, b)
		}
	}
	if len(parts) == 0 {
		return msgs
	}
	ctx := strings.Join(parts, "\n\n")

	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == chat.RoleUser {
			out := make([]chat.Message, len(msgs))
			copy(out, msgs)
			if out[i].Content != "" {
				out[i].Content = ctx + "\n\n" + out[i].Content
			} else {
				out[i].Content = ctx
			}
			return out
		}
	}
	for i := range msgs {
		if msgs[i].Role == chat.RoleSystem {
			out := make([]chat.Message, len(msgs))
			copy(out, msgs)
			if out[i].Content != "" {
				out[i].Content += "\n\n" + ctx
			} else {
				out[i].Content = ctx
			}
			return out
		}
	}
	return append([]chat.Message{{Role: chat.RoleSystem, Content: ctx}}, msgs...)
}

// formatGitContext renders the session-start git block that used to sit in
// the system prompt. A commit or one edited file changed it, so a local prompt
// cache could not reuse the system prompt between runs.
func formatGitContext(st git.RepoState) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Git repository context (at session start):\n- Branch: %s\n- HEAD Commit: %s (Subject: %s)\n", st.Branch, st.Commit, st.Subject)
	if st.IsDirty {
		fmt.Fprintf(&b, "- Status: Dirty (%d files changed)\n", st.DirtyCount)
	} else {
		b.WriteString("- Status: Clean\n")
	}
	return b.String()
}
