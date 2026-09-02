package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/daemon"
)

type statusEnvelope struct {
	SchemaVersion string `json:"schema_version"`
	Command       string `json:"command"`
	OK            bool   `json:"ok"`
	Status        string `json:"status"`
}

// A daemon that answers with unhealthy metadata must still emit its complete
// machine-readable envelope, and must then report failure to the caller. The
// previous behavior wrote `"ok":false` and exited 0, so the envelope and the
// exit code contradicted each other.
func TestDaemonStatusUnhealthyWritesPayloadAndExitsFour(t *testing.T) {
	cases := []struct {
		name       string
		metaJSON   string
		wantStatus string
	}{
		{"incompatible", `{"source":"daemon-cache","ready":true}`, "incompatible"},
		{"unavailable", `{"source":"daemon-cache","ready":false,"version":"` + daemonVersionForTest() + `"}`, "unavailable"},
		{"stale", `{"source":"daemon-cache","ready":true,"stale":true,"version":"` + daemonVersionForTest() + `"}`, "stale"},
		{"degraded", `{"source":"daemon-cache","ready":true,"version":"` + daemonVersionForTest() + `","last_error":"refresh failed"}`, "degraded"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.metaJSON))
			}))
			defer server.Close()

			cmd := daemonCmd()
			var out, errOut bytes.Buffer
			cmd.SetOut(&out)
			cmd.SetErr(&errOut)
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})
			err := cmd.Execute()

			// The diagnostic payload must be complete and parseable even though
			// the command fails.
			var env statusEnvelope
			if jsonErr := json.Unmarshal(out.Bytes(), &env); jsonErr != nil {
				t.Fatalf("payload must still be written: %v\n%s", jsonErr, out.String())
			}
			if env.SchemaVersion != "axis.output/v1" || env.Command != "daemon status" {
				t.Fatalf("unexpected envelope identity: %+v", env)
			}
			if env.OK {
				t.Fatalf("expected ok=false for %s, got %+v", tc.wantStatus, env)
			}
			if env.Status != tc.wantStatus {
				t.Fatalf("status = %q, want %q", env.Status, tc.wantStatus)
			}
			if got := ExitCode(err); got != ExitErrCommandFail {
				t.Fatalf("%s daemon status: exit %d, want %d", tc.wantStatus, got, ExitErrCommandFail)
			}
		})
	}
}

// A healthy daemon must still exit 0.
func TestDaemonStatusFreshExitsZero(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"daemon-cache","ready":true,"version":"` + daemonVersionForTest() + `"}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("fresh daemon must exit 0, got %v (exit %d)", err, ExitCode(err))
	}
	var env statusEnvelope
	if err := json.Unmarshal(out.Bytes(), &env); err != nil {
		t.Fatalf("bad envelope: %v", err)
	}
	if !env.OK || env.Status != "fresh" {
		t.Fatalf("expected fresh/ok, got %+v", env)
	}
}

// GUD-002: when the diagnostic itself cannot be emitted, the writer error is
// the primary error, not the health disposition.
func TestDaemonStatusWriterErrorOutranksDisposition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"daemon-cache","ready":false,"version":"` + daemonVersionForTest() + `"}`))
	}))
	defer server.Close()

	writeErr := errors.New("stdout closed")
	cmd := daemonCmd()
	cmd.SetOut(failingWriter{err: writeErr})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})
	err := cmd.Execute()
	if !errors.Is(err, writeErr) {
		t.Fatalf("writer error must be primary, got %v", err)
	}
}

type failingWriter struct{ err error }

func (f failingWriter) Write([]byte) (int, error) { return 0, f.err }

func daemonVersionForTest() string { return daemon.Version }
