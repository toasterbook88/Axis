package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
)

// redesignFixture is a four-node snapshot covering every status, link class
// and the optional fields the dashboard renders. Names use placeholders only
// (public repo).
func redesignFixture() *models.ClusterSnapshot {
	battery := 72
	return &models.ClusterSnapshot{
		Timestamp: time.Date(2026, 1, 2, 15, 4, 0, 0, time.UTC),
		Summary: models.ClusterSummary{
			TotalNodes: 4, ReachableNodes: 3,
			TotalRAMMB: 110592, TotalFreeRAMMB: 41000,
			TotalAllocatableMB: 33000, TotalReservedMB: 8192,
		},
		Warnings: []models.Warning{
			{Node: "node-c", Kind: "unreachable", Message: "ssh: connect timed out"},
			{Node: "node-b", Kind: "ram_pressure", Message: "memory pressure medium"},
		},
		Nodes: []models.NodeFacts{
			{
				Name: "node-a", Role: "primary", Status: models.StatusComplete,
				OS: "linux", OSVersion: "6.1.0", Arch: "amd64",
				NetworkClass: models.NetworkClassDirectLAN, SSHHandshakeLatencyMs: 4,
				RAMReservedMB: 8192,
				Resources: &models.Resources{
					CPUCores: 16, CPUModel: "Example CPU", RAMTotalMB: 65536, RAMFreeMB: 30000,
					RAMAllocatableMB: 26000, Load1M: 3.2, Pressure: "none",
					GPUs: []models.GPUInfo{{Vendor: "nvidia", Model: "Example GPU", VRAMMB: 24576}},
					Volumes: []models.Volume{
						{Mount: "/", TotalGB: 1000, FreeGB: 400, Class: "nvme", Role: "root"},
						{Mount: "/data", TotalGB: 4000, FreeGB: 300, Class: "hdd"},
					},
				},
				Ollama: &models.OllamaInfo{Installed: true, Running: true, Version: "0.0.1", Port: 11434,
					Models: []string{"model-a:7b", "model-b:14b"}},
				ResidentModels: []models.ResidentModel{{Name: "model-a:7b", Runtime: "ollama", Port: 11434, SizeVRAMMB: 5200}},
			},
			{
				Name: "node-b", Role: "worker", Status: models.StatusPartial,
				OS: "darwin", OSVersion: "26.4", Arch: "arm64",
				NetworkClass: models.NetworkClassTailscale, SSHHandshakeLatencyMs: 38,
				PartialReasons: []models.PartialReason{{Probe: "gpu", Message: "probe timed out"}},
				Resources: &models.Resources{
					CPUCores: 12, CPUModel: "Example SoC", RAMTotalMB: 36864, RAMFreeMB: 6000,
					RAMAllocatableMB: 3000, Load1M: 9.5, Pressure: "medium",
					BatteryPercent: &battery, PowerSource: "battery",
				},
			},
			{
				Name: "node-c", Role: "worker", Status: models.StatusUnreachable,
				Error: "ssh: connect timed out", NetworkClass: models.NetworkClassRelayed,
			},
			{
				Name: "node-d", Role: "worker", Status: models.StatusComplete, OS: "linux", Arch: "arm64",
				Resources: &models.Resources{CPUCores: 4, RAMTotalMB: 8192, RAMFreeMB: 5000, RAMAllocatableMB: 4000, Load1M: 0.4},
			},
		},
	}
}

// loadedModel drives the model through the real message path: a window size
// then a snapshot load, exactly as the Bubble Tea runtime would.
func loadedModel(t *testing.T, w, h int) Model {
	t.Helper()
	m := NewModel()
	m.now = func() time.Time { return time.Date(2026, 1, 2, 15, 6, 0, 0, time.UTC) }
	next, _ := UpdateWithRefresh(m, tea.WindowSizeMsg{Width: w, Height: h})
	next, _ = UpdateWithRefresh(next.(Model), snapshotLoadedMsg{
		Snapshot: redesignFixture(), Timestamp: "3:04PM", Source: "daemon-cache",
	})
	return next.(Model)
}

func press(t *testing.T, m Model, keys ...string) Model {
	t.Helper()
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEsc}
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "shift+tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "down":
			msg = tea.KeyMsg{Type: tea.KeyDown}
		default:
			msg = keyMsg(k)
		}
		next, _ := UpdateWithRefresh(m, msg)
		m = next.(Model)
	}
	return m
}

// The link column must name the observed network class. A relayed or
// unobserved path must never be badged as LAN (Truth Rule).
func TestLinkLabelNamesObservedNetworkClass(t *testing.T) {
	cases := map[models.NetworkClass]string{
		models.NetworkClassDirectLAN: "lan",
		models.NetworkClassTailscale: "tailnet",
		models.NetworkClassVPN:       "vpn",
		models.NetworkClassRelayed:   "relayed",
		models.NetworkClassUnknown:   "unknown",
		"":                           "not observed",
	}
	for class, want := range cases {
		if got := linkLabel(class); got != want {
			t.Errorf("linkLabel(%q) = %q, want %q", class, got, want)
		}
	}
}

