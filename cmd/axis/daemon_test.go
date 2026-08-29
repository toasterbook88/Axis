package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/execution"
)

// These stub the HTTP response to exercise decoding and table rendering. The
// route contract itself is covered by TestFetchDaemonMeshAgainstProductionServer,
// which runs api.ServeWithContext -- a stub cannot prove the daemon registers
// the path the CLI requests.
func TestFetchDaemonMeshReturnsPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("expected GET, got %s", r.Method)
		}
		if r.URL.Path != "/v2/mesh" {
			t.Fatalf("expected /v2/mesh, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[{"name":"alpha","hostname":"10.0.0.1","state":"verified","source":"gossip","last_seen":"2026-05-22T22:00:00Z"}],"count":1}`))
	}))
	defer server.Close()

	peers, err := fetchDaemonMesh(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchDaemonMesh: %v", err)
	}
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].Name != "alpha" {
		t.Fatalf("expected peer name alpha, got %q", peers[0].Name)
	}
}

func TestFetchDaemonMeshReturnsEmptyWhenNoPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[],"count":0}`))
	}))
	defer server.Close()

	peers, err := fetchDaemonMesh(context.Background(), server.URL)
	if err != nil {
		t.Fatalf("fetchDaemonMesh: %v", err)
	}
	if len(peers) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(peers))
	}
}

func TestDaemonMeshCommandRendersTable(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v2/mesh" {
			t.Fatalf("expected /v2/mesh, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[{"name":"alpha","hostname":"10.0.0.1","state":"verified","source":"gossip","last_seen":"2026-05-22T22:00:00Z"}],"count":1}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "mesh"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon mesh: %v", err)
	}
	if !strings.Contains(out.String(), "alpha") {
		t.Fatalf("expected peer name alpha in output, got %q", out.String())
	}
	if !strings.Contains(out.String(), "MESH PEERS") {
		t.Fatalf("expected MESH PEERS header, got %q", out.String())
	}
}

func TestDaemonMeshCommandHandlesEmptyPeers(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[],"count":0}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "mesh"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon mesh: %v", err)
	}
	if !strings.Contains(out.String(), "No active mesh peers") {
		t.Fatalf("expected no-peers message, got %q", out.String())
	}
}

func TestDaemonMeshCommandPropagatesWriterFailure(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"peers":[],"count":0}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--cache-addr", server.URL, "mesh"})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestHumanizeTimeFormatsRecent(t *testing.T) {
	now := time.Now()
	cases := []struct {
		t    time.Time
		want string
	}{
		{time.Time{}, "—"},
		{now.Add(-5 * time.Second), "5s ago"},
		{now.Add(-2 * time.Minute), "2m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-48 * time.Hour), "2d ago"},
	}
	for _, tc := range cases {
		got := humanizeTime(tc.t)
		if got != tc.want {
			t.Errorf("humanizeTime(%v) = %q, want %q", tc.t, got, tc.want)
		}
	}
}

func TestInvalidateDaemonCachePostsToEndpoint(t *testing.T) {
	var sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/invalidate" {
			t.Fatalf("expected /invalidate, got %s", r.URL.Path)
		}
		sawPost = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := invalidateDaemonCache(context.Background(), server.URL); err != nil {
		t.Fatalf("invalidateDaemonCache: %v", err)
	}
	if !sawPost {
		t.Fatal("expected invalidate endpoint to be called")
	}
}

func TestRefreshDaemonCachePostsToEndpoint(t *testing.T) {
	var sawPost bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/refresh" {
			t.Fatalf("expected /refresh, got %s", r.URL.Path)
		}
		sawPost = true
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := refreshDaemonCache(context.Background(), server.URL); err != nil {
		t.Fatalf("refreshDaemonCache: %v", err)
	}
	if !sawPost {
		t.Fatal("expected refresh endpoint to be called")
	}
}

func TestDaemonCacheMutationsPropagateWriterFailureAfterPosting(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	for _, action := range []string{"invalidate", "refresh"} {
		t.Run(action, func(t *testing.T) {
			t.Setenv("AXIS_HOME", t.TempDir())
			var sawPost bool
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodPost || r.URL.Path != "/"+action {
					t.Fatalf("request = %s %s, want POST /%s", r.Method, r.URL.Path, action)
				}
				sawPost = true
				w.WriteHeader(http.StatusNoContent)
			}))
			defer server.Close()

			cmd := daemonCmd()
			cmd.SetOut(rejectingOutputWriter{err: wantErr})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--cache-addr", server.URL, action})
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want writer failure", err)
			}
			if !sawPost {
				t.Fatal("cache mutation did not complete before reporting failure")
			}
		})
	}
}

func TestDaemonCommandsHonorCanceledContextBeforeRequestOrRestart(t *testing.T) {
	t.Setenv("AXIS_HOME", t.TempDir())
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		switch r.URL.Path {
		case "/snapshot/meta":
			_, _ = fmt.Fprintf(w, `{"ready":true,"version":%q}`, daemon.Version)
		case "/v2/mesh":
			_, _ = w.Write([]byte(`{"peers":[],"count":0}`))
		default:
			w.WriteHeader(http.StatusNoContent)
		}
	}))
	defer server.Close()

	for _, action := range []string{"status", "mesh", "invalidate", "refresh", "restart"} {
		t.Run(action, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			cmd := daemonCmd()
			cmd.SetContext(ctx)
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs([]string{"--cache-addr", server.URL, action})
			if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
		})
	}
	if requests != 0 {
		t.Fatalf("canceled daemon commands made %d request(s)", requests)
	}
}

func TestRefreshDaemonCacheWithTriggerPostsQueryParam(t *testing.T) {
	var gotTrigger string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected POST, got %s", r.Method)
		}
		if r.URL.Path != "/refresh" {
			t.Fatalf("expected /refresh, got %s", r.URL.Path)
		}
		gotTrigger = r.URL.Query().Get("trigger")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	if err := refreshDaemonCacheWithTrigger(context.Background(), server.URL, execution.StateChangeExecutionFinished); err != nil {
		t.Fatalf("refreshDaemonCacheWithTrigger: %v", err)
	}
	if gotTrigger != execution.StateChangeExecutionFinished {
		t.Fatalf("expected execution trigger query, got %q", gotTrigger)
	}
}

func TestDaemonStatusWarnsWhenVersionMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/snapshot/meta" {
			t.Fatalf("expected /snapshot/meta, got %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"daemon-cache","ready":true,"refresh_interval_sec":60}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if errOut.String() != "" {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	var envelope struct {
		SchemaVersion string          `json:"schema_version"`
		Command       string          `json:"command"`
		OK            bool            `json:"ok"`
		Status        string          `json:"status"`
		Data          daemon.Metadata `json:"data"`
		Warnings      []struct {
			Kind    string `json:"kind"`
			Message string `json:"message"`
		} `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("daemon status is not one JSON envelope: %v\n%s", err, out.String())
	}
	if envelope.SchemaVersion != "axis.output/v1" || envelope.Command != "daemon status" {
		t.Fatalf("unexpected envelope identity: %+v", envelope)
	}
	if envelope.OK || envelope.Status != "incompatible" {
		t.Fatalf("unexpected missing-version state: %+v", envelope)
	}
	if !envelope.Data.Ready || len(envelope.Warnings) != 1 || envelope.Warnings[0].Kind != "daemon_version" || !strings.Contains(envelope.Warnings[0].Message, "missing version information") {
		t.Fatalf("expected structured missing-version warning, got %+v", envelope)
	}
}

