package daemon

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// startMeshWatchTest writes nodes, starts a daemon watching it with a short
// poll interval and returns a writer for new versions and the reports seen.
func startMeshWatchTest(t *testing.T, nodes string) (*Daemon, func(string), func() []string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	path := filepath.Join(os.Getenv("HOME"), ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(nodes), 0600); err != nil {
		t.Fatal(err)
	}
	d := NewDefault(time.Hour)
	d.collector = func(context.Context) (*models.ClusterSnapshot, error) { return &models.ClusterSnapshot{}, nil }
	d.SetSnapshotPath("")
	var mu sync.Mutex
	var reports []string
	prevPoll, prevReporter := watchConfigPollInterval, reportWatchFingerprintError
	watchConfigPollInterval = 10 * time.Millisecond
	reportWatchFingerprintError = func(_, _ string, err error) {
		mu.Lock()
		defer mu.Unlock()
		reports = append(reports, err.Error())
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() {
		cancel()
		d.WaitStopped(context.Background())
		watchConfigPollInterval, reportWatchFingerprintError = prevPoll, prevReporter
	})
	d.WatchConfig(ctx, path)
	write := func(contents string) {
		t.Helper()
		if err := os.WriteFile(path+".next", []byte(contents), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(path+".next", path); err != nil {
			t.Fatal(err)
		}
	}
	seen := func() []string {
		mu.Lock()
		defer mu.Unlock()
		return append([]string(nil), reports...)
	}
	return d, write, seen
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func TestWatchMeshConfigReportsRestartRequiredOncePerVersion(t *testing.T) {
	nodes := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: alpha.example, ssh_user: test}\n"
	d, write, seen := startMeshWatchTest(t, nodes)

	write(nodes + "discovery: {enabled: true, gossip_port: 12345}\n")
	waitFor(t, "first report", func() bool { return len(seen()) >= 1 })
	time.Sleep(10 * watchConfigPollInterval) // at least 6 more ticks of the same bytes
	if got := seen(); len(got) != 1 || !strings.Contains(got[0], "restart required") {
		t.Fatalf("reports after repeated ticks = %q, want exactly 1 restart-required report", got)
	}
	meta := d.Meta()
	if !meta.MeshRestartRequired || !strings.Contains(meta.MeshRestartReason, "restart required") {
		t.Fatalf("meta restart = %v %q, want set", meta.MeshRestartRequired, meta.MeshRestartReason)
	}

	write(nodes + "discovery: {enabled: true, gossip_port: 12346}\n")
	waitFor(t, "second report for a different version", func() bool { return len(seen()) >= 2 })
	time.Sleep(10 * watchConfigPollInterval)
	if got := seen(); len(got) != 2 {
		t.Fatalf("reports = %q, want 2 (one per rejected version)", got)
	}

	write(nodes)
	waitFor(t, "restart flag cleared", func() bool { return !d.Meta().MeshRestartRequired })
	if meta := d.Meta(); meta.MeshRestartReason != "" {
		t.Fatalf("reason after revert = %q, want empty", meta.MeshRestartReason)
	}

	// Returning to an already reported version reports it again.
	write(nodes + "discovery: {enabled: true, gossip_port: 12345}\n")
	waitFor(t, "report after re-entering a rejected version", func() bool { return len(seen()) >= 3 })
}

func TestWatchMeshConfigParseErrorIsNotRestartRequired(t *testing.T) {
	nodes := "nodes:\n  - {name: local, hostname: localhost, ssh_user: test}\n  - {name: alpha, hostname: alpha.example, ssh_user: test}\n"
	d, write, seen := startMeshWatchTest(t, nodes)

	write(nodes + "discovery: {enabled: true, gossip_port: 12345}\n")
	waitFor(t, "restart flag set", func() bool { return d.Meta().MeshRestartRequired })
	write("nodes: [")
	waitFor(t, "parse error report", func() bool { return len(seen()) >= 2 })
	waitFor(t, "restart flag cleared by parse error", func() bool { return !d.Meta().MeshRestartRequired })
	time.Sleep(10 * watchConfigPollInterval)
	if got := seen(); len(got) != 2 {
		t.Fatalf("reports = %q, want 2", got)
	}
}
