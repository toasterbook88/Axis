package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureWritableTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not restrict writes")
	}

	writable := t.TempDir()
	if err := ensureWritableTarget(filepath.Join(writable, "axis")); err != nil {
		t.Errorf("writable dir reported as unwritable: %v", err)
	}

	locked := t.TempDir()
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	if err := ensureWritableTarget(filepath.Join(locked, "axis")); err == nil {
		t.Error("read-only dir reported as writable")
	}
}

// TestEnsureWritableTargetLeavesNoProbeFile guards against the permission probe
// littering the install directory.
func TestEnsureWritableTargetLeavesNoProbeFile(t *testing.T) {
	dir := t.TempDir()
	if err := ensureWritableTarget(filepath.Join(dir, "axis")); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("probe left files behind: %v", names)
	}
}

// TestInstallReleaseFailsFastOnUnwritableTarget is the regression test for the
// original defect: an unwritable /usr/local/bin was only discovered after the
// release had been downloaded and checksummed, so the failure was slow and the
// message did not say what to do about it.
//
// No release server is stood up here. If the writability preflight regresses,
// installRelease reaches downloadReleaseBinary and fails with a transport error
// instead of the elevation hint, which this test detects.
func TestInstallReleaseFailsFastOnUnwritableTarget(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permissions do not restrict writes")
	}

	locked := t.TempDir()
	target := filepath.Join(locked, "axis")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o700) })

	prevInspect := inspectBinary
	defer func() { inspectBinary = prevInspect }()
	inspectBinary = func(path string) (installInfo, error) {
		abs := mustAbs(path)
		return installInfo{Path: abs, Resolved: abs, IsAxis: true, Version: "0.1.0"}, nil
	}

	cmd := updateCmd()
	var out, errOut strings.Builder
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)

	rel := &ghRelease{TagName: "v1.0.0"}
	err := installRelease(cmd, rel, "1.0.0", []string{target}, "", modeAll, &errOut, &out)
	if err == nil {
		t.Fatalf("expected an error for an unwritable target\nout=%s\nerr=%s", out.String(), errOut.String())
	}
	if !strings.Contains(err.Error(), "not writable") && !strings.Contains(err.Error(), "no writable axis install") {
		t.Errorf("error does not name the permission problem: %v", err)
	}
	if !strings.Contains(err.Error(), "sudo") {
		t.Errorf("error does not tell the operator how to recover: %v", err)
	}

	// The original binary must be untouched.
	got, readErr := os.ReadFile(target)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != "OLD" {
		t.Errorf("target was mutated despite the failure: %q", got)
	}
}

func TestInstallCheckFor(t *testing.T) {
	tests := []struct {
		name       string
		self       string
		shadows    []string
		wantStatus string
		wantIn     string
	}{
		{
			name:       "unresolvable self",
			self:       "",
			wantStatus: "warn",
			wantIn:     "could not resolve",
		},
		{
			name:       "single system-wide install",
			self:       filepath.Join(canonicalInstallDir, "axis"),
			wantStatus: "pass",
			wantIn:     "single system-wide install",
		},
		{
			name:       "single user-local install still passes but advises",
			self:       "/home/alice/.local/bin/axis",
			wantStatus: "pass",
			wantIn:     "user-local",
		},
		{
			name:       "duplicate installs warn",
			self:       filepath.Join(canonicalInstallDir, "axis"),
			shadows:    []string{"/home/alice/.local/bin/axis"},
			wantStatus: "warn",
			wantIn:     "2 axis binaries",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := installCheckFor(tc.self, tc.shadows)
			if got.Status != tc.wantStatus {
				t.Errorf("status = %q, want %q (msg=%q)", got.Status, tc.wantStatus, got.Message)
			}
			if !strings.Contains(got.Message, tc.wantIn) {
				t.Errorf("message %q does not contain %q", got.Message, tc.wantIn)
			}
			if got.Name != "AXIS Install" {
				t.Errorf("unexpected check name %q", got.Name)
			}
		})
	}
}
