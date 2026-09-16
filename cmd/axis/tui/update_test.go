package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/models"
)

func keyMsg(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

func modelWithNodes(n int) Model {
	m := NewModel()
	m.loading = false
	nodes := make([]models.NodeFacts, n)
	for i := range nodes {
		nodes[i] = models.NodeFacts{Name: "node", Role: "worker", Status: models.StatusComplete}
	}
	m.snapshot = &models.ClusterSnapshot{Nodes: nodes}
	return m
}

func TestUpdateQuit(t *testing.T) {
	for _, k := range []string{"q", "ctrl+c"} {
		m := NewModel()
		var key tea.KeyMsg
		if k == "ctrl+c" {
			key = tea.KeyMsg{Type: tea.KeyCtrlC}
		} else {
			key = keyMsg(k)
		}
		next, cmd := UpdateWithRefresh(m, key)
		if cmd == nil {
			t.Fatalf("key %q: expected tea.Quit command, got nil", k)
		}
		if !next.(Model).quitting {
			t.Fatalf("key %q: expected quitting=true", k)
		}
	}
}

func TestUpdateCursorNavigation(t *testing.T) {
	m := modelWithNodes(3)

	// j / down move down, clamped at the last node.
	for i := 1; i <= 4; i++ {
		next, _ := UpdateWithRefresh(m, keyMsg("j"))
		m = next.(Model)
		want := i
		if want > 2 {
			want = 2
		}
		if m.cursor != want {
			t.Fatalf("after %d j presses: cursor = %d, want %d", i, m.cursor, want)
		}
	}

	// k / up move up, clamped at zero.
	for i := 1; i <= 4; i++ {
		next, _ := UpdateWithRefresh(m, keyMsg("k"))
		m = next.(Model)
		want := 2 - i
		if want < 0 {
			want = 0
		}
		if m.cursor != want {
			t.Fatalf("after %d k presses: cursor = %d, want %d", i, m.cursor, want)
		}
	}
}

func TestUpdateCursorBoundsWithShrinkingSnapshot(t *testing.T) {
	// Snapshot shrinks on refresh while the cursor points past the end —
	// the enter handler must not index out of bounds.
	m := modelWithNodes(1)
	next, _ := UpdateWithRefresh(m, keyMsg("j")) // cursor stays 0 (1 node)
	m = next.(Model)
	if m.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 for single-node snapshot", m.cursor)
	}

	m2 := modelWithNodes(3)
	next, _ = UpdateWithRefresh(m2, keyMsg("j"))
	next, _ = UpdateWithRefresh(next.(Model), keyMsg("j"))
	m2 = next.(Model)
	if m2.cursor != 2 {
		t.Fatalf("cursor = %d, want 2", m2.cursor)
	}
	// Refresh arrives with only one node; pressing enter must not panic.
	m2.snapshot = &models.ClusterSnapshot{Nodes: m2.snapshot.Nodes[:1]}
	if _, cmd := UpdateWithRefresh(m2, tea.KeyMsg{Type: tea.KeyEnter}); cmd != nil {
		t.Fatalf("enter with stale cursor returned command %v, want nil", cmd)
	}
}

func TestUpdateTabSwitching(t *testing.T) {
	m := NewModel()

	// l / right advance, clamped at the last tab.
	for i := 1; i <= 5; i++ {
		next, _ := UpdateWithRefresh(m, keyMsg("l"))
		m = next.(Model)
		want := i
		if want > 3 {
			want = 3
		}
		if m.activeTab != want {
			t.Fatalf("after %d l presses: activeTab = %d, want %d", i, m.activeTab, want)
		}
	}

	// h / left retreat, clamped at zero.
	for i := 1; i <= 5; i++ {
		next, _ := UpdateWithRefresh(m, keyMsg("h"))
		m = next.(Model)
		want := 3 - i
		if want < 0 {
			want = 0
		}
		if m.activeTab != want {
			t.Fatalf("after %d h presses: activeTab = %d, want %d", i, m.activeTab, want)
		}
	}

	// Number keys jump directly.
	for key, want := range map[string]int{"1": 0, "2": 1, "3": 2, "4": 3} {
		next, _ := UpdateWithRefresh(m, keyMsg(key))
		if got := next.(Model).activeTab; got != want {
			t.Fatalf("key %q: activeTab = %d, want %d", key, got, want)
		}
	}
}

