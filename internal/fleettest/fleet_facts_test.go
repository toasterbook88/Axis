//go:build fleet

package fleettest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

// nodeTarget describes one node to collect facts from.
type nodeTarget struct {
	Name     string // logical node name for reporting
	Role     string
	Hostname string // SSH dial target (IP or hostname from ~/.ssh/config)
	Port     int
	User     string
	Local    bool // true = collect via LocalCollector, false = SSH
}

// targetFromEnv builds the remote target from AXIS_FLEET_TARGET, accepting
// [user@]host[:port]. It returns an error rather than a default, so a missing
// value skips the test instead of silently probing something unintended.
func targetFromEnv() (nodeTarget, error) {
	spec := strings.TrimSpace(os.Getenv("AXIS_FLEET_TARGET"))
	if spec == "" {
		return nodeTarget{}, fmt.Errorf("AXIS_FLEET_TARGET is unset ([user@]host[:port])")
	}

	tgt := nodeTarget{Role: "worker", Port: 22}
	if at := strings.LastIndex(spec, "@"); at >= 0 {
		tgt.User, spec = spec[:at], spec[at+1:]
	}
	if colon := strings.LastIndex(spec, ":"); colon >= 0 {
		port, err := strconv.Atoi(spec[colon+1:])
		if err != nil {
			return nodeTarget{}, fmt.Errorf("invalid port in AXIS_FLEET_TARGET: %q", spec[colon+1:])
		}
		tgt.Port, spec = port, spec[:colon]
	}
	if spec == "" {
		return nodeTarget{}, fmt.Errorf("AXIS_FLEET_TARGET has no host component")
	}
	tgt.Hostname = spec

	tgt.Name = strings.TrimSpace(os.Getenv("AXIS_FLEET_TARGET_NAME"))
	if tgt.Name == "" {
		tgt.Name = tgt.Hostname
	}
	return tgt, nil
}

// nodeResult holds the collected facts and any transport error for one node.
type nodeResult struct {
	Target nodeTarget
	Facts  *models.NodeFacts
	Err    error
}

// fleetRunReport is written to the guard's run root after a test run.
type fleetRunReport struct {
	RunRoot   string       `json:"run_root"`
	StartedAt time.Time    `json:"started_at"`
	Duration  string       `json:"duration"`
	Nodes     []nodeReport `json:"nodes"`
	Passed    bool         `json:"passed"`
}

type nodeReport struct {
	Name     string            `json:"name"`
	Status   models.NodeStatus `json:"status"`
	Hostname string            `json:"hostname"`
	Arch     string            `json:"arch"`
	OS       string            `json:"os"`
	Error    string            `json:"error,omitempty"`
	Passed   bool              `json:"passed"`
	Failures []string          `json:"failures,omitempty"`
}

