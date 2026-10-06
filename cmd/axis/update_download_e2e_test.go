package main

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

// E2E harness for the updater download path: the real downloadBytes and
// newUpdateRedirectPolicy run over TLS while request URLs carry the production
// allowlist hostnames (e2eHostTransport maps them to the test server and
// reports the logical request, as a real transport does). Run with -v to emit
// the artifact lines (E2E: ...).

type e2eHostTransport struct {
	dest     string
	base     http.RoundTripper
	failHost string
	failErr  error
}

func (t e2eHostTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.failHost != "" && req.URL.Host == t.failHost {
		return nil, t.failErr
	}
	clone := req.Clone(req.Context())
	clone.URL.Scheme, clone.URL.Host, clone.Host = "https", t.dest, t.dest
	resp, err := t.base.RoundTrip(clone)
	if resp != nil {
		resp.Request = req
	}
	return resp, err
}

func e2eDownload(t *testing.T, failHost string, failErr error, h http.HandlerFunc, start string) ([]byte, error) {
	t.Helper()
	srv := httptest.NewTLSServer(h)
	defer srv.Close()
	c := &http.Client{
		Timeout:       10 * time.Second,
		CheckRedirect: newUpdateRedirectPolicy(),
		Transport:     e2eHostTransport{dest: srv.Listener.Addr().String(), base: srv.Client().Transport, failHost: failHost, failErr: failErr},
	}
	prev := updateGetFunc
	defer func() { updateGetFunc = prev }()
	updateGetFunc = c.Get
	return downloadBytes(start)
}

func TestE2EUpdateDownloadRedactsSignedQueries(t *testing.T) {
	const cdn = "release-assets.githubusercontent.com"
	cases := []struct {
		name     string
		failHost string
		failErr  error
		redirect string
		secret   string
		wantHost string
	}{
		{"rejected redirect", "", nil, "https://evil.example.com/x?token=SECRET-A", "SECRET-A", "evil.example.com"},
		{"transport failure after signed redirect", cdn, errors.New("simulated connection reset"), "https://" + cdn + "/x?token=SECRET-B", "SECRET-B", cdn},
		{"nested url.Error from transport", cdn, &url.Error{Op: "Get", URL: "https://" + cdn + "/x?token=SECRET-N", Err: errors.New("proxy refused")}, "https://" + cdn + "/x?token=SECRET-C", "SECRET-", cdn},
	}
	for _, tc := range cases {
		_, err := e2eDownload(t, tc.failHost, tc.failErr, func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, tc.redirect, http.StatusFound)
		}, "https://github.com/start")
		t.Logf("E2E: %s -> %v", tc.name, err)
		if err == nil {
			t.Fatalf("%s: expected an error", tc.name)
		}
		if strings.Contains(err.Error(), tc.secret) || strings.Contains(err.Error(), "token=") {
			t.Fatalf("%s: error leaks the signed query: %v", tc.name, err)
		}
		if !strings.Contains(err.Error(), tc.wantHost) {
			t.Fatalf("%s: error should still name host %s: %v", tc.name, tc.wantHost, err)
		}
	}
}

func TestE2EUpdateDownloadFollowsExactlyFiveRedirects(t *testing.T) {
	chain := func(w http.ResponseWriter, r *http.Request) { // /chain/<total>/<hop>
		p := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		total, _ := strconv.Atoi(p[1])
		hop, _ := strconv.Atoi(p[2])
		if hop == total {
			fmt.Fprint(w, "done")
			return
		}
		http.Redirect(w, r, fmt.Sprintf("https://github.com/chain/%d/%d", total, hop+1), http.StatusFound)
	}
	for _, total := range []int{maxUpdateRedirects, maxUpdateRedirects + 1} {
		body, err := e2eDownload(t, "", nil, chain, fmt.Sprintf("https://github.com/chain/%d/0", total))
		t.Logf("E2E: %d-redirect chain -> body=%q err=%v", total, body, err)
		if total <= maxUpdateRedirects && (err != nil || string(body) != "done") {
			t.Fatalf("a chain of %d redirects must succeed: body=%q err=%v", total, body, err)
		}
		if total > maxUpdateRedirects && (err == nil || !strings.Contains(err.Error(), fmt.Sprintf("stopped after %d redirects", maxUpdateRedirects))) {
			t.Fatalf("redirect %d must be rejected with the cap: %v", total, err)
		}
	}
}

func TestE2EUpdateDownloadNamesRespondingHost(t *testing.T) {
	_, err := e2eDownload(t, "", nil, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, "https://release-assets.githubusercontent.com/x?token=SECRET-D", http.StatusFound)
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
	}, "https://github.com/start")
	t.Logf("E2E: github.com -> CDN HTTP 500 -> %v", err)
	if err == nil || !strings.Contains(err.Error(), "download from release-assets.githubusercontent.com returned HTTP 500") || strings.Contains(err.Error(), "SECRET-D") {
		t.Fatalf("CDN failure must name the CDN host without the query: %v", err)
	}
}