func TestUpdateSnapshotLoadedSchedulesTick(t *testing.T) {
	m := NewModel()
	next, cmd := UpdateWithRefresh(m, snapshotLoadedMsg{
		Snapshot:  &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "n"}}},
		Timestamp: "9:41AM",
	})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("snapshotLoadedMsg should schedule a refresh tick")
	}
	if m.loading {
		t.Fatal("loading should be false after snapshot loads")
	}
	if m.loadErr != nil {
		t.Fatalf("loadErr = %v, want nil", m.loadErr)
	}
	if m.snapshot == nil || len(m.snapshot.Nodes) != 1 {
		t.Fatalf("snapshot not stored: %+v", m.snapshot)
	}
}

func TestUpdateLoadErrorSchedulesRetry(t *testing.T) {
	m := NewModel()
	next, cmd := UpdateWithRefresh(m, loadErrMsg{Err: errBoom})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("loadErrMsg should schedule a retry tick")
	}
	if m.loading {
		t.Fatal("loading should be false after error surfaces")
	}
	if m.loadErr == nil {
		t.Fatal("loadErr should be retained after error")
	}
}

func TestRefreshErrorKeepsPreviousSnapshotVisible(t *testing.T) {
	m := modelWithNodes(1)
	m.snapshot.Nodes[0].Name = "known-node"
	m.source = "daemon-cache"
	m.lastRefresh = "9:41AM"

	next, cmd := UpdateWithRefresh(m, loadErrMsg{Err: errBoom})
	if cmd == nil {
		t.Fatal("refresh error should schedule a retry")
	}
	got := next.(Model)
	if !strings.Contains(got.statusMsg, "Refresh failed; showing previous snapshot") {
		t.Fatalf("status = %q, want stale-snapshot warning", got.statusMsg)
	}

	out := stripANSI(ViewWithLogo(got))
	if !strings.Contains(out, "known-node") {
		t.Fatalf("previous snapshot missing after refresh error:\n%s", out)
	}
	if strings.Contains(out, "Snapshot unavailable") {
		t.Fatalf("refresh error incorrectly replaced valid snapshot with onboarding:\n%s", out)
	}
}

func TestUpdateTickSkipsWhenLoading(t *testing.T) {
	m := NewModel()
	m.loading = true
	next, cmd := UpdateWithRefresh(m, tickMsg{})
	if cmd == nil {
		t.Fatal("tick while loading should reschedule itself")
	}
	if next.(Model).loading != true {
		t.Fatal("tick while loading must not trigger a concurrent load")
	}
}

func TestUpdateWindowSize(t *testing.T) {
	m := NewModel()
	next, _ := UpdateWithRefresh(m, tea.WindowSizeMsg{Width: 120, Height: 40})
	m = next.(Model)
	if m.width != 120 || m.height != 40 {
		t.Fatalf("dimensions = %dx%d, want 120x40", m.width, m.height)
	}
}

func TestUpdateOpensPlacementModalWithDisplayedAuthority(t *testing.T) {
	m := modelWithNodes(1)
	m.source = "daemon-cache"
	m.lastRefresh = "9:41AM"
	next, cmd := UpdateWithRefresh(m, keyMsg("p"))
	if cmd == nil {
		t.Fatal("placement key returned nil command, want text-input blink command")
	}
	got := next.(Model)
	if !got.modalActive {
		t.Fatal("placement key did not activate modal")
	}
	if got.modal.snapshot == m.snapshot {
		t.Fatal("placement modal retained mutable dashboard snapshot pointer")
	}
	if got.modal.source != "daemon-cache" || got.modal.freshness != "9:41AM" {
		t.Fatalf("modal authority = %q/%q, want daemon-cache/9:41AM", got.modal.source, got.modal.freshness)
	}
}

