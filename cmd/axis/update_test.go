package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/buildinfo"
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
		json.NewEncoder(w).Encode(ghRelease{TagName: "v" + buildinfo.Version})
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
	if !strings.Contains(out.String(), "Already up to date") {
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
	if !strings.Contains(output, "Current build is newer than the latest published release") {
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

func TestFindAxisBinariesExplicitPath(t *testing.T) {
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "axis")
	if err := os.WriteFile(target, []byte("test"), 0755); err != nil {
		t.Fatal(err)
	}
	paths := findAxisBinaries(target, false)
	if len(paths) != 1 {
		t.Fatalf("expected 1 path, got %d: %v", len(paths), paths)
	}
}

func TestFindAxisBinariesDedup(t *testing.T) {
	// When explicit path is empty and updateAll is false, findAxisBinaries
	// includes the current executable from os.Executable() and should return
	// at least that path without duplicates.
	paths := findAxisBinaries("", false)
	if len(paths) == 0 {
		t.Fatal("expected at least 1 path for self binary")
	}
	// Verify no duplicates.
	seen := make(map[string]bool)
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate path: %s", p)
		}
		seen[p] = true
	}
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
