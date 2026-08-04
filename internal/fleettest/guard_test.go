package fleettest

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewGuardProducesContainedPaths(t *testing.T) {
	g, err := NewGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	for name, p := range map[string]string{"AxisHome": g.AxisHome, "Home": g.Home} {
		if !strings.HasPrefix(p, g.Root+string(os.PathSeparator)) {
			t.Errorf("%s %q is not beneath root %q", name, p, g.Root)
		}
		if fi, err := os.Stat(p); err != nil || !fi.IsDir() {
			t.Errorf("%s %q was not created as a directory: %v", name, p, err)
		}
	}
	if g.AxisHome == g.Root {
		t.Error("AxisHome must be strictly beneath Root, not equal to it")
	}
}

func TestNewGuardRunRootsAreUnique(t *testing.T) {
	base := t.TempDir()
	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		g, err := NewGuard(base)
		if err != nil {
			t.Fatalf("NewGuard: %v", err)
		}
		if seen[g.Root] {
			t.Fatalf("run root reused: %s", g.Root)
		}
		seen[g.Root] = true
	}
}

// TestNewGuardLeavesRunDirectoryInPlace pins the deliberate choice not to
// clean up. A harness that deletes across a fleet is a worse hazard than
// accumulated run directories.
func TestNewGuardLeavesRunDirectoryInPlace(t *testing.T) {
	g, err := NewGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	if _, err := os.Stat(g.Root); err != nil {
		t.Errorf("run root should persist after construction: %v", err)
	}
}

// TestValidateRejectsAdversarialAxisHomes is the reason this package exists.
// Each case is a way a fleet test could end up writing to real operator state.
func TestValidateRejectsAdversarialAxisHomes(t *testing.T) {
	root := t.TempDir()
	rootReal, err := resolvePhysical(root)
	if err != nil {
		t.Fatalf("resolve root: %v", err)
	}

	// A stand-in for the operator's live store. Never written to; only used as
	// a rejection target.
	realStore := filepath.Join(t.TempDir(), ".axis")
	if err := os.MkdirAll(realStore, 0o755); err != nil {
		t.Fatal(err)
	}

	// Symlink escape: a link *inside* the run root pointing at the real store.
	escape := filepath.Join(rootReal, "escape")
	if err := os.Symlink(realStore, escape); err != nil {
		t.Fatal(err)
	}

	// Prefix lookalike: a sibling whose name starts with the root's name.
	lookalike := rootReal + "-sibling"
	if err := os.MkdirAll(filepath.Join(lookalike, "axis-home"), 0o755); err != nil {
		t.Fatal(err)
	}

	// Dangling symlink inside the root.
	dangling := filepath.Join(rootReal, "dangling")
	if err := os.Symlink(filepath.Join(rootReal, "nope"), dangling); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		axisHome string
	}{
		{"empty", ""},
		{"whitespace only", "   "},
		{"relative path", "axis-home"},
		{"the real operator store", realStore},
		// Any AXIS root that happens to exist elsewhere on the filesystem.
		// Constructed rather than hardcoded: the assertion is about being
		// outside the run root, not about one particular machine's layout.
		{"an operator store outside the run root", filepath.Join(t.TempDir(), ".axis")},
		// Raw strings, not filepath.Join: Join normalises ".." away before
		// Validate ever sees it, so a Join-built case would not exercise the
		// rule at all.
		{"dot-dot escape", rootReal + "/../elsewhere"},
		{"dot-dot that lands back inside", rootReal + "/a/../axis-home"},
		{"symlink escaping to the real store", escape},
		{"path beneath a symlink escape", filepath.Join(escape, "nested")},
		{"prefix lookalike sibling", filepath.Join(lookalike, "axis-home")},
		{"the run root itself", rootReal},
		{"dangling symlink", dangling},
		{"parent of the run root", filepath.Dir(rootReal)},
		{"root directory", "/"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := Validate(rootReal, tc.axisHome); err == nil {
				t.Errorf("Validate accepted %q, which escapes or equals the run root", tc.axisHome)
			}
		})
	}
}

func TestValidateAcceptsContainedPaths(t *testing.T) {
	g, err := NewGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	nested := filepath.Join(g.AxisHome, "deeper", "still")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{g.AxisHome, g.Home, nested} {
		if err := Validate(g.Root, p); err != nil {
			t.Errorf("Validate rejected contained path %q: %v", p, err)
		}
	}
	// A path that does not exist yet but is lexically contained is fine: the
	// harness creates it later.
	if err := Validate(g.Root, filepath.Join(g.Root, "not-created-yet")); err != nil {
		t.Errorf("Validate rejected a contained but not-yet-created path: %v", err)
	}
}

