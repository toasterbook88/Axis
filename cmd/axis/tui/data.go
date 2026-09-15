package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

// loadSnapshotCmd loads the cluster snapshot for the dashboard.
// Authority order: the local daemon cache first (freshness-badged,
// publication-bound), then an explicit on-disk snapshot.json fallback when
// the daemon is unavailable. When both fail the error names both — the
// truth plane must not fabricate an empty fleet.
func loadSnapshotCmd() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		snap, source, err := daemon.FetchSnapshot(ctx, api.DefaultAddr())
		cancel()
		if err == nil {
			return snapshotLoadedMsg{
				Snapshot:  snap,
				Timestamp: formatFreshness(snap.Timestamp),
				Source:    source,
			}
		}

		snapshot, freshness, fileErr := loadDaemonSnapshot()
		if fileErr != nil {
			return loadErrMsg{
				Err: fmt.Errorf("no daemon cache (%v) and no snapshot file (%v); run: axis daemon start", err, fileErr),
			}
		}
		return snapshotLoadedMsg{
			Snapshot:  snapshot,
			Timestamp: formatFreshness(freshness),
			Source:    "file",
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