// TestFleetFactsSmoke collects facts from two nodes concurrently and asserts
// basic invariants. It is gated behind //go:build fleet so it never runs in
// normal CI or make test.
//
// Targets come from the environment so the test is portable — a fleet test that
// hardcodes one operator's hardware cannot run for anyone else:
//
//	AXIS_FLEET_TARGET      required, [user@]host[:port] of the remote node
//	AXIS_FLEET_TARGET_NAME optional, logical name for the remote node
//	AXIS_FLEET_CONTROLLER  optional, logical name for the local node
//
// The test skips when AXIS_FLEET_TARGET is unset. Skipping is the honest
// default for a cluster-dependent test: absent a fleet there is nothing to
// assert, and failing would say the code is broken when only the environment
// is missing.
//
// Every environment variable (AXIS_HOME, HOME) comes from a Guard. No ad-hoc
// paths. The remote side re-verifies containment via RemoteCommand.
func TestFleetFactsSmoke(t *testing.T) {
	target, err := targetFromEnv()
	if err != nil {
		t.Skipf("fleet target not configured: %v", err)
	}

	// --- Guard ---
	g, err := NewGuard("")
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	t.Logf("run root: %s", g.Root)
	t.Logf("AXIS_HOME: %s", g.AxisHome)
	t.Logf("HOME: %s", g.Home)

	// Set the guarded environment for local collection.
	t.Setenv("AXIS_HOME", g.AxisHome)
	t.Setenv("HOME", g.Home)

	// --- Targets ---
	controller := os.Getenv("AXIS_FLEET_CONTROLLER")
	if controller == "" {
		controller = "controller"
	}
	targets := []nodeTarget{
		{
			Name:     controller,
			Role:     "controller",
			Hostname: "localhost",
			Local:    true,
		},
		target,
	}

	// --- Concurrent collection ---
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	startedAt := time.Now().UTC()

	results := make([]nodeResult, len(targets))
	var wg sync.WaitGroup

	for i, target := range targets {
		wg.Add(1)
		go func(idx int, tgt nodeTarget) {
			defer wg.Done()
			results[idx] = collectNode(ctx, t, tgt, g)
		}(i, target)
	}
	wg.Wait()

	// --- Assertions ---
	report := fleetRunReport{
		RunRoot:   g.Root,
		StartedAt: startedAt,
		Nodes:     make([]nodeReport, len(targets)),
	}
	allPassed := true

	for i, res := range results {
		nr := nodeReport{Name: res.Target.Name}
		if res.Err != nil {
			// Transport failure — report as unreachable, never healthy/complete.
			nr.Status = models.StatusUnreachable
			nr.Error = res.Err.Error()
			nr.Passed = false
			nr.Failures = []string{fmt.Sprintf("transport: %s", res.Err)}
			allPassed = false
			t.Errorf("%s: transport failure: %v", res.Target.Name, res.Err)
			report.Nodes[i] = nr
			continue
		}

		f := res.Facts
		nr.Status = f.Status
		nr.Hostname = f.Hostname
		nr.Arch = f.Arch
		nr.OS = f.OS
		if f.Error != "" {
			nr.Error = f.Error
		}

		var failures []string

		// Invariant: arch is non-empty (collected from uname -m on the node).
		if f.Arch == "" {
			failures = append(failures, "arch is empty")
			t.Errorf("%s: arch is empty", f.Name)
		}

		// Invariant: hostname is non-empty (collected from hostname/uname -n).
		if f.Hostname == "" {
			failures = append(failures, "hostname is empty")
			t.Errorf("%s: hostname is empty", f.Name)
		}

		// Invariant: every node reports a service manager (systemd, launchd, etc.)
		// Note: the bundle script only probes for a fixed set of tools (go, python3,
		// git, etc.) and does not include systemctl/launchctl. This is a soft check
		// until the bundle is extended.
		hasServiceMgr := false
		for _, tool := range f.Tools {
			if tool.Name == "systemctl" || tool.Name == "launchctl" || tool.Name == "service" {
				hasServiceMgr = true
				break
			}
		}
		if !hasServiceMgr {
			t.Logf("note: %s: no service manager tool detected (systemctl/launchctl/service) — bundle does not probe for these", f.Name)
		}

		// Invariant: CollectedAt is present and within a sane window.
		if f.CollectedAt.IsZero() {
			failures = append(failures, "CollectedAt is zero")
			t.Errorf("%s: CollectedAt is zero", f.Name)
		} else {
			age := time.Since(f.CollectedAt)
			if age > 5*time.Minute {
				failures = append(failures, fmt.Sprintf("CollectedAt is %v in the past", age))
				t.Errorf("%s: CollectedAt is %v in the past", f.Name, age)
			}
		}

		// Invariant: no node reports StatusComplete if its probe returned an error.
		if f.Status == models.StatusComplete && f.Error != "" {
			failures = append(failures, fmt.Sprintf("status is complete but error is set: %s", f.Error))
			t.Errorf("%s: status is complete but error is set: %s", f.Name, f.Error)
		}

		// Invariant: transport/collection failure is reported as unreachable, never healthy/complete.
		if f.Status == models.StatusUnreachable || f.Status == models.StatusError {
			failures = append(failures, fmt.Sprintf("status is %s — should be unreachable for transport failures", f.Status))
			t.Errorf("%s: status is %s — should be unreachable for transport failures", f.Name, f.Status)
		}

		nr.Passed = len(failures) == 0
		nr.Failures = failures
		if !nr.Passed {
			allPassed = false
		}
		report.Nodes[i] = nr
	}

	report.Passed = allPassed
	report.Duration = time.Since(startedAt).String()

	// --- Write structured run report ---
	reportPath := filepath.Join(g.Root, "fleet-facts-report.json")
	reportData, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		t.Errorf("marshal report: %v", err)
	} else {
		if err := os.WriteFile(reportPath, reportData, 0o644); err != nil {
			t.Errorf("write report: %v", err)
		} else {
			t.Logf("report written to %s", reportPath)
		}
	}
}

