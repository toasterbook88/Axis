package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	axisbuildinfo "github.com/toasterbook88/axis/internal/buildinfo"
)

// buildTestArchive creates a minimal .tar.gz containing a fake axis binary.
func buildTestArchive(t *testing.T, content []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name: "axis",
		Mode: 0755,
		Size: int64(len(content)),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(content); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// checksumLine returns the sha256 hex line for data/name as in checksums.txt.
func checksumLine(data []byte, name string) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]) + "  " + name
}

func TestUpdateAlreadyUpToDate(t *testing.T) {
	// Use --check so the command reports status without attempting any
	// binary downloads or PATH mutations, keeping the test hermetic.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ghRelease{TagName: "v" + axisbuildinfo.Version})
	}))
	defer srv.Close()

	prev := updateAPIBase
	prevGet := updateGetFunc
	defer func() { updateAPIBase = prev; updateGetFunc = prevGet }()

	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "already up to date") {
		t.Errorf("expected up-to-date message, got: %s", out.String())
	}
}

func TestUpdateCheckFlag(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ghRelease{TagName: "v99.0.0"})
	}))
	defer srv.Close()

	prev := updateAPIBase
	prevGet := updateGetFunc
	defer func() { updateAPIBase = prev; updateGetFunc = prevGet }()

	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "v99.0.0") {
		t.Errorf("expected latest version in output, got: %s", output)
	}
	if !strings.Contains(output, "axis update") {
		t.Errorf("expected install hint in output, got: %s", output)
	}
}

func TestUpdateCheckReportsStaleShadows(t *testing.T) {
	// Running binary matches latest, but a shadow is older — --check must not
	// claim the whole machine is current without mentioning the shadow.
	home := t.TempDir()
	t.Setenv("HOME", home)
	goBin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(goBin, axisBinaryName())
	if err := os.WriteFile(shadow, []byte("placeholder"), 0o755); err != nil {
		t.Fatal(err)
	}

	prevInspect := inspectBinary
	defer func() { inspectBinary = prevInspect }()
	inspectBinary = func(path string) (installInfo, error) {
		abs, _ := filepath.Abs(path)
		shadowAbs, _ := filepath.Abs(shadow)
		if abs == shadowAbs {
			return installInfo{
				Path:    abs,
				IsAxis:  true,
				Version: "0.8.0",
			}, nil
		}
		return prevInspect(path)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ghRelease{TagName: "v" + axisbuildinfo.Version})
	}))
	defer srv.Close()

	prev := updateAPIBase
	prevGet := updateGetFunc
	defer func() { updateAPIBase = prev; updateGetFunc = prevGet }()
	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Shadow (stale)") {
		t.Fatalf("expected stale shadow report, got: %s", output)
	}
	if !strings.Contains(output, "axis update --all") {
		t.Fatalf("expected --all hint, got: %s", output)
	}
}

func TestUpdateCheckCurrentBuildNewerThanLatestPublishedRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ghRelease{TagName: "v0.4.0"})
	}))
	defer srv.Close()

	prev := updateAPIBase
	prevGet := updateGetFunc
	defer func() { updateAPIBase = prev; updateGetFunc = prevGet }()

	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs([]string{"--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "newer than the latest published release") {
		t.Fatalf("expected newer-than-release message, got: %s", output)
	}
	if strings.Contains(output, "Update available") {
		t.Fatalf("expected no update prompt, got: %s", output)
	}
}

func TestUpdateRefusesDowngradeInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(ghRelease{TagName: "v0.4.0"})
	}))
	defer srv.Close()

	prev := updateAPIBase
	prevGet := updateGetFunc
	defer func() { updateAPIBase = prev; updateGetFunc = prevGet }()

	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	var out strings.Builder
	cmd.SetOut(&out)
	cmd.SetArgs(nil)
	if err := cmd.Execute(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	output := out.String()
	if !strings.Contains(output, "Refusing to downgrade the currently running binary.") {
		t.Fatalf("expected downgrade refusal, got: %s", output)
	}
}

