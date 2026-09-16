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