func TestDaemonStatusEmitsFreshMachineEnvelope(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"source":"daemon-cache","ready":true,"version":"0.14.10","refresh_interval_sec":60}`))
	}))
	defer server.Close()

	cmd := daemonCmd()
	var out, errOut bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&errOut)
	cmd.SetArgs([]string{"--cache-addr", server.URL, "status"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("daemon status: %v", err)
	}
	if errOut.String() != "" {
		t.Fatalf("unexpected stderr: %q", errOut.String())
	}
	var envelope struct {
		SchemaVersion string          `json:"schema_version"`
		OK            bool            `json:"ok"`
		Status        string          `json:"status"`
		Data          daemon.Metadata `json:"data"`
		Warnings      []any           `json:"warnings"`
	}
	if err := json.Unmarshal(out.Bytes(), &envelope); err != nil {
		t.Fatalf("daemon status is not one JSON envelope: %v\n%s", err, out.String())
	}
	if envelope.SchemaVersion != "axis.output/v1" || !envelope.OK || envelope.Status != "fresh" || envelope.Data.Version != "0.14.10" || len(envelope.Warnings) != 0 {
		t.Fatalf("unexpected fresh envelope: %+v", envelope)
	}
}

func TestDaemonListenAddrNormalizesHostPort(t *testing.T) {
	got, err := daemonListenAddr("127.0.0.1:42425")
	if err != nil {
		t.Fatalf("daemonListenAddr: %v", err)
	}
	if got != "127.0.0.1:42425" {
		t.Fatalf("expected normalized host:port, got %q", got)
	}
}

func TestDaemonListenAddrAcceptsHTTPURL(t *testing.T) {
	got, err := daemonListenAddr("http://127.0.0.1:42425")
	if err != nil {
		t.Fatalf("daemonListenAddr: %v", err)
	}
	if got != "127.0.0.1:42425" {
		t.Fatalf("expected URL host:port, got %q", got)
	}
}

func TestPidAlive(t *testing.T) {
	if !pidAlive(os.Getpid()) {
		t.Error("pidAlive(os.Getpid()) = false, want true")
	}
	if pidAlive(999999) {
		t.Error("pidAlive(999999) = true, want false")
	}
}