// TestEnvOverrideCannotEscape covers the specific incident this guard exists to
// prevent: an AXIS_HOME supplied from the environment pointing at a live store.
func TestEnvOverrideCannotEscape(t *testing.T) {
	g, err := NewGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	realStore := filepath.Join(t.TempDir(), ".axis")
	if err := os.MkdirAll(realStore, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("AXIS_HOME", realStore)

	if err := Validate(g.Root, os.Getenv("AXIS_HOME")); err == nil {
		t.Fatal("an environment-supplied AXIS_HOME outside the run root must be rejected")
	}

	// The guard's own environment must never carry the escaping value.
	for _, kv := range g.Env() {
		if strings.HasPrefix(kv, "AXIS_HOME=") && strings.Contains(kv, realStore) {
			t.Fatalf("guard environment leaked the external store: %q", kv)
		}
	}
}

func TestEnvIsolatesBothAxisHomeAndHome(t *testing.T) {
	g, err := NewGuard(t.TempDir())
	if err != nil {
		t.Fatalf("NewGuard: %v", err)
	}
	env := strings.Join(g.Env(), "\n")
	for _, want := range []string{
		"AXIS_HOME=" + g.AxisHome,
		"HOME=" + g.Home,
		"AXIS_FLEET_ROOT=" + g.Root,
	} {
		if !strings.Contains(env, want) {
			t.Errorf("Env() missing %q; got:\n%s", want, env)
		}
	}
	// HOME matters independently: persist falls back to it when AXIS_HOME is
	// empty, so isolating only AXIS_HOME still leaves a path to the real store.
	if !strings.Contains(env, "HOME="+g.Home) {
		t.Error("Env() must isolate HOME as well as AXIS_HOME")
	}
}

func TestRemoteCommandSelfVerifiesContainment(t *testing.T) {
	script := RemoteCommand("/tmp/axis-fleet-run-remote", "axis facts")
	for _, want := range []string{
		"AXIS_FLEET_ROOT=",
		`AXIS_HOME="$AXIS_FLEET_ROOT/axis-home"`,
		`HOME="$AXIS_FLEET_ROOT/home"`,
		"cd -P", "pwd -P",
		"escapes run root",
		"exit 78",
		"axis facts",
	} {
		if !strings.Contains(script, want) {
			t.Errorf("remote command missing %q:\n%s", want, script)
		}
	}
	if !strings.HasPrefix(script, "set -eu") {
		t.Error("remote command must fail fast")
	}
}

func TestRemoteCommandQuotesRootWithSpaces(t *testing.T) {
	script := RemoteCommand("/tmp/run dir/with 'quote", "true")
	if !strings.Contains(script, `'/tmp/run dir/with '\''quote'`) {
		t.Errorf("run root was not shell-quoted:\n%s", script)
	}
}

func TestRemoteEnvDerivesFromRemoteRoot(t *testing.T) {
	env := strings.Join(RemoteEnv("/srv/run-1"), "\n")
	for _, want := range []string{
		"AXIS_HOME=/srv/run-1/axis-home",
		"HOME=/srv/run-1/home",
		"AXIS_FLEET_ROOT=/srv/run-1",
	} {
		if !strings.Contains(env, want) {
			t.Errorf("RemoteEnv missing %q; got:\n%s", want, env)
		}
	}
}

func TestContainmentErrorNamesBothPaths(t *testing.T) {
	err := Validate("/tmp/root", "/etc/passwd")
	if err == nil {
		t.Fatal("expected rejection")
	}
	msg := err.Error()
	for _, want := range []string{"/tmp/root", "/etc/passwd", "fleettest"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message %q should mention %q", msg, want)
		}
	}
}

// TestRemoteRootDerivationIsUniquePerGuard asserts that the remote run root
// derivation produces distinct paths for different local guards. This is the
// remote counterpart of TestNewGuardRunRootsAreUnique: the local side already
// guarantees unique roots; the remote side must derive from that uniqueness
// so concurrent runs don't collide on the same remote directory.
//
// The derivation is a pure function of g.Root and the target name, so this
// test is hermetic and does not require cluster access.
func TestRemoteRootDerivationIsUniquePerGuard(t *testing.T) {
	base := t.TempDir()
	seen := map[string]bool{}

	for i := 0; i < 5; i++ {
		g, err := NewGuard(base)
		if err != nil {
			t.Fatalf("NewGuard: %v", err)
		}
		remoteRoot := g.RemoteRoot("node-a")
		if seen[remoteRoot] {
			t.Errorf("remote root %q reused for different local guard (iteration %d)", remoteRoot, i)
		}
		seen[remoteRoot] = true
	}
}
