package tui

import (
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// refreshInterval is the auto-refresh duration for the dashboard.
const refreshInterval = 30 * time.Second

// retryInterval is the auto-refresh duration after a failed load.
const retryInterval = 5 * time.Second

// tickCmd schedules a refresh for generation gen after d.
func tickCmd(d time.Duration, gen int) tea.Cmd {
	return tea.Tick(d, func(time.Time) tea.Msg { return tickMsg{gen: gen} })
}

// UpdateWithRefresh is the dashboard's state transition function.
func UpdateWithRefresh(m Model, msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.keepCursorVisible()
		if m.modalActive {
			updated, cmd := m.modal.Update(msg)
			m.modal = updated.(PlacementModal)
			return m, cmd
		}
		return m, nil

	case clockMsg:
		return m, clockCmd()

	case snapshotLoadedMsg:
		m.snapshot = msg.Snapshot
		m.loading = false
		m.loadErr = nil
		m.lastRefresh = msg.Timestamp
		m.source = msg.Source
		m.observedAt = msg.Observed
		if m.observedAt.IsZero() && m.snapshot != nil {
			m.observedAt = m.snapshot.Timestamp
		}
		nodes := 0
		if m.snapshot != nil {
			nodes = len(m.snapshot.Nodes)
		}
		if m.cursor >= nodes {
			m.cursor = max(nodes-1, 0)
		}
		m.keepCursorVisible()
		m.statusMsg = fmt.Sprintf("Snapshot loaded: %d nodes", nodes)
		m.refreshGen++
		return m, tickCmd(refreshInterval, m.refreshGen)

	case loadErrMsg:
		m.loading = false
		m.loadErr = msg.Err
		if m.snapshot != nil {
			m.statusMsg = "Refresh failed; showing previous snapshot: " + msg.Err.Error()
		} else {
			m.statusMsg = "Error: " + msg.Err.Error()
		}
		m.refreshGen++
		return m, tickCmd(retryInterval, m.refreshGen)

	case tickMsg:
		// A superseded generation, or a load already in flight (whose result
		// schedules the next tick), must not start another load.
		if msg.gen != m.refreshGen || m.loading {
			return m, nil
		}
		m.loading = true
		return m, loadSnapshotCmd()
	}

	if m.modalActive {
		updated, cmd := m.modal.Update(msg)
		m.modal = updated.(PlacementModal)
		if m.modal.cancelled || m.modal.confirmed {
			m.modalActive = false
			if m.modal.confirmed && m.modal.explanation != nil {
				m.statusMsg = fmt.Sprintf("Recommended: %s · advisory only · no task executed",
					m.modal.explanation.Decision.Node)
			}
		}
		return m, cmd
	}

	key, ok := msg.(tea.KeyMsg)
	if !ok {
		return m, nil
	}
	if m.showHelp {
		switch key.String() {
		case "?", "esc", "q", "enter":
			m.showHelp = false
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
		return m, nil
	}
	return handleKey(m, key)
}

func handleKey(m Model, key tea.KeyMsg) (tea.Model, tea.Cmd) {
	nodes := 0
	if m.snapshot != nil {
		nodes = len(m.snapshot.Nodes)
	}
	switch key.String() {
	case "q", "ctrl+c":
		m.quitting = true
		return m, tea.Quit

	case "j", "down":
		if m.cursor < nodes-1 {
			m.cursor++
		}
	case "k", "up":
		if m.cursor > 0 {
			m.cursor--
		}
	case "g", "home":
		m.cursor = 0
	case "G", "end":
		m.cursor = max(nodes-1, 0)
	case "pgdown", "ctrl+d":
		m.cursor = min(m.cursor+max(m.tableRows(), 1), max(nodes-1, 0))
	case "pgup", "ctrl+u":
		m.cursor = max(m.cursor-max(m.tableRows(), 1), 0)

	case "1", "2", "3", "4":
		m.activeTab = int(key.String()[0] - '1')
	case "h", "left":
		if m.activeTab > 0 {
			m.activeTab--
		}
	case "l", "right":
		if m.activeTab < len(inspectorTabs)-1 {
			m.activeTab++
		}
	case "tab":
		m.activeTab = (m.activeTab + 1) % len(inspectorTabs)
	case "shift+tab":
		m.activeTab = (m.activeTab + len(inspectorTabs) - 1) % len(inspectorTabs)

	case "r":
		if m.loading {
			m.statusMsg = "Refresh already in progress"
			return m, nil
		}
		m.loading = true
		m.statusMsg = "Refreshing cluster snapshot..."
		return m, loadSnapshotCmd()

	case "p":
		if m.snapshot == nil {
			m.statusMsg = "Placement needs a snapshot; follow the recovery steps and press r"
			return m, nil
		}
		m.modal = NewPlacementModal(m.snapshot, m.source, m.lastRefresh)
		m.modalActive = true
		initCmd := m.modal.Init()
		if m.width > 0 {
			updated, sizeCmd := m.modal.Update(tea.WindowSizeMsg{Width: m.width, Height: m.height})
			m.modal = updated.(PlacementModal)
			initCmd = tea.Batch(initCmd, sizeCmd)
		}
		return m, initCmd

	case "?":
		m.showHelp = true
	}
	m.keepCursorVisible()
	return m, nil
}

// keepCursorVisible scrolls the node table so the cursor row is on screen.
func (m *Model) keepCursorVisible() {
	rows := m.tableRows()
	if rows <= 0 {
		m.tableOffset = 0
		return
	}
	if m.cursor < m.tableOffset {
		m.tableOffset = m.cursor
	}
	if m.cursor >= m.tableOffset+rows {
		m.tableOffset = m.cursor - rows + 1
	}
	if m.tableOffset < 0 {
		m.tableOffset = 0
	}
}
