package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/models"
)

// Model defines the root TUI state machine.
type Model struct {
	// Cluster state
	snapshot    *models.ClusterSnapshot
	loading     bool
	loadErr     error
	lastRefresh string

	// Navigation
	cursor    int // Selected row index
	activeTab int // Inspector tab (0=Details, 1=Backends, 2=Storage, 3=Reservations)

	// Dimensions
	width  int
	height int

	// Status
	statusMsg string
	quitting  bool
}

// NewModel creates an initialized TUI model.
func NewModel() Model {
	return Model{
		cursor:      0,
		activeTab:   0,
		loading:     true,
		lastRefresh: "never",
	}
}

// Init initializes the TUI and returns the first command.
func (m Model) Init() tea.Cmd {
	return loadSnapshotCmd()
}

// Update handles events and returns updated model + commands.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return UpdateWithRefresh(m, msg)
}

// View renders the TUI to a string.
func (m Model) View() string {
	return ViewWithLogo(m)
}

// snapshotLoadedMsg carries a loaded snapshot.
type snapshotLoadedMsg struct {
	Snapshot  *models.ClusterSnapshot
	Timestamp string
}

// loadErrMsg carries a load error.
type loadErrMsg struct {
	Err error
}
