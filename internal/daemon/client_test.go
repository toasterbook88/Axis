package daemon

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/models"
)

func TestFetchSnapshotRejectsUnboundOrMixedPublications(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")
	tests := []struct {
		name       string
		metaID     string
		snapshotID string
		want       string
	}{
		{name: "metadata missing id", snapshotID: "pub-1", want: "metadata missing publication_id"},
		{name: "snapshot missing id", metaID: "pub-1", want: "snapshot payload missing publication.id"},
		{name: "refresh raced reads", metaID: "pub-1", snapshotID: "pub-2", want: "snapshot publication changed during read"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/snapshot/meta":
					_ = json.NewEncoder(w).Encode(Metadata{Source: "daemon-cache", Ready: true, PublicationID: tt.metaID})
				case "/snapshot":
					var publication *models.PublicationEnvelope
					if tt.snapshotID != "" {
						publication = &models.PublicationEnvelope{ID: tt.snapshotID}
					}
					_ = json.NewEncoder(w).Encode(models.ClusterSnapshot{Status: models.SnapshotHealthy, Publication: publication})
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			if _, _, err := FetchSnapshot(context.Background(), server.URL); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("FetchSnapshot error = %v, want containing %q", err, tt.want)
			}
		})
	}
}

func TestRunGuardedUsesStreamTransportAndSignsForwardedExecutionOrigin(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	wantOrigin := models.NewExecutionOrigin("relay", "relay.local", "relay-123")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run" || r.URL.Query().Get("stream") != "1" {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer tok" {
			t.Fatalf("Authorization = %q, want Bearer tok", got)
		}
		if got := r.Header.Get("Accept"); got != RunStreamContentType {
			t.Fatalf("Accept = %q, want %q", got, RunStreamContentType)
		}
		gotOrigin, ok, err := auth.ForwardedExecutionOriginFromRequest(r, "tok", time.Now())
		if err != nil {
			t.Fatalf("ForwardedExecutionOriginFromRequest: %v", err)
		}
		if !ok {
			t.Fatal("expected signed forwarded origin headers")
		}
		if gotOrigin != wantOrigin {
			t.Fatalf("forwarded origin = %+v, want %+v", gotOrigin, wantOrigin)
		}

		var req execution.GuardedExecutionRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.Description != "echo ok" || req.Mode != execution.ModeExec || req.Confirm != execution.ConfirmWord {
			t.Fatalf("unexpected request payload: %+v", req)
		}

		w.Header().Set("Content-Type", RunStreamContentType)
		events := []RunStreamEvent{
			{Type: RunStreamEventReady, Result: &execution.GuardedExecutionResult{Node: "alpha", FitScore: 91}},
			{Type: RunStreamEventStateChange, Trigger: execution.StateChangeExecutionReserved, Result: &execution.GuardedExecutionResult{Node: "alpha"}},
			{Type: RunStreamEventStdout, Text: "ok\n"},
			{Type: RunStreamEventResult, Result: &execution.GuardedExecutionResult{
				OK:      true,
				Node:    "alpha",
				Command: "echo ok",
				Output:  "ok\n",
			}},
		}
		enc := json.NewEncoder(w)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				t.Fatalf("encode event: %v", err)
			}
		}
	}))
	defer server.Close()

	resp, err := RunGuarded(context.Background(), server.URL, execution.GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        execution.ModeExec,
		Confirm:     execution.ConfirmWord,
	}, wantOrigin)
	if err != nil {
		t.Fatalf("RunGuarded: %v", err)
	}
	if !resp.OK || resp.Node != "alpha" || resp.Output != "ok\n" {
		t.Fatalf("unexpected response: %+v", resp)
	}
}

func TestRunHTTPClientForAddrDisablesFixedTimeout(t *testing.T) {
	client, _ := runHTTPClientForAddr("127.0.0.1:7777")
	if client.Timeout != 0 {
		t.Fatalf("run http client timeout = %v, want 0", client.Timeout)
	}
}

