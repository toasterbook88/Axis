package tui

import (
	"encoding/json"
	"os"
	"path/filepath"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/toasterbook88/axis/internal/models"
)

// loadSnapshotCmd loads the daemon snapshot asynchronously.
func loadSnapshotCmd() tea.Cmd {
	return func() tea.Msg {
		snapshot, err := loadDaemonSnapshot()
		if err != nil {
			return loadErrMsg{Err: err}
		}
		return snapshotLoadedMsg{
			Snapshot:  snapshot,
			Timestamp: time.Now().Format("15:04:05"),
		}
	}
}

// loadDaemonSnapshot reads the cached snapshot from disk.
func loadDaemonSnapshot() (*models.ClusterSnapshot, error) {
	// Try daemon cache first
	cachePath := filepath.Join(os.Getenv("HOME"), ".local", "share", "axis", "snapshot.json")

	// Fallback to legacy path
	if _, err := os.Stat(cachePath); os.IsNotExist(err) {
		cachePath = filepath.Join(os.Getenv("HOME"), ".axis", "snapshot.json")
	}

	data, err := os.ReadFile(cachePath)
	if err != nil {
		// Return empty snapshot if file doesn't exist
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{},
		}, nil
	}

	var snapshot models.ClusterSnapshot
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return nil, err
	}

	return &snapshot, nil
}
