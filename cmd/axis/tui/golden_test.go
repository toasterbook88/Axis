package tui

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/buildinfo"
)

// Golden frames pin the whole rendered screen, colors stripped, for the
// layouts an operator sees. Regenerate after an intended change with:
//
//	UPDATE_GOLDEN=true go test ./cmd/axis/tui -run Golden
//
// then review the diff under testdata/. Fixtures use placeholder names only
// (public repo). The release version is normalized so releases do not churn
// the files.

func assertGoldenFrame(t *testing.T, name, frame string) {
	t.Helper()
	got := normalizeFrame(frame)
	path := filepath.Join("testdata", name+".golden")
	if os.Getenv("UPDATE_GOLDEN") == "true" {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v (run with UPDATE_GOLDEN=true to create)", path, err)
	}
	if got != string(want) {
		t.Fatalf("golden mismatch for %s\n--- want\n%s\n--- got\n%s", path, want, got)
	}
}

// normalizeFrame strips color, trailing spaces and the release version.
func normalizeFrame(frame string) string {
	lines := strings.Split(stripANSI(frame), "\n")
	for i, l := range lines {
		l = strings.ReplaceAll(l, "v"+buildinfo.Version, "vX.Y.Z")
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n") + "\n"
}

func TestGoldenDashboard(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []string
	}{
		{"dashboard_overview_120x32", 120, 32, nil},
		{"dashboard_partial_node_120x32", 120, 32, []string{"j"}},
		{"dashboard_models_tab_120x32", 120, 32, []string{"2"}},
		{"dashboard_storage_tab_120x32", 120, 32, []string{"3"}},
		{"dashboard_reservations_tab_120x32", 120, 32, []string{"4"}},
		{"dashboard_help_100x30", 100, 30, []string{"?"}},
		{"dashboard_narrow_60x20", 60, 20, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := press(t, loadedModel(t, tc.w, tc.h), tc.keys...)
			assertGoldenFrame(t, tc.name, m.View())
		})
	}
}

func TestGoldenDashboardASCII(t *testing.T) {
	m := loadedModel(t, 100, 26)
	m.glyphs = asciiGlyphs
	assertGoldenFrame(t, "dashboard_ascii_100x26", m.View())
}

func TestGoldenDashboardRecovery(t *testing.T) {
	m := NewModel()
	next, _ := UpdateWithRefresh(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	next, _ = UpdateWithRefresh(next.(Model), loadErrMsg{Err: errors.New("no daemon cache (connection refused) and no snapshot file (not found)")})
	assertGoldenFrame(t, "dashboard_recovery_100x30", next.(Model).View())
}

func TestGoldenDashboardLoadingASCII(t *testing.T) {
	m := NewModel()
	m.glyphs = asciiGlyphs
	next, _ := UpdateWithRefresh(m, tea.WindowSizeMsg{Width: 80, Height: 20})
	assertGoldenFrame(t, "dashboard_loading_ascii_80x20", next.(Model).View())
}

func TestGoldenWorkspace(t *testing.T) {
	cases := []struct {
		name string
		w, h int
		keys []tea.KeyMsg
	}{
		{"workspace_chat_sidebar_120x30", 120, 30, nil},
		{"workspace_leader_120x30", 120, 30, []tea.KeyMsg{keyCtrlX}},
		{"workspace_palette_120x30", 120, 30, []tea.KeyMsg{keyCtrlO}},
		{"workspace_palette_filtered_120x30", 120, 30, []tea.KeyMsg{keyCtrlO, runes("mod")}},
		{"workspace_help_120x40", 120, 40, []tea.KeyMsg{keyCtrlX, runes("h")}},
		{"workspace_dashboard_120x30", 120, 30, []tea.KeyMsg{keyF2}},
		{"workspace_no_sidebar_90x24", 90, 24, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ws, _ := newTestWorkspace(t, tc.w, tc.h)
			ws = wsKeys(t, ws, tc.keys...)
			assertGoldenFrame(t, tc.name, ws.View())
		})
	}
}