func TestHttpClientForAddrKeepsShortMetadataTimeout(t *testing.T) {
	client, _ := HttpClientForAddr("127.0.0.1:7777")
	if client.Timeout != daemonRequestTimeout {
		t.Fatalf("metadata http client timeout = %v, want %v", client.Timeout, daemonRequestTimeout)
	}
}

func TestRunGuardedStreamDispatchesReadyAndOutput(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run" || r.URL.Query().Get("stream") != "1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", RunStreamContentType)
		events := []RunStreamEvent{
			{Type: RunStreamEventReady, Result: &execution.GuardedExecutionResult{Node: "alpha", FitScore: 87}},
			{Type: RunStreamEventStateChange, Trigger: execution.StateChangeExecutionReserved, Result: &execution.GuardedExecutionResult{Node: "alpha"}},
			{Type: RunStreamEventStdout, Text: "hello\n"},
			{Type: RunStreamEventStderr, Text: "warn\n"},
			{Type: RunStreamEventStateChange, Trigger: execution.StateChangeExecutionFinished, Result: &execution.GuardedExecutionResult{Node: "alpha", OK: true}},
			{Type: RunStreamEventResult, Result: &execution.GuardedExecutionResult{OK: true, Node: "alpha", Output: "hello\nwarn"}},
		}
		enc := json.NewEncoder(w)
		for _, event := range events {
			if err := enc.Encode(event); err != nil {
				t.Fatalf("encode event: %v", err)
			}
		}
	}))
	defer server.Close()

	var stdout, stderr strings.Builder
	var ready execution.GuardedExecutionResult
	var triggers []string
	resp, err := RunGuardedStream(context.Background(), server.URL, execution.GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        execution.ModeExec,
		Confirm:     execution.ConfirmWord,
		Stdout:      &stdout,
		Stderr:      &stderr,
		OnReady: func(resp execution.GuardedExecutionResult) {
			ready = resp
		},
		OnStateChange: func(_ context.Context, trigger string, _ execution.GuardedExecutionResult) {
			triggers = append(triggers, trigger)
		},
	}, models.ExecutionOrigin{})
	if err != nil {
		t.Fatalf("RunGuardedStream: %v", err)
	}
	if ready.Node != "alpha" || ready.FitScore != 87 {
		t.Fatalf("unexpected ready payload: %+v", ready)
	}
	if stdout.String() != "hello\n" {
		t.Fatalf("stdout = %q, want hello\\n", stdout.String())
	}
	if stderr.String() != "warn\n" {
		t.Fatalf("stderr = %q, want warn\\n", stderr.String())
	}
	if len(triggers) != 2 || triggers[0] != execution.StateChangeExecutionReserved || triggers[1] != execution.StateChangeExecutionFinished {
		t.Fatalf("state-change triggers = %v, want reserved then finished", triggers)
	}
	if !resp.OK || resp.Node != "alpha" {
		t.Fatalf("unexpected final response: %+v", resp)
	}
}

func TestRunGuardedStreamReturnsFinalExecutionError(t *testing.T) {
	t.Setenv(auth.TokenEnvVar, "tok")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/run" || r.URL.Query().Get("stream") != "1" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", RunStreamContentType)
		if err := json.NewEncoder(w).Encode(RunStreamEvent{
			Type: RunStreamEventResult,
			Result: &execution.GuardedExecutionResult{
				OK:          false,
				Description: "echo ok",
				Mode:        execution.ModeExec,
				Error:       "node discovery failed",
			},
		}); err != nil {
			t.Fatalf("encode event: %v", err)
		}
	}))
	defer server.Close()

	resp, err := RunGuardedStream(context.Background(), server.URL, execution.GuardedExecutionRequest{
		Description: "echo ok",
		Mode:        execution.ModeExec,
		Confirm:     execution.ConfirmWord,
	}, models.ExecutionOrigin{})
	if err == nil {
		t.Fatal("expected final stream error")
	}
	if !strings.Contains(err.Error(), "node discovery failed") {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Error != "node discovery failed" {
		t.Fatalf("unexpected final response: %+v", resp)
	}
}
