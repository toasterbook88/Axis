package tui

import (
	"os"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

// Model defines the root TUI state machine.
type Model struct {
	// Cluster state
	snapshot    *models.ClusterSnapshot
	loading     bool
	loadErr     error
	lastRefresh string    // observed time as a clock reading (placement modal provenance)
	observedAt  time.Time // when the displayed snapshot was collected; zero = unknown

	// refreshGen numbers refresh cycles. Each completed load schedules one
	// tick tagged with the new generation; ticks from older generations are
	// dropped, so manual refreshes never stack extra timer chains.
	refreshGen int

	// Navigation
	cursor      int // Selected row index
	tableOffset int // First visible table row
	activeTab   int // Inspector tab, index into inspectorTabs

	// Dimensions
	width  int
	height int

	// Status
	statusMsg string
	quitting  bool
	source    string // provenance of the displayed snapshot: daemon-cache, live, file

	// Overlays
	showHelp    bool
	modal       PlacementModal
	modalActive bool

	// Rendering environment
	glyphs     glyphSet
	colorDepth ui.ColorDepth
	now        func() time.Time
}

// NewModel creates an initialized TUI model. Glyphs and logo color depth
// follow the terminal environment (ASCII under TERM=linux or a C locale).
func NewModel() Model {
	g := unicodeGlyphs
	if !ui.UnicodeSupported(os.Getenv) {
		g = asciiGlyphs
	}
	return Model{
		loading:     true,
		lastRefresh: "never",
		glyphs:      g,
		colorDepth:  ui.DetectColorDepth(os.Getenv),
		now:         time.Now,
	}
}

// Init loads the first snapshot and starts the clock that keeps the
// "observed N ago" badge current.
func (m Model) Init() tea.Cmd {
	return tea.Batch(loadSnapshotCmd(), clockCmd())
}

// Update handles events and returns updated model + commands.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	return UpdateWithRefresh(m, msg)
}

// View renders the TUI to a string.
func (m Model) View() string {
	return ViewWithLogo(m)
}

// snapshotLoadedMsg carries a loaded snapshot plus its provenance source
// (daemon-cache, live, file) so views can badge what the operator is seeing.
type snapshotLoadedMsg struct {
	Snapshot  *models.ClusterSnapshot
	Timestamp string
	Observed  time.Time // collection time; falls back to Snapshot.Timestamp
	Source    string
}

// loadErrMsg carries a load error.
type loadErrMsg struct {
	Err error
}

// tickMsg triggers a scheduled snapshot refresh for one generation.
type tickMsg struct{ gen int }

// clockMsg re-renders relative ages once a second.
type clockMsg struct{}

func clockCmd() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return clockMsg{} })
}
