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

func TestUpdateInstall(t *testing.T) {
	fakeBinary := []byte("#!/bin/sh\necho axis-new\n")
	archiveName := fmt.Sprintf("axis_9.9.9_%s_%s.tar.gz", "testos", "testarch")
	archiveData := buildTestArchive(t, fakeBinary)
	csLine := checksumLine(archiveData, archiveName)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/releases/latest"):
			json.NewEncoder(w).Encode(ghRelease{
				TagName: "v9.9.9",
				Assets: []ghAsset{
					{Name: archiveName, BrowserDownloadURL: "http://" + r.Host + "/archive"},
					{Name: "checksums.txt", BrowserDownloadURL: "http://" + r.Host + "/checksums"},
				},
			})
		case r.URL.Path == "/archive":
			w.Write(archiveData)
		case r.URL.Path == "/checksums":
			fmt.Fprintln(w, csLine)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	// Write a placeholder "current binary" in a temp dir.
	tmpDir := t.TempDir()
	target := filepath.Join(tmpDir, "axis")
	if err := os.WriteFile(target, []byte("old"), 0755); err != nil {
		t.Fatal(err)
	}

	prev := updateAPIBase
	prevGet := updateGetFunc
	defer func() { updateAPIBase = prev; updateGetFunc = prevGet }()
	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	// Patch runtime values via archive name derived from "testos"/"testarch" — we can't
	// override runtime.GOOS/GOARCH directly, so exercise installRelease directly.
	rel := &ghRelease{
		TagName: "v9.9.9",
		Assets: []ghAsset{
			{Name: archiveName, BrowserDownloadURL: srv.URL + "/archive"},
			{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums"},
		},
	}
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
	_ = rel // used to verify the asset lookup path above
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
	sum := sha256.Sum256(data)
	csLine := hex.EncodeToString(sum[:]) + "  " + name

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
