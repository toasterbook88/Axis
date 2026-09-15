package tui

import (
	"github.com/toasterbook88/axis/internal/persist"
	"strings"
	"testing"
)

// Regression: renderInspectorEnhanced panicked with "strings: negative
// Repeat count" when the first View() fired before tea.WindowSizeMsg set
// m.width (observed live on `axis tui` launch at 0-width terminals).
// The divider must clamp instead of repeating a negative count.
func TestRenderInspectorZeroWidthDoesNotPanic(t *testing.T) {
	m := modelWithNodes(1)
	m.width = 0
	m.height = 0

	out := renderInspector(m)
	if out == "" {
		t.Fatal("renderInspector returned empty string at zero width")
	}
	if !strings.Contains(out, "Details") {
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
	out := renderHeaderWithLogo(m)
	if !strings.Contains(out, "daemon-cache") {
		t.Fatalf("header missing daemon-cache badge: %q", out)
	}

	emptySource := modelWithNodes(1)
	outUnknown := renderHeaderWithLogo(emptySource)
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

// Footer must advertise the placement wizard and help keys; these were
// wired but undiscoverable.
func TestFooterAdvertisesHiddenKeys(t *testing.T) {
	m := NewModel()
	out := renderFooter(m)
	for _, want := range []string{"[p] Place task", "[?] Help"} {
		if !strings.Contains(out, want) {
			t.Fatalf("footer missing %q in %q", want, out)
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