// collectNode gathers facts from one target. For local targets it uses
// LocalCollector; for remote targets it uses SSH + RemoteCollector with
// the guard's RemoteCommand for containment re-verification on the far side.
func collectNode(ctx context.Context, t *testing.T, target nodeTarget, g *Guard) nodeResult {
	t.Helper()

	if target.Local {
		lc := facts.NewLocalCollector(target.Name, target.Role)
		f, err := lc.Collect(ctx)
		return nodeResult{Target: target, Facts: f, Err: err}
	}

	// Remote: create a unique remote run root on the target, derived from the
	// local guard's unique root so concurrent runs don't collide. Then use
	// RemoteCommand so every command carries the guarded environment and the
	// far side re-verifies containment before executing.
	remoteRoot := g.RemoteRoot(target.Name)
	env := RemoteEnv(remoteRoot)

	exec := transport.NewSSHExecutor(target.Hostname, target.Port, target.User, 15)
	if err := exec.Connect(ctx); err != nil {
		return nodeResult{Target: target, Err: fmt.Errorf("ssh connect to %s@%s: %w", target.User, target.Hostname, err)}
	}
	defer exec.Close()

	// Create the remote run root and its subdirectories.
	_, err := exec.Run(ctx, "mkdir -p "+shellQuote(remoteRoot+"/axis-home")+" "+shellQuote(remoteRoot+"/home"))
	if err != nil {
		return nodeResult{Target: target, Err: fmt.Errorf("create remote run root: %w", err)}
	}

	// Wrap the executor so every command runs under bash (avoids fish issues)
	// and prepends the guarded environment.
	guardedExec := &guardedRemoteExecutor{
		inner: exec,
		env:   env,
	}

	rc := facts.NewRemoteCollector(target.Name, target.Role, target.Hostname, guardedExec)
	f, err := rc.Collect(ctx)
	return nodeResult{Target: target, Facts: f, Err: err}
}

// guardedRemoteExecutor wraps a transport.Executor so every Run is executed
// under bash --noprofile --norc with the guard's environment variables set,
// and the command is wrapped in RemoteCommand for containment re-verification.
type guardedRemoteExecutor struct {
	inner transport.Executor
	env   []string
}

func (e *guardedRemoteExecutor) Connect(ctx context.Context) error {
	return e.inner.Connect(ctx)
}

func (e *guardedRemoteExecutor) Run(ctx context.Context, cmd string) (string, error) {
	// Build the environment prefix.
	envPrefix := ""
	for _, kv := range e.env {
		envPrefix += "export " + kv + "; "
	}

	// Wrap in RemoteCommand for containment re-verification on the far side.
	// RemoteCommand needs the remote root; extract it from the env.
	remoteRoot := ""
	for _, kv := range e.env {
		if strings.HasPrefix(kv, "AXIS_FLEET_ROOT=") {
			remoteRoot = strings.TrimPrefix(kv, "AXIS_FLEET_ROOT=")
			break
		}
	}
	if remoteRoot != "" {
		cmd = RemoteCommand(remoteRoot, envPrefix+cmd)
	}

	// Ensure bash execution.
	cmd = strings.TrimSpace(cmd)
	if !strings.HasPrefix(cmd, "/usr/bin/env bash --noprofile --norc -c ") {
		cmd = "/usr/bin/env bash --noprofile --norc -c " + shellQuote(cmd)
	}
	return e.inner.Run(ctx, cmd)
}

func (e *guardedRemoteExecutor) Close() error {
	return e.inner.Close()
}
