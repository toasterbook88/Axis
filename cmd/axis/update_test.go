package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
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

func TestExtractBinaryNotFound(t *testing.T) {
	archive := buildTestArchive(t, []byte("data"))
	_, err := extractBinary(archive, "notexist")
	if err == nil {
		t.Fatal("expected error for missing binary")
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

func TestParseLdflagVersion(t *testing.T) {
	got := parseLdflagVersion("github.com/toasterbook88/axis/internal/buildinfo.Version=0.14.4")
	if got != "0.14.4" {
		t.Fatalf("got %q", got)
	}
	if parseLdflagVersion("github.com/other/mod.Version=1.0.0") != "" {
		t.Fatal("expected reject non-axis module")
	}
}
