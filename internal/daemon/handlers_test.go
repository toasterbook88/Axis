package daemon

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestHealthPayloadNilMeta(t *testing.T) {
	p := HealthPayload(nil)
	if p["status"] != "ok" {
		t.Errorf("expected status ok, got %v", p["status"])
	}
	if p["name"] != "axis" {
		t.Errorf("expected name axis, got %v", p["name"])
	}
	if _, ok := p["cache_ready"]; ok {
		t.Error("expected cache_ready absent for nil meta")
	}
}

func TestHealthPayloadWithMeta(t *testing.T) {
	meta := &Metadata{Ready: true, CacheAgeSec: 10, LastError: "some error"}
	p := HealthPayload(meta)
	if p["cache_ready"] != true {
		t.Errorf("expected cache_ready true, got %v", p["cache_ready"])
	}
	if p["cache_age_sec"] != 10 {
		t.Errorf("expected cache_age_sec 10, got %v", p["cache_age_sec"])
	}
	if p["cache_last_error"] != "some error" {
		t.Errorf("expected cache_last_error 'some error', got %v", p["cache_last_error"])
	}
}

func TestHealthPayloadWithMetaNoError(t *testing.T) {
	p := HealthPayload(&Metadata{Ready: false})
	if _, ok := p["cache_last_error"]; ok {
		t.Error("expected cache_last_error absent when LastError is empty")
	}
}

func TestToolDefinitionsReturnsTwoKnownTools(t *testing.T) {
	defs := ToolDefinitions()
	if len(defs) < 2 {
		t.Fatalf("expected at least 2 tool definitions, got %d", len(defs))
	}
	names := make(map[string]bool)
	for _, d := range defs {
		names[d.Name] = true
		if d.Description == "" {
			t.Errorf("tool %q has empty description", d.Name)
		}
		if d.InputSchema == nil {
			t.Errorf("tool %q has nil InputSchema", d.Name)
		}
	}
	if !names["axis_execute"] {
		t.Error("expected axis_execute tool")
	}
	if !names["axis_knowledge"] {
		t.Error("expected axis_knowledge tool")
	}
}

func TestNormalizeAddrPrependsHTTP(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"127.0.0.1:42425", "http://127.0.0.1:42425"},
		{"  127.0.0.1:42425/  ", "http://127.0.0.1:42425"},
		{"http://127.0.0.1:42425", "http://127.0.0.1:42425"},
		{"https://remote:8080", "https://remote:8080"},
	}
	for _, tc := range cases {
		if got := NormalizeAddr(tc.in); got != tc.want {
			t.Errorf("NormalizeAddr(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewWithZeroIntervalUsesDefault(t *testing.T) {
	d := New(0, func(_ context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{}, nil
	})
	if d.interval != defaultRefreshInterval {
		t.Errorf("expected default interval %v, got %v", defaultRefreshInterval, d.interval)
	}
}

func TestNewWithNilCollectorFallsBackToDefault(t *testing.T) {
	d := New(0, nil)
	if d.collector == nil {
		t.Fatal("expected non-nil collector after New with nil")
	}
}

func TestDefaultSnapshotPathContainsDotAxis(t *testing.T) {
	p := DefaultSnapshotPath()
	if !strings.Contains(p, ".axis") {
		t.Errorf("expected .axis in default snapshot path, got %q", p)
	}
	if !strings.HasSuffix(p, "snapshot.json") {
		t.Errorf("expected snapshot.json suffix, got %q", p)
	}
}

func TestCloneSnapshotProducesIndependentCopy(t *testing.T) {
	orig := &models.ClusterSnapshot{
		Status: models.SnapshotHealthy,
		Nodes:  []models.NodeFacts{{Name: "alpha"}},
	}
	clone := CloneSnapshot(orig)
	if clone == orig {
		t.Fatal("expected new pointer")
	}
	clone.Nodes[0].Name = "mutated"
	if orig.Nodes[0].Name != "alpha" {
		t.Error("mutating clone changed original")
	}
}

func TestCloneSnapshotNilReturnsNil(t *testing.T) {
	if got := CloneSnapshot(nil); got != nil {
		t.Fatal("expected nil")
	}
}
