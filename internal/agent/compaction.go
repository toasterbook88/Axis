package agent

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/ui"
)

// compactionThreshold is the fraction of the token budget at which the agent
// proactively summarizes older conversation turns to preserve context.
const compactionThreshold = 0.70

// protectLastTurns is the number of most-recent messages always kept verbatim
// during compaction (the working context the model needs for the current task).
const protectLastTurns = 8

// minSummarizable is the minimum number of compactable messages required to
// bother making an LLM summarization round-trip.
const minSummarizable = 4

// maxSummarizeContentChars caps how much of any single message is fed to the
// summarizer, keeping the summarization prompt itself bounded.
const maxSummarizeContentChars = 2000

// compactContext summarizes older conversation turns into a single compressed
// system message when the conversation approaches the token budget. It is
// advisory: on any backend failure it silently falls back to the existing
// truncation policy in chat.Conversation, so the agent never breaks because
// compaction failed.
func (a *Agent) compactContext(ctx context.Context) error {
	if a.maxTokens <= 0 {
		return nil
	}
	if a.conv.EstimateTokens() < int(float64(a.maxTokens)*compactionThreshold) {
		return nil
	}

	candidates := a.conv.SummarizableMessages(protectLastTurns)
	if len(candidates) < minSummarizable {
		return nil
	}

	prompt := buildSummarizationPrompt(candidates)
	summary, err := a.summarizeViaBackend(ctx, prompt)
	if err != nil || strings.TrimSpace(summary) == "" {
		// Fallback: let chat.Conversation's built-in truncation handle it.
		return nil
	}

	// Replace the oldest non-system messages (up to len-protectLast) with a
	// single compacted summary message, preserving the system prompt and the
	// recent working context.
	start := a.conv.FirstNonSystemIndex()
	if start < 0 {
		return nil
	}
	end := a.conv.Len() - protectLastTurns
	if end <= start {
		return nil
	}
	a.conv.ReplaceRange(start, end, []chat.Message{
		{Role: chat.RoleSystem, Content: "[Compacted earlier conversation — summary]\n" + strings.TrimSpace(summary)},
	})

	if a.verbose {
		fmt.Fprintf(a.output, "%s Compacted %d older messages into a summary (%d → %d tokens)\n",
			ui.Dim("♻"), len(candidates), a.conv.EstimateTokens(), a.conv.EstimateTokens())
	}
	return nil
}

// buildSummarizationPrompt renders the candidate messages into a plain-text
// excerpt the summarizer model can compress. Tool calls are rendered inline so
// the summary preserves what was invoked.
func buildSummarizationPrompt(msgs []chat.Message) string {
	var sb strings.Builder
	sb.WriteString("Summarize the following conversation excerpt between a user and an AI agent with tools. ")
	sb.WriteString("Preserve every actionable detail: what was asked, which tools were called and their key results, ")
	sb.WriteString("decisions or approvals given, file paths touched, and any errors encountered. ")
	sb.WriteString("Be concise but lossless with respect to facts the agent still needs. ")
	sb.WriteString("Output only the summary, no preamble.\n\n")
	for _, m := range msgs {
		switch m.Role {
		case chat.RoleUser:
			sb.WriteString("User: ")
		case chat.RoleAssistant:
			sb.WriteString("Assistant: ")
		case chat.RoleTool:
			sb.WriteString("Tool result: ")
		default:
			continue
		}
		content := m.Content
		if len(content) > maxSummarizeContentChars {
			content = content[:maxSummarizeContentChars] + "…[truncated]"
		}
		sb.WriteString(content)
		sb.WriteString("\n")
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&sb, "  [tool call: %s(%s)]\n", tc.Function.Name, string(tc.Function.Arguments))
		}
	}
	return sb.String()
}

// summarizeViaBackend runs a single no-tools, non-streamed exchange against the
// active backend to produce a summary. Output is discarded from the user's
// stream so compaction is invisible.
func (a *Agent) summarizeViaBackend(ctx context.Context, prompt string) (string, error) {
	msgs := []chat.Message{
		{Role: chat.RoleSystem, Content: "You are a conversation summarizer. Output only the summary, no preamble."},
		{Role: chat.RoleUser, Content: prompt},
	}
	resp, err := a.client.ChatStream(ctx, msgs, nil, io.Discard)
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