func TestUpdateInstall(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho axis-new\n")

	// Write a placeholder "current binary" in a temp dir.
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "axis")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	// Exercise replaceExecutable directly since we can't override runtime.GOOS/GOARCH.
	if err := replaceExecutable(target, fakeBinary); err != nil {
		t.Fatalf("replaceExecutable: %v", err)
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, fakeBinary) {
		t.Errorf("binary not replaced correctly")
	}
}

func TestSelectUpdateTargetsDefaultIsSelfOnly(t *testing.T) {
	self := "/tmp/fake-axis-self"
	got, err := selectUpdateTargets("", false, self)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != self {
		t.Fatalf("default targets = %v, want only self", got)
	}
}

func TestSelectUpdateTargetsAllIncludesShadows(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	goBin := filepath.Join(home, "go", "bin")
	if err := os.MkdirAll(goBin, 0o755); err != nil {
		t.Fatal(err)
	}
	shadow := filepath.Join(goBin, axisBinaryName())
	if err := os.WriteFile(shadow, []byte("x"), 0o755); err != nil {
		t.Fatal(err)
	}
	self := filepath.Join(t.TempDir(), "axis")
	if err := os.WriteFile(self, []byte("self"), 0o755); err != nil {
		t.Fatal(err)
	}
	selfAbs, _ := filepath.Abs(self)

	got, err := selectUpdateTargets("", true, selfAbs)
	if err != nil {
		t.Fatal(err)
	}
	foundSelf, foundShadow := false, false
	shadowAbs, _ := filepath.Abs(shadow)
	for _, p := range got {
		if p == selfAbs {
			foundSelf = true
		}
		if p == shadowAbs {
			foundShadow = true
		}
	}
	if !foundSelf || !foundShadow {
		t.Fatalf("--all targets = %v, want self %q and shadow %q", got, selfAbs, shadowAbs)
	}
}

func TestPackageManagerForPath(t *testing.T) {
	tests := []struct {
		path string
		want string
		ok   bool
	}{
		{"/opt/homebrew/bin/axis", "homebrew", true},
		{"/usr/local/Cellar/axis/0.1/bin/axis", "homebrew", true},
		{"/nix/store/abc-axis/bin/axis", "nix", true},
		{"/home/user/go/bin/axis", "", false},
		{"/usr/local/bin/axis", "", false},
	}
	for _, tt := range tests {
		got, ok := packageManagerForPath(tt.path)
		if ok != tt.ok || got != tt.want {
			t.Errorf("packageManagerForPath(%q) = %q,%v want %q,%v", tt.path, got, ok, tt.want, tt.ok)
		}
	}
}

func TestIsAxisBuildInfo(t *testing.T) {
	if !isAxisBuildInfo(&buildinfo.BuildInfo{Path: "github.com/toasterbook88/axis/cmd/axis"}) {
		t.Fatal("expected main package path match")
	}
	modMatch := &buildinfo.BuildInfo{}
	modMatch.Main.Path = "github.com/toasterbook88/axis"
	if !isAxisBuildInfo(modMatch) {
		t.Fatal("expected exact module path match")
	}
	// Must not accept sibling modules or path-injection lookalikes.
	for _, bad := range []string{
		"github.com/toasterbook88/axis-tools",
		"example.com/github.com/toasterbook88/axis",
		"github.com/toasterbook88/axis/evil",
	} {
		bi := &buildinfo.BuildInfo{Path: bad}
		bi.Main.Path = bad
		if isAxisBuildInfo(bi) {
			t.Fatalf("expected reject %q", bad)
		}
	}
}

