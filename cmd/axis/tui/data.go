package tui

import (
	"encoding/json"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

// loadSnapshotCmd loads the daemon snapshot asynchronously.
func loadSnapshotCmd() tea.Cmd {
	return func() tea.Msg {
		snapshot, freshness, err := loadDaemonSnapshot()
		if err != nil {
			return loadErrMsg{Err: err}
		}
		return snapshotLoadedMsg{
			Snapshot:  snapshot,
			Timestamp: formatFreshness(freshness),
		}
	}
}

// formatFreshness renders a snapshot timestamp for the header. A zero
// time (timestamp absent and mtime unavailable) renders as "unknown"
// instead of a fabricated clock reading.
func formatFreshness(t time.Time) string {
	if t.IsZero() {
		return "unknown"
	}
	return t.Format(time.Kitchen)
}

// loadDaemonSnapshot reads the cached snapshot from disk using AXIS_HOME-aware paths.
// Returns the snapshot, its collection timestamp, and any error.
func loadDaemonSnapshot() (*models.ClusterSnapshot, time.Time, error) {
	// Use persist.AxisPath for AXIS_HOME-aware path resolution
	cachePath := persist.AxisPath("snapshot.json")

	data, err := os.ReadFile(cachePath)
	if err != nil {
		// Return nil snapshot if file doesn't exist (truth-plane: don't fabricate "0 nodes")
		return nil, time.Time{}, err
	}

	var snapshot models.ClusterSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, time.Time{}, err
	}

	// Use the snapshot's actual collection timestamp, not file mtime.
	// Degraded fallback: file mtime when the timestamp is absent. If even
	// the stat fails, leave freshness zero so the header renders an
	// explicit "unknown" rather than a fabricated time.
	freshness := snapshot.Timestamp
	if freshness.IsZero() {
		if info, err := os.Stat(cachePath); err == nil {
			freshness = info.ModTime()
		}
	}

	return &snapshot, freshness, nil
}