// The Reservations tab must show the reservation overlay the snapshot
// carries, not a hard-coded "no active reservations".
func TestReservationsTabShowsSnapshotReservedRAM(t *testing.T) {
	m := press(t, loadedModel(t, 120, 40), "4")
	out := stripANSI(m.View())
	if strings.Contains(out, "No active reservations") {
		t.Fatalf("reservations tab claims none while node-a has 8 GB reserved:\n%s", out)
	}
	if !strings.Contains(out, "8.0 GB") {
		t.Fatalf("reservations tab missing observed 8.0 GB reserved:\n%s", out)
	}
}

// An unreachable node has no metrics. Its row shows the error rather than
// empty bars that read as an idle machine.
func TestUnreachableRowShowsErrorNotEmptyGauges(t *testing.T) {
	out := stripANSI(loadedModel(t, 120, 40).View())
	var row string
	for line := range strings.SplitSeq(out, "\n") {
		if strings.Contains(line, "node-c") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatalf("node-c row missing:\n%s", out)
	}
	if !strings.Contains(row, "ssh: connect timed out") {
		t.Fatalf("unreachable row hides its error: %q", row)
	}
	if strings.Contains(row, "0%") {
		t.Fatalf("unreachable row shows a fabricated 0%% gauge: %q", row)
	}
}

// Every refresh used to start a new 30s tick chain; pressing r stacked
// them. A tick from a superseded generation must be ignored.
func TestManualRefreshDoesNotStackTickChains(t *testing.T) {
	m := loadedModel(t, 120, 40)
	staleTick := tickMsg{gen: m.refreshGen}

	m = press(t, m, "r")
	next, _ := UpdateWithRefresh(m, snapshotLoadedMsg{Snapshot: redesignFixture(), Source: "daemon-cache"})
	m = next.(Model)

	next, cmd := UpdateWithRefresh(m, staleTick)
	if next.(Model).loading || cmd != nil {
		t.Fatal("stale tick from a superseded refresh started another load")
	}

	next, cmd = UpdateWithRefresh(m, tickMsg{gen: m.refreshGen})
	if !next.(Model).loading || cmd == nil {
		t.Fatal("current-generation tick did not refresh")
	}
}

// Snapshot warnings and partial-probe reasons are observed facts the
// dashboard used to drop.
func TestOverviewSurfacesWarningsAndPartialReasons(t *testing.T) {
	m := press(t, loadedModel(t, 120, 40), "j")
	out := stripANSI(m.View())
	for _, want := range []string{"memory pressure medium", "gpu: probe timed out", "2 warnings"} {
		if !strings.Contains(out, want) {
			t.Errorf("view missing %q:\n%s", want, out)
		}
	}
}

func TestHeaderShowsSnapshotAgeAndHealth(t *testing.T) {
	out := stripANSI(loadedModel(t, 120, 40).View())
	for _, want := range []string{"3/4 reachable", "daemon-cache", "2m ago"} {
		if !strings.Contains(out, want) {
			t.Errorf("header missing %q:\n%s", want, out)
		}
	}
}

func TestHelpOverlayOpensAndCloses(t *testing.T) {
	m := press(t, loadedModel(t, 120, 40), "?")
	if !m.showHelp {
		t.Fatal("? did not open help")
	}
	out := stripANSI(m.View())
	for _, want := range []string{"Keys", "place", "refresh", "advisory"} {
		if !strings.Contains(out, want) {
			t.Errorf("help overlay missing %q:\n%s", want, out)
		}
	}
	if m = press(t, m, "esc"); m.showHelp {
		t.Fatal("esc did not close help")
	}
	if m = press(t, press(t, m, "?"), "?"); m.showHelp {
		t.Fatal("second ? did not close help")
	}
}

func TestTabKeysCycle(t *testing.T) {
	m := loadedModel(t, 120, 40)
	if m = press(t, m, "tab"); m.activeTab != 1 {
		t.Fatalf("tab: activeTab = %d, want 1", m.activeTab)
	}
	if m = press(t, m, "shift+tab", "shift+tab"); m.activeTab != len(inspectorTabs)-1 {
		t.Fatalf("shift+tab wrap: activeTab = %d, want %d", m.activeTab, len(inspectorTabs)-1)
	}
}

func TestHomeEndKeysJump(t *testing.T) {
	m := press(t, loadedModel(t, 120, 40), "G")
	if m.cursor != 3 {
		t.Fatalf("G: cursor = %d, want 3", m.cursor)
	}
	if m = press(t, m, "g"); m.cursor != 0 {
		t.Fatalf("g: cursor = %d, want 0", m.cursor)
	}
}

