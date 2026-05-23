package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/state"
)

func stubObservationsState(t *testing.T, st *state.ClusterState, err error) func() {
	t.Helper()
	prev := loadObservationsState
	loadObservationsState = func() (*state.ClusterState, error) {
		return st, err
	}
	return func() {
		loadObservationsState = prev
	}
}

func TestObservationsCmdNoObservations(t *testing.T) {
	st := &state.ClusterState{Observations: map[string]models.ExecutionObservation{}}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "No observations tracked") {
		t.Fatalf("expected 'No observations tracked' in stdout, got %q", stdout)
	}
}

func TestObservationsListCmdTextOutput(t *testing.T) {
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{
			"abc123": {
				Scope: models.ObservationScope{
					Node:     "alpha",
					Workload: "inference",
					Backend:  "ollama",
					Tool:     "llama3",
				},
				ObservedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				SampleCount: 3,
				WallTimeMS:  1200,
				PeakRAMMB:   512,
				PeakVRAMMB:  256,
				LastSuccess: true,
			},
		},
	}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsListCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Fatalf("expected 'alpha' in stdout, got %q", stdout)
	}
	if !strings.Contains(stdout, "512 MB") {
		t.Fatalf("expected '512 MB' in stdout, got %q", stdout)
	}
	if !strings.Contains(stdout, "256 MB") {
		t.Fatalf("expected '256 MB' in stdout, got %q", stdout)
	}
}

func TestObservationsListCmdJSONOutput(t *testing.T) {
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{
			"abc123": {
				Scope: models.ObservationScope{
					Node:     "alpha",
					Workload: "inference",
					Backend:  "ollama",
					Tool:     "llama3",
				},
				ObservedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				SampleCount: 3,
				WallTimeMS:  1200,
				PeakRAMMB:   512,
				LastSuccess: true,
			},
		},
	}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsListCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	var entries []models.ExecutionObservation
	if err := json.Unmarshal([]byte(stdout), &entries); err != nil {
		t.Fatalf("unmarshal JSON: %v\noutput: %q", err, stdout)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Scope.Node != "alpha" {
		t.Fatalf("expected node alpha, got %q", entries[0].Scope.Node)
	}
}

func TestObservationsInspectCmdTextOutput(t *testing.T) {
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{
			"abc123def456": {
				Scope: models.ObservationScope{
					Node:     "alpha",
					Workload: "inference",
					Backend:  "ollama",
					Tool:     "llama3",
				},
				ObservedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				SampleCount: 3,
				WallTimeMS:  1200,
				PeakRAMMB:   512,
				LastSuccess: true,
			},
		},
	}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsInspectCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"abc123def456"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Fatalf("expected 'alpha' in stdout, got %q", stdout)
	}
	if !strings.Contains(stdout, "512 MB") {
		t.Fatalf("expected '512 MB' in stdout, got %q", stdout)
	}
}

func TestObservationsInspectCmdJSONOutput(t *testing.T) {
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{
			"abc123def456": {
				Scope: models.ObservationScope{
					Node:     "alpha",
					Workload: "inference",
					Backend:  "ollama",
					Tool:     "llama3",
				},
				ObservedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				SampleCount: 3,
				WallTimeMS:  1200,
				PeakRAMMB:   512,
				LastSuccess: true,
			},
		},
	}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsInspectCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"abc123def456", "--format", "json"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	var obs models.ExecutionObservation
	if err := json.Unmarshal([]byte(stdout), &obs); err != nil {
		t.Fatalf("unmarshal JSON: %v\noutput: %q", err, stdout)
	}
	if obs.Scope.Node != "alpha" {
		t.Fatalf("expected node alpha, got %q", obs.Scope.Node)
	}
}

func TestObservationsInspectCmdNotFound(t *testing.T) {
	st := &state.ClusterState{Observations: map[string]models.ExecutionObservation{}}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsInspectCmd()
	_, _, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"missing"})
		return cmd.Execute()
	})
	if err == nil {
		t.Fatal("expected error for missing observation")
	}
	code := ExitCode(err)
	if code != ExitErrGeneric {
		t.Fatalf("expected exit code %d, got %d", ExitErrGeneric, code)
	}
}

func TestObservationsInspectCmdPrefixMatch(t *testing.T) {
	st := &state.ClusterState{
		Observations: map[string]models.ExecutionObservation{
			"abc123def456": {
				Scope: models.ObservationScope{
					Node:     "alpha",
					Workload: "inference",
					Backend:  "ollama",
					Tool:     "llama3",
				},
				ObservedAt:  time.Date(2025, 1, 1, 0, 0, 0, 0, time.UTC),
				SampleCount: 3,
				WallTimeMS:  1200,
				PeakRAMMB:   512,
				LastSuccess: true,
			},
		},
	}
	restore := stubObservationsState(t, st, nil)
	defer restore()

	cmd := observationsInspectCmd()
	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd.SetArgs([]string{"abc123"})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("unexpected stderr: %q", stderr)
	}
	if !strings.Contains(stdout, "alpha") {
		t.Fatalf("expected 'alpha' in stdout, got %q", stdout)
	}
}

func TestRenderObservationTable(t *testing.T) {
	entries := []models.ExecutionObservation{
		{
			Scope: models.ObservationScope{
				Node:     "alpha",
				Workload: "inference",
				Backend:  "ollama",
				Tool:     "llama3",
			},
			WallTimeMS:  1200,
			PeakRAMMB:   512,
			PeakVRAMMB:  256,
			SampleCount: 3,
			LastSuccess: true,
		},
		{
			Scope: models.ObservationScope{
				Node:     "beta",
				Workload: "script",
				Backend:  "local",
				Tool:     "git",
			},
			WallTimeMS:  500,
			PeakRAMMB:   128,
			SampleCount: 1,
			LastSuccess: false,
		},
	}
	out := renderObservationTable(entries)
	if !strings.Contains(out, "alpha") {
		t.Fatalf("expected 'alpha' in output, got %q", out)
	}
	if !strings.Contains(out, "beta") {
		t.Fatalf("expected 'beta' in output, got %q", out)
	}
	if !strings.Contains(out, "512") {
		t.Fatalf("expected '512' in output, got %q", out)
	}
	if !strings.Contains(out, "256 MB") {
		t.Fatalf("expected '256 MB' in output, got %q", out)
	}
	if !strings.Contains(out, "last failed") {
		t.Fatalf("expected 'last failed' in output, got %q", out)
	}
}

func TestRenderObservationTableEmpty(t *testing.T) {
	out := renderObservationTable([]models.ExecutionObservation{})
	if !strings.Contains(out, "No observations tracked") {
		t.Fatalf("expected 'No observations tracked', got %q", out)
	}
}

func TestRenderObservationTableTruncation(t *testing.T) {
	entries := make([]models.ExecutionObservation, 55)
	for i := range entries {
		entries[i] = models.ExecutionObservation{
			Scope: models.ObservationScope{
				Node: fmt.Sprintf("node-%02d", i),
			},
		}
	}
	out := renderObservationTable(entries)
	if !strings.Contains(out, "and 5 more observations") {
		t.Fatalf("expected truncation message, got %q", out)
	}
}
