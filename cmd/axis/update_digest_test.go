package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// mappedHostTransport routes every request to the TLS test server (trusting
// its self-signed cert via the server's own transport) while preserving the
// original allowlisted URL for redirect validation.
type mappedHostTransport struct {
	destAddr string
	base     http.RoundTripper
}

func (t mappedHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	clone := req.Clone(req.Context())
	if clone.URL != nil {
		clone.URL.Scheme = "https"
		clone.URL.Host = t.destAddr
	}
	clone.Host = t.destAddr
	resp, err := t.base.RoundTrip(clone)
	if resp != nil {
		// Report the logical (allowlisted) request, as a real transport would.
		resp.Request = req
	}
	return resp, err
}

// mappedHostTLSClient returns a client that hits the TLS test server while the
// request URL carries a real allowlisted hostname, so CheckRedirect sees the
// hosts the updater would see in production.
func mappedHostTLSClient(t *testing.T, srv *httptest.Server) *http.Client {
	t.Helper()
	c := srv.Client()
	c.Timeout = 10 * time.Second
	c.CheckRedirect = newUpdateRedirectPolicy()
	c.Transport = mappedHostTransport{
		destAddr: srv.Listener.Addr().String(),
		base:     srv.Client().Transport,
	}
	return c
}

func TestAllowedUpdateHostsIncludesReleaseAssets(t *testing.T) {
	found := false
	for _, h := range allowedUpdateHosts {
		if h == "release-assets.githubusercontent.com" {
			found = true
		}
	}
	if !found {
		t.Fatalf("allowedUpdateHosts must include release-assets.githubusercontent.com: %v", allowedUpdateHosts)
	}
}

func TestSafeGetFollowsRedirectToReleaseAssetsHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			// Redirect to the allowlisted CDN host; mappedHostTransport routes it
			// back to this server.
			http.Redirect(w, r, "https://release-assets.githubusercontent.com/final", http.StatusFound)
			return
		}
		fmt.Fprint(w, "asset-bytes")
	}))
	defer srv.Close()

	c := mappedHostTLSClient(t, srv)
	resp, err := c.Get("https://github.com/start")
	if err != nil {
		t.Fatalf("expected redirect to allowlisted host to succeed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	if got := resp.Request.URL.Host; got != "release-assets.githubusercontent.com" {
		t.Fatalf("redirect was not followed to the CDN host: final host = %q", got)
	}
	buf := new(bytes.Buffer)
	buf.ReadFrom(resp.Body)
	if got := buf.String(); got != "asset-bytes" {
		t.Fatalf("body = %q", got)
	}
}

func TestSafeGetRejectsRedirectToUnlistedHost(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://evil.example.com/steal", http.StatusFound)
	}))
	defer srv.Close()

	c := mappedHostTLSClient(t, srv)
	_, err := c.Get("https://github.com/toasterbook88/axis/releases/download/v0.19.4/x.txt")
	if err == nil {
		t.Fatal("expected redirect to unlisted host to be rejected")
	}
	if !strings.Contains(err.Error(), "evil.example.com") || !strings.Contains(err.Error(), "not an allowed GitHub domain") {
		t.Fatalf("error should name the rejected host, got %v", err)
	}
}

func TestSafeGetRejectsRedirectToHTTP(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://release-assets.githubusercontent.com/downgrade", http.StatusFound)
	}))
	defer srv.Close()

	c := mappedHostTLSClient(t, srv)
	_, err := c.Get("https://github.com/toasterbook88/axis/releases/download/v0.19.4/x.txt")
	if err == nil {
		t.Fatal("expected redirect to plain HTTP to be rejected")
	}
	if !strings.Contains(err.Error(), "HTTPS") {
		t.Fatalf("error should mention HTTPS-only policy, got %v", err)
	}
}

func TestSafeGetCapsRedirectCount(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Redirect back to an allowlisted host forever (self-loop).
		http.Redirect(w, r, "https://github.com/loop", http.StatusFound)
	}))
	defer srv.Close()

	c := mappedHostTLSClient(t, srv)
	_, err := c.Get("https://github.com/loop")
	if err == nil {
		t.Fatal("expected redirect loop to be capped")
	}
	if !strings.Contains(err.Error(), "stopped after") && !strings.Contains(err.Error(), "redirect") {
		t.Fatalf("error should mention redirect cap, got %v", err)
	}
}