// The whole frame must fit the terminal exactly at every common size,
// including tiny ones: no wrapped lines, no scroll-off.
func TestViewFitsTerminalAtEverySize(t *testing.T) {
	sizes := [][2]int{{200, 60}, {120, 40}, {100, 30}, {80, 24}, {60, 20}, {40, 12}, {24, 8}}
	for _, sz := range sizes {
		w, h := sz[0], sz[1]
		for _, keys := range [][]string{nil, {"?"}, {"4"}, {"G", "2"}} {
			m := press(t, loadedModel(t, w, h), keys...)
			lines := strings.Split(m.View(), "\n")
			if len(lines) > h {
				t.Errorf("%dx%d keys=%v: %d lines > height", w, h, keys, len(lines))
			}
			for i, line := range lines {
				if lw := lipgloss.Width(line); lw > w {
					t.Errorf("%dx%d keys=%v: line %d width %d > %d: %q", w, h, keys, i, lw, w, stripANSI(line))
				}
			}
		}
	}
}

// Narrow terminals drop low-priority columns instead of wrapping.
func TestNarrowTableDropsLowPriorityColumns(t *testing.T) {
	wide := stripANSI(loadedModel(t, 160, 40).View())
	if !strings.Contains(wide, "GPU") || !strings.Contains(wide, "LINK") {
		t.Fatalf("wide table missing GPU/LINK columns:\n%s", wide)
	}
	narrow := stripANSI(loadedModel(t, 60, 24).View())
	for line := range strings.SplitSeq(narrow, "\n") {
		if strings.Contains(line, "NODE") && (strings.Contains(line, "GPU") || strings.Contains(line, "LINK")) {
			t.Fatalf("60-col table header still has GPU/LINK: %q", line)
		}
	}
	if !strings.Contains(narrow, "node-a") {
		t.Fatalf("60-col table lost node names:\n%s", narrow)
	}
}

// Many nodes on a short terminal: the selected row must stay on screen.
func TestTableScrollsToKeepCursorVisible(t *testing.T) {
	m := NewModel()
	nodes := make([]models.NodeFacts, 40)
	for i := range nodes {
		nodes[i] = models.NodeFacts{Name: "node-" + string(rune('a'+i%26)) + string(rune('a'+i/26)), Status: models.StatusComplete}
	}
	next, _ := UpdateWithRefresh(m, tea.WindowSizeMsg{Width: 100, Height: 30})
	next, _ = UpdateWithRefresh(next.(Model), snapshotLoadedMsg{Snapshot: &models.ClusterSnapshot{Nodes: nodes}, Source: "file"})
	m = press(t, next.(Model), "G")
	out := stripANSI(m.View())
	if !strings.Contains(out, nodes[39].Name) {
		t.Fatalf("last node not visible after G:\n%s", out)
	}
}

func TestASCIIModeUsesNoBoxDrawing(t *testing.T) {
	m := loadedModel(t, 120, 40)
	m.glyphs = asciiGlyphs
	out := m.View()
	for _, r := range out {
		if r > 0x7e && r != 0x1b {
			t.Fatalf("ASCII mode emitted non-ASCII rune %q", r)
		}
	}
}

func TestFormatAge(t *testing.T) {
	now := time.Date(2026, 1, 2, 15, 0, 0, 0, time.UTC)
	cases := []struct {
		at   time.Time
		want string
	}{
		{time.Time{}, "age unknown"},
		{now.Add(-5 * time.Second), "just now"},
		{now.Add(-90 * time.Second), "1m ago"},
		{now.Add(-3 * time.Hour), "3h ago"},
		{now.Add(-50 * time.Hour), "2d ago"},
		{now.Add(10 * time.Second), "just now"},
	}
	for _, tc := range cases {
		if got := formatAge(tc.at, now); got != tc.want {
			t.Errorf("formatAge(%v) = %q, want %q", tc.at, got, tc.want)
		}
	}
}

func TestFooterLegendPerGlyphSet(t *testing.T) {
	m := loadedModel(t, 120, 40)
	if got := stripANSI(renderFooter(m, 120)); !strings.Contains(got, "↑↓ select") {
		t.Fatalf("unicode footer = %q", got)
	}
	m.glyphs = asciiGlyphs
	if got := stripANSI(renderFooter(m, 120)); !strings.Contains(got, "j/k select") || !strings.Contains(got, "h/l tab") {
		t.Fatalf("ascii footer = %q", got)
	}
}

func TestTableHeaderShowsScrollPosition(t *testing.T) {
	m := NewModel()
	nodes := make([]models.NodeFacts, 13)
	for i := range nodes {
		nodes[i] = models.NodeFacts{Name: "node-" + string(rune('a'+i)), Status: models.StatusComplete}
	}
	next, _ := UpdateWithRefresh(m, tea.WindowSizeMsg{Width: 90, Height: 24})
	next, _ = UpdateWithRefresh(next.(Model), snapshotLoadedMsg{Snapshot: &models.ClusterSnapshot{Nodes: nodes}, Source: "file"})
	out := stripANSI(next.(Model).View())
	if !strings.Contains(out, "1-10 of 13") {
		t.Fatalf("scroll position missing:\n%s", out)
	}
}

func TestSummaryKeepsMemoryOnNarrowTerminal(t *testing.T) {
	out := stripANSI(renderSummary(loadedModel(t, 60, 18), 60))
	if !strings.Contains(out, "memory") || !strings.Contains(out, "63%") || !strings.Contains(out, "2 warnings") {
		t.Fatalf("narrow summary = %q", out)
	}
}
