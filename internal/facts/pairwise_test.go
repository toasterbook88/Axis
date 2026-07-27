package facts

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestParseRTTP95(t *testing.T) {
	cases := []struct {
		name string
		in   string
		ok   bool
		want time.Duration
	}{
		{
			name: "linux ping summary",
			in:   "PING 127.0.0.1 (127.0.0.1) 56(84) bytes of data.\n--- 127.0.0.1 ping statistics ---\n4 packets transmitted, 4 received, 0% packet loss, time 3007ms\nrtt min/avg/max/mdev = 0.018/0.024/0.031/0.004 ms\n",
			ok:   true,
			want: 31000, // 0.031ms → 31µs
		},
		{
			name: "no summary line",
			in:   "PING x (1.2.3.4) 56(84) bytes of data.\n",
			ok:   false,
		},
		{
			name: "malformed numbers",
			in:   "rtt min/avg/max/mdev = abc/def/ghi/jkl ms\n",
			ok:   false,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := parseRTTP95(c.in)
			if ok != c.ok {
				t.Fatalf("ok = %v, want %v", ok, c.ok)
			}
			if ok && got != c.want {
				t.Fatalf("got %v, want %v", got, c.want)
			}
		})
	}
}

func TestInferOverlay(t *testing.T) {
	cases := []struct {
		host string
		want string
	}{
		{"100.81.205.4", "tailscale"},
		{"169.254.1.2", "thunderbolt"},
		{"192.168.1.103", "lan"},
		{"10.0.0.5", "lan"},
		{"cranium.local", "lan"},
	}
	for _, c := range cases {
		if got := inferOverlay(c.host); got != c.want {
			t.Errorf("inferOverlay(%q) = %q, want %q", c.host, got, c.want)
		}
	}
}

func TestBuildPairwiseLinkMatrixNoLocalNode(t *testing.T) {
	nodes := []models.NodeFacts{
		{Name: "alpha", SSHTarget: "alpha.local"},
		{Name: "beta", SSHTarget: "beta.local"},
	}
	m := BuildPairwiseLinkMatrix(context.Background(), "nonexistent", nodes)
	if m == nil {
		t.Fatal("expected non-nil matrix")
	}
	if len(m.Links) != 0 {
		t.Fatalf("expected 0 links when local node absent, got %d", len(m.Links))
	}
}

func TestBuildPairwiseLinkMatrixSkipsLocalAndEmptyTargets(t *testing.T) {
	// Local node with no target having a reachable host: skip local self,
	// skip nodes with empty dial targets, attempt the one with a target.
	// We can't guarantee ping works in CI, so just verify the local self is
	// skipped and empty-target nodes are skipped (no panic, no crash).
	nodes := []models.NodeFacts{
		{Name: "local", SSHTarget: "127.0.0.1"},
		{Name: "local-self", SSHTarget: ""}, // empty target, skipped
		{Name: "unreachable", SSHTarget: "192.0.2.1"}, // TEST-NET, will fail ping
	}
	m := BuildPairwiseLinkMatrix(context.Background(), "local", nodes)
	if m == nil {
		t.Fatal("expected non-nil matrix")
	}
	for _, l := range m.Links {
		if l.SourceNode != "local" {
			t.Errorf("link source = %q, want local", l.SourceNode)
		}
		if l.TargetNode == "local" {
			t.Errorf("local self-link should be skipped")
		}
	}
}

func TestTopologySummaryEmpty(t *testing.T) {
	if got := TopologySummary(nil); got != "topology: <empty>" {
		t.Errorf("got %q", got)
	}
	if got := TopologySummary(&models.PairwiseLinkMatrix{}); got != "topology: <empty>" {
		t.Errorf("got %q", got)
	}
}

func TestTopologySummaryNonEmpty(t *testing.T) {
	m := &models.PairwiseLinkMatrix{Links: []models.LinkMetric{
		{SourceNode: "a", TargetNode: "b", OverlayType: "lan", RTTLatencyP95: 500 * time.Microsecond},
	}}
	got := TopologySummary(m)
	if got == "topology: <empty>" {
		t.Fatal("expected non-empty summary")
	}
	if !contains(got, "a→b") {
		t.Errorf("summary missing edge: %q", got)
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || (len(s) > 0 && (s[0:len(sub)] == sub || contains(s[1:], sub))))
}