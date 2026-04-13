package main

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestCollectStatusSnapshotPrefersCacheWhenAvailable(t *testing.T) {
	cachedSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 2},
	}
	liveSnap := &models.ClusterSnapshot{
		Summary: models.ClusterSummary{TotalNodes: 1},
	}

	snap, source, err := collectStatusSnapshot(
		context.Background(),
		true,
		false,
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return cachedSnap, "daemon-cache", nil
		},
		func(context.Context) (*models.ClusterSnapshot, string, error) {
			return liveSnap, "live", nil
		},
	)
	if err != nil {
		t.Fatalf("collectStatusSnapshot: %v", err)
	}
	if snap != cachedSnap {
		t.Fatal("expected cached snapshot to be returned")
	}
	if source != "daemon-cache" {
		t.Fatalf("expected daemon-cache source, got %q", source)
	}
	if len(snap.Warnings) != 0 {
		t.Fatalf("expected no warnings on cached hit, got %#v", snap.Warnings)
	}
}

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
	if got := snap.Warnings[0].Message; got != "daemon cache unavailable; fell back to live snapshot: context deadline exceeded" {
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

// --- Resident model display tests ---

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

func TestPrintResidentModelsSectionOllama(t *testing.T) {
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{
			Name:   "cortex",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "llama3:8b", Runtime: "ollama", Source: "ollama-ps"},
				{Name: "qwen3:4b", Runtime: "ollama", Source: "ollama-ps"},
			},
		},
	}
	printResidentModelsSection(&buf, nodes)
	out := buf.String()
	if !strings.Contains(out, "RESIDENT MODELS") {
		t.Errorf("expected RESIDENT MODELS header, got:\n%s", out)
	}
	if !strings.Contains(out, "cortex") {
		t.Errorf("expected node name 'cortex', got:\n%s", out)
	}
	if !strings.Contains(out, "ollama") {
		t.Errorf("expected runtime 'ollama', got:\n%s", out)
	}
	if !strings.Contains(out, "llama3:8b") || !strings.Contains(out, "qwen3:4b") {
		t.Errorf("expected both model names, got:\n%s", out)
	}
}

func TestPrintResidentModelsSectionLlamaCpp(t *testing.T) {
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{
			Name:   "medulla",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "mistral-7b-instruct-q4_K_M", Runtime: "llama.cpp", Source: "llama-server-api"},
			},
		},
	}
	printResidentModelsSection(&buf, nodes)
	out := buf.String()
	if !strings.Contains(out, "llama.cpp") {
		t.Errorf("expected runtime 'llama.cpp', got:\n%s", out)
	}
	if !strings.Contains(out, "mistral-7b-instruct-q4_K_M") {
		t.Errorf("expected model name, got:\n%s", out)
	}
}

func TestPrintResidentModelsSectionMLX(t *testing.T) {
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{
			Name:   "scout",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "mlx-community/Llama-3.2-1B-Instruct-4bit", Runtime: "mlx", Source: "mlx-lm-api"},
			},
		},
	}
	printResidentModelsSection(&buf, nodes)
	out := buf.String()
	if !strings.Contains(out, "mlx") {
		t.Errorf("expected runtime 'mlx', got:\n%s", out)
	}
	if !strings.Contains(out, "mlx-community/Llama-3.2-1B-Instruct-4bit") {
		t.Errorf("expected model name, got:\n%s", out)
	}
}

func TestPrintResidentModelsSectionMultiNodeMultiRuntime(t *testing.T) {
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{
			Name:   "cortex",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "llama3:8b", Runtime: "ollama"},
			},
		},
		{
			Name:   "medulla",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "mistral-7b-q4", Runtime: "llama.cpp"},
			},
		},
		{
			Name:   "scout",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "mlx-community/Llama-3.2-1B-4bit", Runtime: "mlx"},
			},
		},
	}
	printResidentModelsSection(&buf, nodes)
	out := buf.String()
	for _, want := range []string{"cortex", "medulla", "scout", "ollama", "llama.cpp", "mlx"} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output, got:\n%s", want, out)
		}
	}
}

func TestPrintResidentModelsSectionTruncatesLongList(t *testing.T) {
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{
			Name:   "bignode",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "model-a", Runtime: "ollama"},
				{Name: "model-b", Runtime: "ollama"},
				{Name: "model-c", Runtime: "ollama"},
				{Name: "model-d", Runtime: "ollama"},
				{Name: "model-e", Runtime: "ollama"},
			},
		},
	}
	printResidentModelsSection(&buf, nodes)
	out := buf.String()
	if !strings.Contains(out, "+2 more") {
		t.Errorf("expected '+2 more' truncation for 5 models (max 3), got:\n%s", out)
	}
	if strings.Contains(out, "model-d") || strings.Contains(out, "model-e") {
		t.Errorf("expected model-d and model-e to be truncated, got:\n%s", out)
	}
}

func TestTruncateModelList(t *testing.T) {
	cases := []struct {
		names []string
		max   int
		want  string
	}{
		{[]string{"a", "b", "c"}, 3, "a, b, c"},
		{[]string{"a", "b", "c", "d"}, 3, "a, b, c, +1 more"},
		{[]string{"a", "b", "c", "d", "e"}, 3, "a, b, c, +2 more"},
		{[]string{"only"}, 3, "only"},
	}
	for _, tc := range cases {
		got := truncateModelList(tc.names, tc.max)
		if got != tc.want {
			t.Errorf("truncateModelList(%v, %d) = %q, want %q", tc.names, tc.max, got, tc.want)
		}
	}
}

func TestPrintResidentModelsSectionUnknownRuntimesSortedAlphabetically(t *testing.T) {
	// Unknown runtimes must appear in sorted order regardless of map iteration.
	// Run many times to expose any non-determinism from map traversal.
	var buf bytes.Buffer
	nodes := []models.NodeFacts{
		{
			Name:   "node",
			Status: models.StatusComplete,
			ResidentModels: []models.ResidentModel{
				{Name: "z-model", Runtime: "zzz-backend"},
				{Name: "a-model", Runtime: "aaa-backend"},
				{Name: "m-model", Runtime: "mmm-backend"},
			},
		},
	}
	for i := 0; i < 20; i++ {
		buf.Reset()
		printResidentModelsSection(&buf, nodes)
		out := buf.String()
		// aaa must appear before mmm, mmm before zzz
		aIdx := strings.Index(out, "aaa-backend")
		mIdx := strings.Index(out, "mmm-backend")
		zIdx := strings.Index(out, "zzz-backend")
		if aIdx < 0 || mIdx < 0 || zIdx < 0 {
			t.Fatalf("iteration %d: missing runtime label in output:\n%s", i, out)
		}
		if !(aIdx < mIdx && mIdx < zIdx) {
			t.Errorf("iteration %d: expected aaa < mmm < zzz, got positions %d %d %d\n%s", i, aIdx, mIdx, zIdx, out)
		}
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
