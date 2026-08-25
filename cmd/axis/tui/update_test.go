package tui

import (
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

func TestUpdateOpensPlacementModal(t *testing.T) {
	m := modelWithNodes(1)
	next, cmd := UpdateWithRefresh(m, keyMsg("p"))
	if cmd == nil {
		t.Fatal("placement key returned nil command, want text-input blink command")
	}
	if !next.(Model).modalActive {
		t.Fatal("placement key did not activate modal")
	}
}

func TestUpdateRoutesKeysToPlacementModal(t *testing.T) {
	m := modelWithNodes(1)
	m.modalActive = true
	m.modal = NewPlacementModal()

	next, cmd := UpdateWithRefresh(m, tea.KeyMsg{Type: tea.KeyEscape})
	if cmd != nil {
		t.Fatalf("modal escape returned command %v, want nil", cmd)
	}
	if next.(Model).modalActive {
		t.Fatal("modal remained active after Escape")
	}
}

type boomError struct{}

func (boomError) Error() string { return "boom" }

var errBoom = boomError{}