func TestPlacementModalPreservesObservedTimeDuringRefresh(t *testing.T) {
	m := modelWithNodes(1)
	m.source = "daemon-cache"
	m.lastRefresh = "9:41AM"

	next, refreshCmd := UpdateWithRefresh(m, keyMsg("r"))
	if refreshCmd == nil {
		t.Fatal("manual refresh returned nil command")
	}
	m = next.(Model)
	if m.lastRefresh != "9:41AM" {
		t.Fatalf("in-flight refresh changed observed time to %q", m.lastRefresh)
	}

	next, _ = UpdateWithRefresh(m, keyMsg("p"))
	got := next.(Model)
	if !got.modalActive {
		t.Fatal("placement modal did not open during in-flight refresh")
	}
	if got.modal.freshness != "9:41AM" {
		t.Fatalf("modal freshness = %q, want last observed time", got.modal.freshness)
	}
	refreshed := &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "new-node", Status: models.StatusComplete}}}
	next, tick := UpdateWithRefresh(got, snapshotLoadedMsg{
		Snapshot:  refreshed,
		Timestamp: "9:42AM",
		Source:    "live",
	})
	if tick == nil {
		t.Fatal("snapshot completion while modal is open did not schedule next refresh")
	}
	got = next.(Model)
	if got.loading || got.snapshot != refreshed || got.source != "live" || got.lastRefresh != "9:42AM" {
		t.Fatalf("root lifecycle not updated behind modal: loading=%v source=%q freshness=%q snapshot=%p",
			got.loading, got.source, got.lastRefresh, got.snapshot)
	}
	if !got.modalActive {
		t.Fatal("snapshot completion closed the placement modal")
	}
	if got.modal.snapshot.Nodes[0].Name != "node" || got.modal.freshness != "9:41AM" {
		t.Fatalf("modal authority changed during refresh: node=%q freshness=%q",
			got.modal.snapshot.Nodes[0].Name, got.modal.freshness)
	}
}

func TestUpdateRoutesKeysToPlacementModal(t *testing.T) {
	m := modelWithNodes(1)
	m.modalActive = true
	m.modal = NewPlacementModal(m.snapshot, "file", "unknown")
	next, cmd := UpdateWithRefresh(m, tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("modal escape returned command %v, want nil", cmd)
	}
	if next.(Model).modalActive {
		t.Fatal("modal remained active after Escape")
	}
}

func TestUpdatePlacementConfirmationNamesNodeAndExecutionBoundary(t *testing.T) {
	m := modelWithNodes(1)
	m.modalActive = true
	m.modal = NewPlacementModal(m.snapshot, "daemon-cache", "9:41AM")
	m.modal.explanation = &models.PlacementExplanation{
		Decision: models.PlacementDecision{Node: "node-a", OK: true},
	}

	next, _ := UpdateWithRefresh(m, tea.KeyMsg{Type: tea.KeyEnter})
	got := next.(Model)
	if got.modalActive {
		t.Fatal("confirmed placement modal remained active")
	}
	if want := "Recommended: node-a · advisory only · no task executed"; got.statusMsg != want {
		t.Fatalf("status = %q, want %q", got.statusMsg, want)
	}
}

func TestSnapshotRecoveryExplainsExplicitSetupAndRetry(t *testing.T) {
	m := NewModel()
	m.loading = false
	m.loadErr = errBoom

	out := stripANSI(ViewWithLogo(m))
	for _, want := range []string{
		"Snapshot unavailable",
		"axis init",
		"axis daemon service install",
		"axis daemon start",
		"Press r here to retry immediately",
		"The TUI starts nothing until you run one of these commands.",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("recovery view missing %q in:\n%s", want, out)
		}
	}
}

func TestHelpListsPlacementAndRefreshKeys(t *testing.T) {
	m := modelWithNodes(1)
	next, _ := UpdateWithRefresh(m, keyMsg("?"))
	status := next.(Model).statusMsg
	for _, want := range []string{"p place", "r refresh", "? help"} {
		if !strings.Contains(status, want) {
			t.Errorf("help status missing %q in %q", want, status)
		}
	}
}

type boomError struct{}

func (boomError) Error() string { return "boom" }

var errBoom = boomError{}