// downloadReleaseBinaryViaServer drives downloadReleaseBinary against a
// configurable release payload served from a plain HTTP test server
// (updateGetFunc is swapped, so the HTTPS allowlist is not in play here).
func downloadReleaseBinaryViaServer(t *testing.T, version string, rel *ghRelease, archive []byte, status int) (string, error) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if status != http.StatusOK {
			w.WriteHeader(status)
			return
		}
		if strings.Contains(r.URL.Path, "/asset/") {
			_, _ = w.Write(archive)
			return
		}
		if strings.Contains(r.URL.Path, "checksums") {
			// Serve the entry only for the actual archive.
			fmt.Fprintln(w, checksumLine(archive, fmt.Sprintf("axis_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	// Rewrite asset URLs to the actual server (they were built with a
	// port-less placeholder).
	for i := range rel.Assets {
		if u, err := url.Parse(rel.Assets[i].BrowserDownloadURL); err == nil && u.Path != "" {
			rel.Assets[i].BrowserDownloadURL = srv.URL + u.Path
		}
	}

	prevBase, prevGet := updateAPIBase, updateGetFunc
	defer func() { updateAPIBase = prevBase; updateGetFunc = prevGet }()
	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	out := new(strings.Builder)
	errOut := new(strings.Builder)
	cmd.SetOut(out)
	cmd.SetErr(errOut)
	_, err := downloadReleaseBinary(cmd, rel, version)
	outStr := out.String()
	if err != nil && !strings.Contains(err.Error(), "checksum") && !strings.Contains(err.Error(), "digest") {
		err = fmt.Errorf("%w (out=%s)", err, outStr)
	}
	return outStr, err
}

func goodRelease(version string, archive []byte, withChecksums, withDigest, wrongDigest bool) *ghRelease {
	name := fmt.Sprintf("axis_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	sum := sha256.Sum256(archive)
	digest := "sha256:" + hex.EncodeToString(sum[:])
	if wrongDigest {
		digest = "sha256:" + strings.Repeat("ab", 32)
	}
	rel := &ghRelease{TagName: "v" + version}
	rel.Assets = append(rel.Assets, ghAsset{Name: name, BrowserDownloadURL: "http://127.0.0.1/asset/" + name, Digest: digest})
	if withChecksums {
		rel.Assets = append(rel.Assets, ghAsset{Name: "checksums.txt", BrowserDownloadURL: "http://127.0.0.1/checksums.txt"})
	}
	_ = withDigest
	return rel
}

func TestDownloadReleaseBinaryVerifiesAPIDigest(t *testing.T) {
	archive := buildTestArchive(t, []byte("good"))
	// Digest-only release (no checksums.txt) with a matching sha256 digest.
	_, err := downloadReleaseBinaryViaServer(t, "1.0.0", goodRelease("1.0.0", archive, false, true, false), archive, http.StatusOK)
	if err != nil {
		t.Fatalf("expected API digest verification to succeed, got %v", err)
	}
}

func TestDownloadReleaseBinaryRejectsAPIDigestMismatch(t *testing.T) {
	archive := buildTestArchive(t, []byte("good"))
	_, err := downloadReleaseBinaryViaServer(t, "1.0.0", goodRelease("1.0.0", archive, false, true, true), archive, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected API digest mismatch error, got %v", err)
	}
}

func TestDownloadReleaseBinaryRejectsWhenDigestAndChecksumsDisagree(t *testing.T) {
	archive := buildTestArchive(t, []byte("good"))
	// checksums.txt has the correct entry but the API digest is wrong.
	_, err := downloadReleaseBinaryViaServer(t, "1.0.0", goodRelease("1.0.0", archive, true, true, true), archive, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected digest/checksums disagreement to abort, got %v", err)
	}
}

func TestDownloadReleaseBinaryFailsClosedWithoutAnyChecksum(t *testing.T) {
	archive := buildTestArchive(t, []byte("good"))
	rel := &ghRelease{TagName: "v1.0.0"}
	name := fmt.Sprintf("axis_1.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	rel.Assets = append(rel.Assets, ghAsset{Name: name, BrowserDownloadURL: "http://127.0.0.1/asset/" + name})
	_, err := downloadReleaseBinaryViaServer(t, "1.0.0", rel, archive, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "no checksum available") {
		t.Fatalf("expected fail-closed error without checksums.txt or digest, got %v", err)
	}
}

func TestDownloadReleaseBinaryIgnoresNonSHA256DigestButRequiresChecksumsTxt(t *testing.T) {
	archive := buildTestArchive(t, []byte("good"))
	rel := &ghRelease{TagName: "v1.0.0"}
	name := fmt.Sprintf("axis_1.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	rel.Assets = append(rel.Assets, ghAsset{Name: name, BrowserDownloadURL: "http://127.0.0.1/asset/" + name, Digest: "md5:abcdef"})
	_, err := downloadReleaseBinaryViaServer(t, "1.0.0", rel, archive, http.StatusOK)
	if err == nil || !strings.Contains(err.Error(), "no checksum available") {
		t.Fatalf("non-sha256 digest alone must not count as verification, got %v", err)
	}
}

func TestDownloadReleaseBinaryMissingChecksumsEntryFailsClosed(t *testing.T) {
	archive := buildTestArchive(t, []byte("good"))
	// checksums.txt exists but has no entry for the archive name (served name differs).
	rel := &ghRelease{TagName: "v1.0.0"}
	name := fmt.Sprintf("axis_1.0.0_%s_%s.tar.gz", runtime.GOOS, runtime.GOARCH)
	rel.Assets = append(rel.Assets,
		ghAsset{Name: name, BrowserDownloadURL: "http://127.0.0.1/asset/" + name},
		ghAsset{Name: "checksums.txt", BrowserDownloadURL: "http://127.0.0.1/checksums.txt"},
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "checksums") {
			fmt.Fprintln(w, checksumLine(archive, "some_other_file.tar.gz"))
			return
		}
		_, _ = w.Write(archive)
	}))
	defer srv.Close()

	for i := range rel.Assets {
		if u, err := url.Parse(rel.Assets[i].BrowserDownloadURL); err == nil && u.Path != "" {
			rel.Assets[i].BrowserDownloadURL = srv.URL + u.Path
		}
	}

	prevBase, prevGet := updateAPIBase, updateGetFunc
	defer func() { updateAPIBase = prevBase; updateGetFunc = prevGet }()
	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	cmd.SetOut(new(strings.Builder))
	cmd.SetErr(new(strings.Builder))
	if _, err := downloadReleaseBinary(cmd, rel, "1.0.0"); err == nil || !strings.Contains(err.Error(), "no checksum entry") {
		t.Fatalf("missing checksums.txt entry must abort, got %v", err)
	}
}

func TestDownloadBytesErrorNamesHostNotQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	prevGet := updateGetFunc
	defer func() { updateGetFunc = prevGet }()
	updateGetFunc = srv.Client().Get

	target := srv.URL + "/asset/x.tar.gz?token=SECRET-QUERY"
	_, err := downloadBytes(target)
	if err == nil {
		t.Fatal("expected HTTP 500 error")
	}
	host, _ := url.Parse(srv.URL)
	if !strings.Contains(err.Error(), fmt.Sprintf("download from %s returned HTTP 500", host.Host)) {
		t.Fatalf("error must name the host, not the signed query, got %v", err)
	}
	if strings.Contains(err.Error(), "SECRET-QUERY") {
		t.Fatalf("error must not leak the query string, got %v", err)
	}
}

func TestInstallReleaseDoesNotReplaceOnChecksumFailure(t *testing.T) {
	tmp := t.TempDir()
	target := filepath.Join(tmp, "axis")
	if err := os.WriteFile(target, []byte("OLD"), 0o755); err != nil {
		t.Fatal(err)
	}

	prevInspect := inspectBinary
	defer func() { inspectBinary = prevInspect }()
	inspectBinary = func(path string) (installInfo, error) {
		abs := mustAbs(path)
		return installInfo{Path: abs, Resolved: abs, IsAxis: true, Version: "0.1.0"}, nil
	}

	archive := buildTestArchive(t, []byte("NEW-BINARY-CONTENT"))
	version := "1.0.0"
	name := fmt.Sprintf("axis_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.Contains(r.URL.Path, "checksums"):
			// checksums.txt exists but lists a wrong hash → verification fails.
			fmt.Fprintln(w, strings.Repeat("ab", 32)+"  "+name)
		default:
			_, _ = w.Write(archive)
		}
	}))
	defer srv.Close()

	prevBase, prevGet := updateAPIBase, updateGetFunc
	defer func() { updateAPIBase = prevBase; updateGetFunc = prevGet }()
	updateAPIBase = srv.URL
	updateGetFunc = srv.Client().Get

	cmd := updateCmd()
	out, errOut := new(strings.Builder), new(strings.Builder)
	cmd.SetOut(out)
	cmd.SetErr(errOut)

	rel := &ghRelease{TagName: "v" + version}
	rel.Assets = append(rel.Assets,
		ghAsset{Name: name, BrowserDownloadURL: srv.URL + "/asset/" + name},
		ghAsset{Name: "checksums.txt", BrowserDownloadURL: srv.URL + "/checksums.txt"},
	)

	if err := installRelease(cmd, rel, version, []string{target}, "", modePath, errOut, out); err == nil {
		t.Fatal("expected installRelease to fail on checksum mismatch")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "OLD" {
		t.Fatalf("target must be unchanged when checksum fails, got %q", got)
	}
}
