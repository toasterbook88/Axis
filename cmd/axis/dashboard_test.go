package main

import (
	"strings"
	"testing"
)

func TestNodeListItemStatusIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"complete", "●"},
		{"partial", "◐"},
		{"unreachable", "○"},
		{"error", "?"},
		{"unknown", "?"},
	}
	for _, tt := range tests {
		item := NodeListItem{Status: tt.status}
		got := item.StatusIcon()
		if !strings.Contains(got, tt.want) {
			t.Errorf("StatusIcon(%q) = %q, want containing %q", tt.status, got, tt.want)
		}
	}
}

func TestNodeListItemPressureColor(t *testing.T) {
	tests := []struct {
		pressure string
		want     string
	}{
		{"none", "none"},
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		item := NodeListItem{Pressure: tt.pressure}
		got := item.PressureColor()
		if !strings.Contains(got, tt.want) {
			t.Errorf("PressureColor(%q) = %q, want containing %q", tt.pressure, got, tt.want)
		}
	}
}

func TestDoctorCheckIcon(t *testing.T) {
	tests := []struct {
		status string
		want   string
	}{
		{"pass", "✓"},
		{"warn", "!"},
		{"fail", "✗"},
		{"unknown", "?"},
	}
	for _, tt := range tests {
		c := DoctorCheck{Status: tt.status}
		got := c.Icon()
		if !strings.Contains(got, tt.want) {
			t.Errorf("Icon(%q) = %q, want containing %q", tt.status, got, tt.want)
		}
	}
}

func TestRenderNodeTable(t *testing.T) {
	nodes := []NodeListItem{
		{
			Name:     "alpha",
			Status:   "complete",
			Arch:     "arm64",
			RAMTotal: 16384,
			RAMFree:  8192,
			Pressure: "low",
			GPUs:     []string{"Apple M3 Max"},
			IsLocal:  true,
		},
		{
			Name:     "beta",
			Status:   "unreachable",
			Arch:     "amd64",
			RAMTotal: 32768,
			RAMFree:  0,
			Pressure: "high",
			GPUs:     nil,
			IsLocal:  false,
		},
	}
	out := RenderNodeTable(nodes)
	if !strings.Contains(out, "alpha") || !strings.Contains(out, "beta") {
		t.Fatalf("expected node names in table, got %q", out)
	}
	if !strings.Contains(out, "arm64") || !strings.Contains(out, "amd64") {
		t.Fatalf("expected architectures in table, got %q", out)
	}
	if !strings.Contains(out, "Apple M3 Max") {
		t.Fatalf("expected GPU model in table, got %q", out)
	}
	if !strings.Contains(out, "local") {
		t.Fatalf("expected local label in table, got %q", out)
	}
}