// withReleaseServer runs fn with updateAPIBase/updateGetFunc pointed at a
// server that serves a complete latest-release payload for this GOOS/GOARCH.
func withReleaseServer(t *testing.T, version string, binary []byte, fn func()) {
	t.Helper()
	archive := buildTestArchive(t, binary)
	archiveName := fmt.Sprintf("axis_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	cs := checksumLine(archive, archiveName)

	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "/releases/latest"):
			_ = json.NewEncoder(w).Encode(ghRelease{
				TagName: "v" + version,
				Assets: []ghAsset{
					{Name: archiveName, BrowserDownloadURL: srv.URL + "/asset/" + archiveName},
					{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
				},
			})
		case strings.Contains(r.URL.Path, "/asset/"):
			_, _ = w.Write(archive)
		case strings.Contains(r.URL.Path, "checksums"):
			fmt.Fprintln(w, cs)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	prevBase := updateAPIBase
	prevGet := updateGetFunc
	defer func() {
		updateAPIBase = prevBase
		updateGetFunc = prevGet
	}()
	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get
	fn()
}

func mustAbs(p string) string {
	a, err := filepath.Abs(p)
	if err != nil {
		return p
	}
	return a
}

func TestInstallReleaseSkipsNonAxisManagedNewerAndUpdatesStale(t *testing.T) {
	tmp := t.TempDir()
	nonAxis := filepath.Join(tmp, "not-axis")
	managed := filepath.Join(tmp, "managed")
	newer := filepath.Join(tmp, "newer")
	stale := filepath.Join(tmp, "stale")
	for _, p := range []string{nonAxis, managed, newer, stale} {
		if err := os.WriteFile(p, []byte("OLD"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	prevInspect := inspectBinary
	defer func() { inspectBinary = prevInspect }()
	inspectBinary = func(path string) (installInfo, error) {
		abs := mustAbs(path)
		switch abs {
		case mustAbs(nonAxis):
			return installInfo{Path: abs, IsAxis: false, Reason: "not an AXIS binary"}, nil
		case mustAbs(managed):
			return installInfo{Path: abs, Resolved: abs, IsAxis: true, Version: "0.1.0", ManagedBy: "homebrew"}, nil
		case mustAbs(newer):
			return installInfo{Path: abs, Resolved: abs, IsAxis: true, Version: "99.0.0"}, nil
		case mustAbs(stale):
			return installInfo{Path: abs, Resolved: abs, IsAxis: true, Version: "0.1.0"}, nil
		default:
			return installInfo{Path: abs, IsAxis: false, Reason: "unknown"}, nil
		}
	}

	newBytes := []byte("NEW-BINARY-CONTENT")
	withReleaseServer(t, "1.0.0", newBytes, func() {
		cmd := updateCmd()
		var out, errOut strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		rel := &ghRelease{TagName: "v1.0.0"}
		// Assets filled by download via updateGetFunc from server when fetchLatest isn't used —
		// installRelease calls downloadReleaseBinary which uses updateGetFunc on asset URLs
		// from rel.Assets. Seed assets from a probe request.
		resp, err := updateGetFunc(updateAPIBase + "/repos/x/y/releases/latest")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(rel); err != nil {
			t.Fatal(err)
		}

		err = installRelease(cmd, rel, "1.0.0",
			[]string{nonAxis, managed, newer, stale},
			"", modeAll, &errOut, &out)
		if err != nil {
			t.Fatalf("installRelease: %v\nout=%s\nerr=%s", err, out.String(), errOut.String())
		}

		// Only stale should be rewritten.
		for _, p := range []string{nonAxis, managed, newer} {
			got, _ := os.ReadFile(p)
			if string(got) != "OLD" {
				t.Errorf("%s was mutated: %q", p, got)
			}
		}
		got, err := os.ReadFile(stale)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, newBytes) {
			t.Errorf("stale not updated: got %q want %q\nout=%s", got, newBytes, out.String())
		}
		if !strings.Contains(out.String(), "Skipped (managed") {
			t.Errorf("expected managed skip message, out=%s", out.String())
		}
		if !strings.Contains(out.String(), "newer than release") {
			t.Errorf("expected newer skip message, out=%s", out.String())
		}
	})
}

func TestInstallReleaseExplicitPathAllowsUnknownVersion(t *testing.T) {
	// Devin P1: --path with identity-validated AXIS binary but unknown version
	// must not print "Nothing to update."
	tmp := t.TempDir()
	target := filepath.Join(tmp, "axis-dev")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	prevInspect := inspectBinary
	defer func() { inspectBinary = prevInspect }()
	inspectBinary = func(path string) (installInfo, error) {
		abs := mustAbs(path)
		return installInfo{
			Path:     abs,
			Resolved: abs,
			IsAxis:   true,
			Version:  "", // unknown local build
			Reason:   "AXIS binary but version metadata unavailable",
		}, nil
	}

	newBytes := []byte("EXPLICIT-PATH-NEW")
	withReleaseServer(t, "1.2.3", newBytes, func() {
		cmd := updateCmd()
		var out, errOut strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)

		rel := &ghRelease{}
		resp, err := updateGetFunc(updateAPIBase + "/repos/x/y/releases/latest")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if err := json.NewDecoder(resp.Body).Decode(rel); err != nil {
			t.Fatal(err)
		}

		// modeAll would refuse; modePath must accept.
		if err := installRelease(cmd, rel, "1.2.3", []string{target}, "", modeAll, &errOut, &out); err != nil {
			t.Fatalf("modeAll: %v", err)
		}
		if got, _ := os.ReadFile(target); string(got) != "OLD" {
			t.Fatalf("modeAll must not replace unknown-version secondary, got %q", got)
		}
		if !strings.Contains(errOut.String(), "refusing implicit --all") {
			t.Fatalf("expected --all refuse message, err=%s", errOut.String())
		}

		out.Reset()
		errOut.Reset()
		if err := installRelease(cmd, rel, "1.2.3", []string{target}, "", modePath, &errOut, &out); err != nil {
			t.Fatalf("modePath: %v\nout=%s\nerr=%s", err, out.String(), errOut.String())
		}
		got, err := os.ReadFile(target)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, newBytes) {
			t.Fatalf("modePath did not update: got %q want %q\nout=%s", got, newBytes, out.String())
		}
		if strings.Contains(out.String(), "Nothing to update") {
			t.Fatalf("explicit --path must not report nothing to update: %s", out.String())
		}
	})
}

func TestInstallReleaseUpdatesSymlinkTargetNotLink(t *testing.T) {
	tmp := t.TempDir()
	realBin := filepath.Join(tmp, "real-axis")
	linkBin := filepath.Join(tmp, "link-axis")
	if err := os.WriteFile(realBin, []byte("OLD-TARGET"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realBin, linkBin); err != nil {
		t.Fatal(err)
	}

	prevInspect := inspectBinary
	defer func() { inspectBinary = prevInspect }()
	inspectBinary = func(path string) (installInfo, error) {
		abs := mustAbs(path)
		return installInfo{
			Path:      abs,
			Resolved:  mustAbs(realBin),
			IsSymlink: true,
			IsAxis:    true,
			Version:   "0.1.0",
		}, nil
	}

	newBytes := []byte("NEW-VIA-SYMLINK")
	withReleaseServer(t, "2.0.0", newBytes, func() {
		cmd := updateCmd()
		var out, errOut strings.Builder
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)

		rel := &ghRelease{}
		resp, err := updateGetFunc(updateAPIBase + "/repos/x/y/releases/latest")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		_ = json.NewDecoder(resp.Body).Decode(rel)

		if err := installRelease(cmd, rel, "2.0.0", []string{linkBin}, "", modePath, &errOut, &out); err != nil {
			t.Fatalf("installRelease: %v\nout=%s\nerr=%s", err, out.String(), errOut.String())
		}

		// Symlink must remain a symlink.
		fi, err := os.Lstat(linkBin)
		if err != nil {
			t.Fatal(err)
		}
		if fi.Mode()&os.ModeSymlink == 0 {
			t.Fatalf("symlink was destroyed; mode=%v out=%s", fi.Mode(), out.String())
		}
		// Target content updated.
		got, err := os.ReadFile(realBin)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(got, newBytes) {
			t.Fatalf("resolved target not updated: %q", got)
		}
		// Reading through link also sees new content.
		got2, _ := os.ReadFile(linkBin)
		if !bytes.Equal(got2, newBytes) {
			t.Fatalf("link read mismatch: %q", got2)
		}
	})
}

func TestExtractBinary(t *testing.T) {
	content := []byte("fake-binary-data")
	archive := buildTestArchive(t, content)
	got, err := extractBinary(archive, "axis")
	if err != nil {
		t.Fatalf("extractBinary: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("got %q want %q", got, content)
	}
}

func TestExtractBinaryNotFound(t *testing.T) {
	archive := buildTestArchive(t, []byte("data"))
	_, err := extractBinary(archive, "notexist")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}
}

func TestVerifyChecksum(t *testing.T) {
	data := []byte("hello world")
	name := "axis_1.0.0_linux_amd64.tar.gz"
	csLine := checksumLine(data, name)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, csLine)
	}))
	defer srv.Close()

	prevGet := updateGetFunc
	defer func() { updateGetFunc = prevGet }()
	updateGetFunc = srv.Client().Get

	if err := verifyChecksum(data, name, srv.URL+"/checksums.txt"); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestVerifyChecksumMismatch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "deadbeef  axis_1.0.0_linux_amd64.tar.gz")
	}))
	defer srv.Close()

	prevGet := updateGetFunc
	defer func() { updateGetFunc = prevGet }()
	updateGetFunc = srv.Client().Get

	err := verifyChecksum([]byte("real data"), "axis_1.0.0_linux_amd64.tar.gz", srv.URL+"/checksums.txt")
	if err == nil {
		t.Fatal("expected checksum mismatch error")
	}
}

