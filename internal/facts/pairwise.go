package facts

import (
	"context"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// BuildPairwiseLinkMatrix probes directional RTT from the local node to each
// remote node in the snapshot and returns a best-effort PairwiseLinkMatrix.
//
// Measurement model: from a single vantage (the local node), ping each
// remote's ResolvedDialTarget (falling back to SSHTarget). Cross-remote edges
// (remote→remote) are NOT probed directly — only local→remote is measured.
// Throughput is left at 0 (unmeasured); RTTLatencyP95 is the primary signal.
//
// The matrix is best-effort: nodes that can't be reached or where ping is
// unavailable are omitted rather than the whole build failing.
func BuildPairwiseLinkMatrix(ctx context.Context, localName string, nodes []models.NodeFacts) *models.PairwiseLinkMatrix {
	local := findNode(nodes, localName)
	if local == nil {
		return &models.PairwiseLinkMatrix{Links: nil}
	}

	links := make([]models.LinkMetric, 0, len(nodes))
	for i := range nodes {
		target := &nodes[i]
		if target.Name == local.Name {
			continue
		}
		host := target.ResolvedDialTarget
		if strings.TrimSpace(host) == "" {
			host = target.SSHTarget
		}
		if strings.TrimSpace(host) == "" {
			continue
		}
		rtt, ok := probeRTT(ctx, host)
		if !ok {
			continue
		}
		links = append(links, models.LinkMetric{
			SourceNode:    local.Name,
			TargetNode:    target.Name,
			OverlayType:   inferOverlay(host),
			RTTLatencyP95: rtt,
		})
	}
	return &models.PairwiseLinkMatrix{Links: links}
}

// findNode returns a pointer to the node with the given name, or nil.
func findNode(nodes []models.NodeFacts, name string) *models.NodeFacts {
	for i := range nodes {
		if nodes[i].Name == name {
			return &nodes[i]
		}
	}
	return nil
}

// probeRTT sends a few ICMP pings to host and returns the worst-case
// round-trip from the ping summary. Returns (0, false) if ping is unavailable
// or the host is unreachable. Uses a short deadline so one dead host doesn't
// stall the whole matrix build.
func probeRTT(ctx context.Context, host string) (time.Duration, bool) {
	ctx, cancel := context.WithTimeout(ctx, 6*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "ping", "-c", "4", "-W", "1", host)
	out, err := cmd.Output()
	if err != nil {
		return 0, false
	}
	return parseRTTP95(string(out))
}

// parseRTTP95 extracts the worst-case (max over 4 samples ≈ P95) RTT from a
// ping summary line of the form:
//
//	rtt min/avg/max/mdev = 0.123/0.456/0.789/0.012 ms
func parseRTTP95(out string) (time.Duration, bool) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.Contains(line, "rtt min/avg/max/mdev") && !strings.Contains(line, "rtt min/avg/max") {
			continue
		}
		eqIdx := strings.Index(line, "=")
		if eqIdx < 0 {
			return 0, false
		}
		right := strings.TrimSpace(line[eqIdx+1:])
		right = strings.TrimSuffix(right, " ms")
		parts := strings.Split(right, "/")
		if len(parts) < 3 {
			return 0, false
		}
		maxMS, err := strconv.ParseFloat(parts[2], 64)
		if err != nil {
			return 0, false
		}
		return time.Duration(maxMS * float64(time.Millisecond)), true
	}
	return 0, false
}

// inferOverlay classifies the overlay type from the target host's address
// shape. This is a heuristic covering the common AXIS overlays.
func inferOverlay(host string) string {
	switch {
	case strings.HasPrefix(host, "100."):
		return "tailscale"
	case strings.HasPrefix(host, "169.254."):
		return "thunderbolt"
	default:
		return "lan"
	}
}

// TopologySummary returns a human-readable summary of the matrix for logging.
func TopologySummary(m *models.PairwiseLinkMatrix) string {
	if m == nil || len(m.Links) == 0 {
		return "topology: <empty>"
	}
	parts := make([]string, 0, len(m.Links))
	for _, l := range m.Links {
		parts = append(parts, fmt.Sprintf("%s→%s %s %.2fms", l.SourceNode, l.TargetNode, l.OverlayType, float64(l.RTTLatencyP95)/float64(time.Millisecond)))
	}
	return "topology: " + strings.Join(parts, ", ")
}
