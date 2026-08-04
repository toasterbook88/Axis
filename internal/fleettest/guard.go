// Package fleettest provides the safety boundary for AXIS fleet tests.
//
// Fleet tests run real AXIS binaries against real nodes, so they can write to
// whatever AXIS_HOME resolves to — including an operator's live ~/.axis. This
// package exists so that cannot happen: a fleet test obtains a Guard, and the
// Guard is the only sanctioned source of the environment those tests run under.
//
// The rule it enforces:
//
//	A test run may only write beneath its own generated run directory,
//	locally and remotely.
//
// This is deliberately NOT a production helper. Ordinary AXIS commands must
// keep working against a real operator store; a guard wired into the normal
// resolution path would either reject legitimate stores or be trivially skipped
// by any code that forgot to call it. Nothing under cmd/ imports this package,
// so it never enters the shipped binary.
//
// Enforcement is programmatic rather than conventional on purpose. The Makefile
// already documents that AXIS_HOME outranks HOME, and that comment did not stop
// a suite-wide AXIS_HOME from being pointed at a live store. A check that
// refuses to construct the environment does.
package fleettest

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ContainmentError reports an AXIS_HOME that is not safely inside a run root.
type ContainmentError struct {
	Root     string
	AxisHome string
	Reason   string
}

func (e *ContainmentError) Error() string {
	return fmt.Sprintf("fleettest: refusing AXIS_HOME %q under run root %q: %s",
		e.AxisHome, e.Root, e.Reason)
}

// Guard is a validated fleet-test environment. Every path it exposes is
// physical (symlinks already resolved) and strictly inside Root.
type Guard struct {
	// Root is the run directory. Nothing outside it may be written.
	Root string
	// AxisHome is the AXIS store for this run, strictly beneath Root.
	AxisHome string
	// Home is the HOME for this run, strictly beneath Root. Set because
	// persist resolution falls back to HOME whenever AXIS_HOME is empty, so
	// isolating only AXIS_HOME still leaves a path to the real store.
	Home string
}

// NewGuard creates a unique run root beneath baseDir and derives a contained
// AXIS_HOME and HOME inside it.
//
// The run directory is intentionally left in place. Retention and cleanup are a
// separate concern: a harness that deletes on eight machines is a worse hazard
// than an accumulation of run directories.
func NewGuard(baseDir string) (*Guard, error) {
	if baseDir == "" {
		baseDir = os.TempDir()
	}
	root, err := os.MkdirTemp(baseDir, "axis-fleet-run-")
	if err != nil {
		return nil, fmt.Errorf("fleettest: create run root: %w", err)
	}
	// Resolve immediately: on macOS os.TempDir() is itself a symlink
	// (/var -> /private/var), so an unresolved root would fail its own
	// containment check.
	rootReal, err := resolvePhysical(root)
	if err != nil {
		return nil, fmt.Errorf("fleettest: resolve run root: %w", err)
	}

	axisHome := filepath.Join(rootReal, "axis-home")
	home := filepath.Join(rootReal, "home")
	for _, d := range []string{axisHome, home} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return nil, fmt.Errorf("fleettest: create %s: %w", d, err)
		}
	}

	g := &Guard{Root: rootReal, AxisHome: axisHome, Home: home}
	if err := Validate(g.Root, g.AxisHome); err != nil {
		return nil, err
	}
	if err := Validate(g.Root, g.Home); err != nil {
		return nil, err
	}
	return g, nil
}

// Validate reports whether axisHome is a safe, contained store for run root.
//
// Exported so a harness can check an externally supplied value before using it,
// and so the adversarial cases are directly testable.
func Validate(root, axisHome string) error {
	fail := func(reason string) error {
		return &ContainmentError{Root: root, AxisHome: axisHome, Reason: reason}
	}

	if strings.TrimSpace(axisHome) == "" {
		return fail("empty value")
	}
	if strings.TrimSpace(root) == "" {
		return fail("empty run root")
	}
	if hasDotDot(axisHome) || hasDotDot(root) {
		return fail("path contains a '..' component")
	}
	if !filepath.IsAbs(axisHome) || !filepath.IsAbs(root) {
		return fail("path is not absolute")
	}

	// Physical resolution is what defeats a symlink escape: a link inside the
	// run root pointing at the operator store resolves outside the root and is
	// rejected by the descendant test below.
	rootReal, err := resolvePhysical(root)
	if err != nil {
		return fail("run root could not be resolved to a physical path")
	}
	axisReal, err := resolvePhysical(axisHome)
	if err != nil {
		return fail("could not be resolved to a physical path")
	}

	if axisReal == rootReal {
		return fail("must be strictly beneath the run root, not the root itself")
	}
	// Compare with a trailing separator so a sibling whose name merely starts
	// with the root's name ("/tmp/run-x" vs "/tmp/run-xyz") is not accepted.
	if !strings.HasPrefix(axisReal, rootReal+string(os.PathSeparator)) {
		return fail("resolves outside the run root (" + axisReal + ")")
	}
	return nil
}

