package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestCollectStatusSnapshotFallsBackToLiveWhenCacheFails(t *testing.T) {
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	snap, source, err := collectStatusSnapshot(
		context.Background(),
		true,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", context.DeadlineExceeded
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("collectStatusSnapshot: %v", err)
	}
	if snap != liveSnap {
		t.Fatal("expected live snapshot fallback")
	}
	if source != "live-fallback" {
		t.Fatalf("expected live-fallback source, got %q", source)
	}
	if len(snap.Warnings) != 1 {
		t.Fatalf("expected one cache warning, got %#v", snap.Warnings)
	}
	if snap.Warnings[0].Kind != "cache" {
		t.Fatalf("warning kind = %q, want cache", snap.Warnings[0].Kind)
	}
	if got := snap.Warnings[0].Message; got != "using live snapshot (daemon cache unavailable)" {
		t.Fatalf("warning message = %q", got)
	}
}

func TestCollectStatusSnapshotCachedOnlyFailsWhenCacheFails(t *testing.T) {
	snap, source, err := collectStatusSnapshot(
		context.Background(),
		false,
		true,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return nil, "", context.DeadlineExceeded
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			t.Fatal("expected no live fallback in cached-only mode")
			return nil, "", nil
		},
	)
	if err == nil {
		t.Fatal("expected cached-only cache failure")
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot on cached-only failure, got %#v", snap)
	}
	if source != "" {
		t.Fatalf("expected empty source on cached-only failure, got %q", source)
	}
	if got := err.Error(); got != "daemon cache unavailable: context deadline exceeded" {
		t.Fatalf("unexpected cached-only error: %q", got)
	}
}

func TestPrintResidentModelsSectionEmpty(t *testing.T) {
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{Name: "cortex", Status: models.StatusComplete},
	}
	printResidentModelsSection(&buf, nodes)
	if buf.Len() != 0 {
		t.Errorf("expected no output for nodes with no resident models, got %q", buf.String())
	}
}

func TestFormatResidentRuntime(t *testing.T) {
	// Strip ANSI codes by checking the raw string contains the label text.
	cases := []struct{ rt, want string }{
		{"ollama", "ollama"},
		{"llama.cpp", "llama.cpp"},
		{"mlx", "mlx"},
		{"apple-foundation-models", "apple-fm"},
		{"unknown-rt", "unknown-rt"},
	}
	for _, tc := range cases {
		got := formatResidentRuntime(tc.rt)
		if !strings.Contains(got, tc.want) {
			t.Errorf("formatResidentRuntime(%q) = %q, want it to contain %q", tc.rt, got, tc.want)
		}
	}
}

// --- VRAM column tests ---

func TestResidentRowVRAMTotal(t *testing.T) {
	cases := []struct {
		rms  []models.ResidentModel
		want int64
	}{
		{nil, 0},
		{[]models.ResidentModel{{Name: "a", SizeVRAMMB: 0}}, 0},
		{[]models.ResidentModel{{Name: "a", SizeVRAMMB: 1331}}, 1331},
		{[]models.ResidentModel{
			{Name: "a", SizeVRAMMB: 1331},
			{Name: "b", SizeVRAMMB: 2048},
		}, 3379},
	}
	for _, tc := range cases {
		got := residentRowVRAMTotal(tc.rms)
		if got != tc.want {
			t.Errorf("residentRowVRAMTotal(%v) = %d, want %d", tc.rms, got, tc.want)
		}
	}
}
