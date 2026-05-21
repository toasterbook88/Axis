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
