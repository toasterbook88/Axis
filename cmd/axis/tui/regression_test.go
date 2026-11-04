package tui

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

// Regression: renderInspectorEnhanced panicked with "strings: negative
// Repeat count" when the first View() fired before tea.WindowSizeMsg set
// m.width (observed live on `axis tui` launch at 0-width terminals).
// The divider must clamp instead of repeating a negative count.
func TestRenderInspectorZeroWidthDoesNotPanic(t *testing.T) {
	m := modelWithNodes(1)
	m.width = 0
	m.height = 0

	// Zero size means no WindowSizeMsg yet; the inspector renders at the
	// default frame size and must not panic on any degenerate size.
	for _, sz := range [][2]int{{0, 0}, {1, 1}, {7, 2}, {8, 3}} {
		_ = renderInspector(m, sz[0], sz[1])
	}
	out := ViewWithLogo(m)
	if !strings.Contains(out, "Overview") {
		t.Fatalf("inspector missing tab bar at zero width: %q", out)
	}
}

// Same contract for the whole view path, including header and footer.
func TestViewWithLogoZeroWidthDoesNotPanic(t *testing.T) {
	m := modelWithNodes(2)
	m.width = 0
	m.height = 0

	out := ViewWithLogo(m)
	if !strings.Contains(out, "NODE") {
		t.Fatalf("fleet header missing at zero width: %q", out)
	}
}

// Cache explicitness: the header must badge the provenance of the snapshot
// it displays. An unknown source renders "unknown", never a fabricated one.
func TestHeaderBadgesSnapshotSource(t *testing.T) {
	m := modelWithNodes(1)
	m.source = "daemon-cache"
	out := renderHeader(m, 120)
	if !strings.Contains(out, "daemon-cache") {
		t.Fatalf("header missing daemon-cache badge: %q", out)
	}

	emptySource := modelWithNodes(1)
	outUnknown := renderHeader(emptySource, 120)
	if !strings.Contains(outUnknown, "unknown") {
		t.Fatalf("header missing unknown badge when source unset: %q", outUnknown)
	}
}

// The snapshot-loaded handler must retain the provenance source for badge
// rendering across the update cycle.
func TestSnapshotLoadedMsgRetainsSource(t *testing.T) {
	m := modelWithNodes(1)
	m.loading = true

	msg := snapshotLoadedMsg{
		Snapshot:  m.snapshot,
		Timestamp: "9:41AM",
		Source:    "live",
	}
	next, _ := UpdateWithRefresh(m, msg)
	got := next.(Model)
	if got.source != "live" {
		t.Fatalf("source = %q, want %q", got.source, "live")
	}
	if got.loading {
		t.Fatal("loading must clear after snapshotLoadedMsg")
	}
}

// Footer must keep every primary interaction visible after the normal
// snapshot-loaded path sets a status message.
func TestFooterKeepsKeybindingsVisibleAfterSnapshotLoad(t *testing.T) {
	m := NewModel()
	next, _ := UpdateWithRefresh(m, snapshotLoadedMsg{
		Snapshot:  &models.ClusterSnapshot{Nodes: []models.NodeFacts{{Name: "node-a"}}},
		Timestamp: "9:41AM",
		Source:    "daemon-cache",
	})
	m = next.(Model)

	out := stripANSI(renderFooter(m, 120))
	for _, want := range []string{
		"Snapshot loaded: 1 nodes",
		"select",
		"tab",
		"p place",
		"r refresh",
		"? help",
		"q quit",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("post-load footer missing %q in %q", want, out)
		}
	}
}

// loadDaemonSnapshot stays the explicit file fallback: nil snapshot on
// missing file, real error surfaced. (Companion to the daemon-first
// loadSnapshotCmd authority order.)
func TestLoadDaemonSnapshotRemainsFileScoped(t *testing.T) {
	t.Setenv(persist.AxisHomeEnv, t.TempDir())

	snap, _, err := loadDaemonSnapshot()
	if err == nil {
		t.Fatal("expected error for missing snapshot file, got nil (truth-plane: must not fabricate empty state)")
	}
	if snap != nil {
		t.Fatalf("expected nil snapshot, got %+v", snap)
	}
}
