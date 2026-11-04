// Package tui implements axis tui: the full-screen agent workspace and the
// cluster dashboard.
//
// Two Bubble Tea models live here:
//
//   - Workspace (workspace*.go) is what axis tui runs. It hosts a
//     conversation model (the internal/console console in embedded mode,
//     built by cmd/axis) as the main pane, draws a cluster sidebar beside it
//     and a status bar below, and switches the whole screen to the dashboard
//     on F2. ctrl+o opens a command palette and ctrl+x is a leader key; both
//     dispatch the same workspaceActions, which run existing slash commands
//     or dashboard keys, never a new command grammar.
//
//   - Model (model.go, update.go, view*.go) is the dashboard: a node table and
//     a tabbed inspector, plus the placement preview (modal_placement.go). It
//     owns the snapshot load and refresh cycle, so inside the workspace it is
//     also the sidebar's data source; axis tui --dashboard runs it alone.
//
// Message routing in Workspace.Update: window sizes go to both panes;
// snapshot, tick, clock and placement results go to the dashboard; keys go to
// whichever view is active (palette, leader and placement take precedence);
// everything else, chiefly the agent's stream and tool events, goes to the
// conversation.
//
// Truth Rule: everything drawn from the snapshot says "not observed" when the
// snapshot lacks it, and the source and age of the snapshot are always on
// screen. Nothing here executes a task; placement is advisory.
//
// Every View fills exactly the terminal's width and height (fitBlock,
// fitLine), and every glyph has an ASCII fallback (theme.go). Golden frames in
// testdata/ pin the rendered screens; regenerate them with
// UPDATE_GOLDEN=true go test ./cmd/axis/tui -run Golden.
//
// The user guide is docs/runbooks/tui.md and the design record is
// docs/decisions/tui-agent-workspace.md.
package tui
