package chat

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/models"
)

// ClusterSummaryForPrompt is a lightweight view of the cluster state that can
// be injected into the system prompt without consuming excessive tokens.
type ClusterSummaryForPrompt struct {
	NodeCount      int
	ReachableCount int
	TotalRAMMB     int64
	FreeRAMMB      int64
	Status         string   // healthy, degraded
	Tools          []string // deduplicated tool names across cluster
}

// BuildClusterSummary extracts a lightweight prompt summary from a snapshot.
func BuildClusterSummary(snap *models.ClusterSnapshot) *ClusterSummaryForPrompt {
	if snap == nil || len(snap.Nodes) == 0 {
		return nil
	}
	s := &ClusterSummaryForPrompt{
		NodeCount:      snap.Summary.TotalNodes,
		ReachableCount: snap.Summary.ReachableNodes,
		TotalRAMMB:     snap.Summary.TotalRAMMB,
		FreeRAMMB:      snap.Summary.TotalFreeRAMMB,
		Status:         string(snap.Status),
	}
	seen := make(map[string]bool)
	for _, n := range snap.Nodes {
		for _, t := range n.Tools {
			if !seen[t.Name] {
				seen[t.Name] = true
				s.Tools = append(s.Tools, t.Name)
			}
		}
	}
	return s
}

// BuildSystemPrompt constructs the system prompt for a chat session.
// cluster may be nil if --context is not enabled.
// extra is an optional user-supplied addition (from --system flag).
func BuildSystemPrompt(cluster *ClusterSummaryForPrompt, extra string) string {
	var b strings.Builder

	b.WriteString("You are AXIS, a cluster-aware CLI assistant. ")
	b.WriteString("The user is the operator. Be concise and technical. ")
	b.WriteString("Never fabricate cluster facts — if uncertain, tell the user to run `axis status` or `axis facts` to get live data.\n\n")

	fmt.Fprintf(&b, "AXIS version: %s\n", buildinfo.Version)

	b.WriteString("\nAvailable AXIS commands the user can run:\n")
	b.WriteString("- `axis facts`          — local hardware facts\n")
	b.WriteString("- `axis status`         — cluster snapshot (all nodes)\n")
	b.WriteString("- `axis task place`     — advisory placement for a task\n")
	b.WriteString("- `axis task context`   — execution context for agents\n")
	b.WriteString("- `axis task run`       — execute a task on a selected node\n")
	b.WriteString("- `axis doctor`         — validate config, SSH, and daemon health\n")

	if cluster != nil {
		b.WriteString("\nCurrent cluster summary (snapshot at session start):\n")
		fmt.Fprintf(&b, "- Nodes: %d total, %d reachable\n", cluster.NodeCount, cluster.ReachableCount)
		fmt.Fprintf(&b, "- RAM: %d MB total, %d MB free\n", cluster.TotalRAMMB, cluster.FreeRAMMB)
		fmt.Fprintf(&b, "- Status: %s\n", cluster.Status)
		if len(cluster.Tools) > 0 {
			fmt.Fprintf(&b, "- Tools available: %s\n", strings.Join(cluster.Tools, ", "))
		}
		b.WriteString("Note: this summary may become stale during a long session. Use tools or tell the user to run `axis status` for fresh data.\n")
	}

	if extra != "" {
		b.WriteString("\nOperator instructions: ")
		b.WriteString(extra)
		b.WriteString("\n")
	}

	return b.String()
}