func TestSafeGetRejectsHTTP(t *testing.T) {
	_, err := safeGet("http://github.com/foo")
	if err == nil || !strings.Contains(err.Error(), "only HTTPS") {
		t.Errorf("expected HTTPS-only error, got %v", err)
	}
}

func TestSafeGetRejectsUnknownHost(t *testing.T) {
	_, err := safeGet("https://evil.example.com/axis")
	if err == nil || !strings.Contains(err.Error(), "not an allowed") {
		t.Errorf("expected disallowed host error, got %v", err)
	}
}

func TestCompareReleaseVersions(t *testing.T) {
	tests := []struct {
		name    string
		current string
		latest  string
		want    int
	}{
		{name: "equal", current: "0.7.0", latest: "0.7.0", want: 0},
		{name: "newer available", current: "0.7.0", latest: "0.8.0", want: -1},
		{name: "current newer", current: "0.7.0", latest: "0.4.0", want: 1},
		{name: "trim v prefix", current: "v0.7.0", latest: "0.7.0", want: 0},
		{name: "compare missing patch", current: "0.7", latest: "0.7.1", want: -1},
		{name: "prerelease to final", current: "1.0.0-rc1", latest: "1.0.0", want: -1},
		{name: "final newer than prerelease", current: "1.0.0", latest: "1.0.0-rc1", want: 1},
		{name: "alpha before beta", current: "1.0.0-alpha", latest: "1.0.0-beta", want: -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := compareReleaseVersions(tt.current, tt.latest)
			if err != nil {
				t.Fatalf("compareReleaseVersions returned error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("compareReleaseVersions(%q, %q) = %d, want %d", tt.current, tt.latest, got, tt.want)
			}
		})
	}
}

func TestParseLdflagVersion(t *testing.T) {
	got := parseLdflagVersion("github.com/toasterbook88/axis/internal/buildinfo.Version=0.14.4")
	if got != "0.14.4" {
		t.Fatalf("got %q", got)
	}
	if parseLdflagVersion("github.com/other/mod.Version=1.0.0") != "" {
		t.Fatal("expected reject non-axis module")
	}
}