// resolvePhysical resolves symlinks for a path that may not fully exist yet.
// It walks up to the deepest existing ancestor, resolves that, and re-attaches
// the remainder. A component that exists but cannot be resolved is an error
// rather than a guess: unknown must never read as safe.
func resolvePhysical(p string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(p); err == nil {
		return resolved, nil
	}
	cur := p
	var rest []string
	for {
		// A component that exists as a symlink while the whole path failed to
		// resolve is a dangling or cyclic link. Walking past it and re-attaching
		// its name would silently treat an unresolvable link as an ordinary
		// not-yet-created directory — the leaf-versus-whole-path mistake, in the
		// opposite direction.
		if fi, err := os.Lstat(cur); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return "", fmt.Errorf("component %q is a symlink that cannot be resolved", cur)
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return "", fmt.Errorf("no resolvable ancestor for %q", p)
		}
		rest = append([]string{filepath.Base(cur)}, rest...)
		cur = parent
		if resolved, err := filepath.EvalSymlinks(cur); err == nil {
			return filepath.Join(append([]string{resolved}, rest...)...), nil
		}
		if _, err := os.Lstat(cur); err == nil {
			// Exists but unresolvable — refuse rather than assume.
			return "", fmt.Errorf("component %q exists but cannot be resolved", cur)
		}
	}
}

func hasDotDot(p string) bool {
	for _, part := range strings.Split(filepath.ToSlash(p), "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

// Env returns the environment assignments a guarded local process must receive.
func (g *Guard) Env() []string {
	return []string{
		"AXIS_HOME=" + g.AxisHome,
		"HOME=" + g.Home,
		"AXIS_FLEET_ROOT=" + g.Root,
	}
}

// RemoteRoot returns the remote run root path for a target node, derived from
// the local guard's unique root so concurrent runs don't collide on the same
// remote directory. The target name disambiguates multiple nodes in one run.
func (g *Guard) RemoteRoot(targetName string) string {
	return filepath.Join("/tmp", filepath.Base(g.Root)+"-"+targetName)
}

// RemoteEnv returns the assignments for a remote run root on another node.
// Remote paths differ from local ones, so the caller supplies the root that
// was created on that node.
func RemoteEnv(remoteRoot string) []string {
	return []string{
		"AXIS_HOME=" + remoteRoot + "/axis-home",
		"HOME=" + remoteRoot + "/home",
		"AXIS_FLEET_ROOT=" + remoteRoot,
	}
}

// RemoteCommand wraps cmd so the remote side exports the guarded environment
// and re-verifies containment before running anything.
//
// The remote check is not redundant with the local one. The controller cannot
// see the remote filesystem, so only the remote shell can confirm that its
// AXIS_HOME is physically inside its run root. If it cannot confirm that, the
// command refuses rather than proceeding under an unverified environment.
func RemoteCommand(remoteRoot, cmd string) string {
	return strings.Join([]string{
		`set -eu`,
		`AXIS_FLEET_ROOT=` + shellQuote(remoteRoot),
		`AXIS_HOME="$AXIS_FLEET_ROOT/axis-home"`,
		`HOME="$AXIS_FLEET_ROOT/home"`,
		`export AXIS_FLEET_ROOT AXIS_HOME HOME`,
		`mkdir -p "$AXIS_HOME" "$HOME"`,
		// Physical resolution on the remote side, using only POSIX builtins so
		// this works on macOS and NixOS without extra tooling.
		`__r=$(cd -P "$AXIS_FLEET_ROOT" 2>/dev/null && pwd -P) || {`,
		`  echo "fleet-guard: run root unresolvable on remote" >&2; exit 78; }`,
		`__a=$(cd -P "$AXIS_HOME" 2>/dev/null && pwd -P) || {`,
		`  echo "fleet-guard: AXIS_HOME unresolvable on remote" >&2; exit 78; }`,
		`case "$__a" in`,
		`  "$__r"/?*) ;;`,
		`  *) echo "fleet-guard: AXIS_HOME $__a escapes run root $__r" >&2; exit 78 ;;`,
		`esac`,
		cmd,
	}, "\n")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
