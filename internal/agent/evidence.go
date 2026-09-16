package agent

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/skills"
	"github.com/toasterbook88/axis/internal/state"
)

func redactCommandFromDecision(dec string) string {
	var sb strings.Builder
	i := 0
	for i < len(dec) {
		ch := dec[i]
		if ch == '\'' || ch == '"' {
			end := strings.IndexByte(dec[i+1:], ch)
			if end >= 0 {
				sb.WriteByte(ch)
				sb.WriteString("[REDACTED]")
				sb.WriteByte(ch)
				i += 1 + end + 1
				continue
			}
		}
		sb.WriteByte(ch)
		i++
	}
	return sb.String()
}

func sanitizeAndRedactEvidence(payload string) string {
	var sb strings.Builder
	for _, r := range payload {
		if r < 32 && r != '\n' && r != '\t' {
			sb.WriteRune(' ')
		} else {
			sb.WriteRune(r)
		}
	}
	s := sb.String()
	s = bearerRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = apiKeyHeaderRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = authHeaderRegex.ReplaceAllString(s, "${1}[REDACTED]")
	s = genericSecretRegex.ReplaceAllString(s, "${1}${2}[REDACTED]")
	return s
}

// clusterContextSnippet returns a compact live cluster snapshot for injection
// into the system prompt each turn, so the agent stays aware of node health,
// free memory, and resident models and can adapt placement mid-task.
func (a *Agent) clusterContextSnippet() string {
	if a.toolContext == nil {
		return ""
	}
	view := a.toolContext.GetView()
	if view == nil || view.Snapshot == nil {
		return ""
	}
	summary := summarizeSnapshot(view.Snapshot)
	if summary == "" {
		return ""
	}
	return "<live_cluster_context>\n" + summary + "\n</live_cluster_context>"
}

func (a *Agent) retrieveEvidence(userPrompt string) string {
	var recentDecisions []string
	if a.toolContext != nil {
		view := a.toolContext.GetView()
		if view != nil && view.State != nil {
			recentDecisions = view.State.Decisions
		}
	}
	if len(recentDecisions) == 0 {
		if st, err := state.Load(); err == nil && st != nil {
			recentDecisions = st.Decisions
		}
	}

	var matchedSkill skills.LearnedSkill
	var matched bool
	if a.toolContext != nil {
		view := a.toolContext.GetView()
		if view != nil && view.Skills != nil {
			matchedSkill, matched = view.Skills.BestMatch(userPrompt)
		}
	}
	if !matched {
		if sk, err := skills.Load(); err == nil && sk != nil {
			matchedSkill, matched = sk.BestMatch(userPrompt)
		}
	}

	if a.securityClass == BackendRemote {
		return a.remoteEvidence(matchedSkill, matched)
	}
	return a.localEvidence(recentDecisions, matchedSkill, matched)
}

func (a *Agent) remoteEvidence(skill skills.LearnedSkill, matched bool) string {
	if !matched {
		return ""
	}
	var b strings.Builder
	b.WriteString("Relevant learned skill matching current query:\n")
	b.WriteString(fmt.Sprintf("- ID: %s\n", skill.ID))
	b.WriteString(fmt.Sprintf("  Success Count: %d\n", skill.SuccessCount))
	b.WriteString(fmt.Sprintf("  Last Used: %s\n", skill.LastUsed.Format(time.RFC3339)))
	if skill.PreferredNode != "" {
		b.WriteString(fmt.Sprintf("  Preferred Node: %s\n", skill.PreferredNode))
	}
	if len(skill.NodeCount) > 0 {
		b.WriteString(fmt.Sprintf("  Node success counts: %s\n", formatNodeCounts(skill.NodeCount)))
	}
	return wrapEvidence(b.String())
}

func formatNodeCounts(counts map[string]int) string {
	nodes := make([]string, 0, len(counts))
	for n := range counts {
		nodes = append(nodes, n)
	}
	sort.Strings(nodes)
	parts := make([]string, 0, len(nodes))
	for _, n := range nodes {
		parts = append(parts, fmt.Sprintf("%s: %d successes", n, counts[n]))
	}
	return strings.Join(parts, ", ")
}

func (a *Agent) localEvidence(recentDecisions []string, skill skills.LearnedSkill, matched bool) string {
	excludeRawCommands := !a.allowRawCommandEvidence

	var decisionsStr []string
	if len(recentDecisions) > 0 {
		last := recentDecisions
		if len(last) > 5 {
			last = last[len(last)-5:]
		}
		for _, dec := range last {
			d := dec
			if excludeRawCommands {
				d = redactCommandFromDecision(d)
			}
			decisionsStr = append(decisionsStr, fmt.Sprintf("- %s", d))
		}
	}

	var b strings.Builder
	if len(decisionsStr) > 0 {
		b.WriteString("Recent placement decisions:\n")
		b.WriteString(strings.Join(decisionsStr, "\n"))
		b.WriteString("\n")
	}
	if matched {
		b.WriteString("Relevant learned skill matching current query:\n")
		b.WriteString(fmt.Sprintf("- Description: %s\n", skill.Description))
		if excludeRawCommands {
			b.WriteString("  Suggested Command: [REDACTED]\n")
		} else {
			b.WriteString(fmt.Sprintf("  Suggested Command: %s\n", skill.Command))
		}
		b.WriteString(fmt.Sprintf("  Success Count: %d\n", skill.SuccessCount))
		b.WriteString(fmt.Sprintf("  Last Used: %s\n", skill.LastUsed.Format(time.RFC3339)))
		if skill.PreferredNode != "" {
			b.WriteString(fmt.Sprintf("  Preferred Node: %s\n", skill.PreferredNode))
		}
		if len(skill.NodeCount) > 0 {
			b.WriteString(fmt.Sprintf("  Node success counts: %s\n", formatNodeCounts(skill.NodeCount)))
		}
	}

	payload := b.String()
	if payload == "" {
		return ""
	}
	sanitizedPayload := sanitizeAndRedactEvidence(payload)
	return wrapEvidence(sanitizedPayload)
}

func wrapEvidence(payload string) string {
	// Wrapper overhead:
	// prefix: "<untrusted_historical_evidence>\n" (31 bytes)
	// suffix: "\n</untrusted_historical_evidence>" (32 bytes)
	// Total overhead: 63 bytes. Max payload: 2048 - 63 = 1985.
	truncatedPayload := truncateUTF8(payload, 1985)
	return "<untrusted_historical_evidence>\n" + truncatedPayload + "\n</untrusted_historical_evidence>"
}
