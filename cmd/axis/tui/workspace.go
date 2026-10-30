package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/console"
)

// Workspace is the full-screen axis tui: the agent conversation as the main
// pane, a live cluster sidebar beside it, a status bar, and the cluster
// dashboard one key away. The conversation is the console from axis agent,
// hosted in embedded mode, so every agent feature (streaming, tool cards,
// approvals, /model, /skills, /mcp, @node completion) works unchanged.
//
// The dashboard Model doubles as the workspace's data source: it owns the
// snapshot load and refresh cycle the sidebar reads.
type Workspace struct {
	chat tea.Model
	dash Model

	view          wsView
	sidebarHidden bool // operator toggle; narrow terminals hide it regardless
	leader        bool // ctrl+x pressed, awaiting the second key
	palette       *palette
	showHelp      bool

	width, height int
}

type wsView int

const (
	viewChat wsView = iota
	viewDashboard
)

// sidebarWidth is the sidebar's width, borders included.
const sidebarWidth = 34

// minSidebarTerminal is the narrowest terminal that still shows the sidebar.
const minSidebarTerminal = 100

// NewWorkspace hosts chat (an embedded console) beside the cluster views.
func NewWorkspace(chat tea.Model) Workspace {
	dash := NewModel()
	dash.hosted = true
	return Workspace{chat: chat, dash: dash}
}

// Init starts the conversation and the snapshot cycle.
func (w Workspace) Init() tea.Cmd {
	return tea.Batch(w.chat.Init(), w.dash.Init(), tea.SetWindowTitle("axis"))
}

func (w Workspace) sidebarVisible() bool {
	return !w.sidebarHidden && w.width >= minSidebarTerminal
}

// chatSize is the conversation pane: everything left of the sidebar, above
// the status bar.
func (w Workspace) chatSize() (int, int) {
	cw := w.width
	if w.sidebarVisible() {
		cw -= sidebarWidth
	}
	return max(cw, 1), max(w.height-1, 1)
}

func (w Workspace) resizeChat() (Workspace, tea.Cmd) {
	cw, ch := w.chatSize()
	var cmd tea.Cmd
	w.chat, cmd = w.chat.Update(tea.WindowSizeMsg{Width: cw, Height: ch})
	return w, cmd
}

// isDashboardMsg reports messages produced by the dashboard's own commands.
func isDashboardMsg(msg tea.Msg) bool {
	switch msg.(type) {
	case snapshotLoadedMsg, loadErrMsg, tickMsg, clockMsg, placementScoredMsg:
		return true
	}
	return false
}

// Update routes each message to the pane that owns it.
func (w Workspace) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		w.width, w.height = msg.Width, msg.Height
		var dcmd tea.Cmd
		var next tea.Model
		next, dcmd = w.dash.Update(msg)
		w.dash = next.(Model)
		var ccmd tea.Cmd
		w, ccmd = w.resizeChat()
		return w, tea.Batch(dcmd, ccmd)

	case tea.KeyMsg:
		return w.handleKey(msg)
	}

	if isDashboardMsg(msg) {
		next, cmd := w.dash.Update(msg)
		w.dash = next.(Model)
		return w, cmd
	}
	// Everything else belongs to the conversation (agent stream, tool and
	// startup events). The placement wizard's text input also needs blink
	// ticks while it is open.
	var cmds []tea.Cmd
	var cmd tea.Cmd
	w.chat, cmd = w.chat.Update(msg)
	cmds = append(cmds, cmd)
	if w.dash.modalActive {
		next, dcmd := w.dash.Update(msg)
		w.dash = next.(Model)
		cmds = append(cmds, dcmd)
	}
	return w, tea.Batch(cmds...)
}

func (w Workspace) handleKey(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	k := key.String()

	if w.palette != nil {
		return w.updatePalette(key)
	}
	// Fast typing arrives as one multi-rune message. Outside the
	// conversation every binding is a single key, so replay them singly.
	if split := splitRunes(key); split != nil && (w.leader || w.view == viewDashboard) {
		var cmds []tea.Cmd
		for _, k := range split {
			next, cmd := w.handleKey(k)
			w = next.(Workspace)
			cmds = append(cmds, cmd)
		}
		return w, tea.Batch(cmds...)
	}
	if w.showHelp {
		if k == "ctrl+c" {
			return w, tea.Quit
		}
		w.showHelp = false
		return w, nil
	}
	if w.leader {
		w.leader = false
		if a, ok := leaderAction(k); ok {
			return a.run(w)
		}
		return w, nil
	}

	// The placement wizard owns the keyboard wherever it is shown.
	if w.dash.modalActive {
		next, cmd := w.dash.Update(key)
		w.dash = next.(Model)
		return w, cmd
	}

	switch k {
	case "ctrl+x":
		w.leader = true
		return w, nil
	case "ctrl+o":
		w.palette = newPalette()
		return w, nil
	case "f2":
		return w.toggleDashboard()
	}

	if w.view == viewDashboard {
		if !w.dash.showHelp {
			switch k {
			case "esc", "q":
				w.view = viewChat
				return w, nil
			case "a", "enter":
				return w.askAboutSelected()
			}
		}
		next, cmd := w.dash.Update(key)
		w.dash = next.(Model)
		return w, cmd
	}

	var cmd tea.Cmd
	w.chat, cmd = w.chat.Update(key)
	return w, cmd
}

func (w Workspace) toggleDashboard() (tea.Model, tea.Cmd) {
	if w.view == viewDashboard {
		w.view = viewChat
	} else {
		w.view = viewDashboard
	}
	return w, nil
}

// askAboutSelected returns to the conversation with a question about the
// node selected in the dashboard prefilled (not sent).
func (w Workspace) askAboutSelected() (tea.Model, tea.Cmd) {
	w.view = viewChat
	if node, ok := w.dash.selectedNode(); ok {
		var cmd tea.Cmd
		w.chat, cmd = w.chat.Update(console.SetInputMsg{Text: "@" + node.Name + " "})
		return w, cmd
	}
	return w, nil
}

// runInChat submits a slash command through the conversation, exactly as if
// typed.
func (w Workspace) runInChat(text string) (tea.Model, tea.Cmd) {
	w.view = viewChat
	var cmd tea.Cmd
	w.chat, cmd = w.chat.Update(console.RunCommandMsg{Text: text})
	return w, cmd
}

// sendDashKey delivers one key to the dashboard, as if pressed there.
func (w Workspace) sendDashKey(k string) (Workspace, tea.Cmd) {
	next, cmd := w.dash.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
	w.dash = next.(Model)
	return w, cmd
}

// View draws exactly width × height.
func (w Workspace) View() string {
	if w.width <= 0 || w.height <= 0 {
		return ""
	}
	if w.dash.modalActive {
		placed := lipgloss.Place(w.width, w.height, lipgloss.Center, lipgloss.Center, w.dash.modal.View())
		return strings.Join(fitBlock(placed, w.width, w.height), "\n")
	}

	var body []string
	if w.view == viewDashboard {
		body = fitBlock(w.dash.View(), w.width, w.height-1)
	} else {
		cw, ch := w.chatSize()
		chat := fitBlock(w.chat.View(), cw, ch)
		if w.sidebarVisible() {
			side := renderSidebar(w.dash, sidebarWidth, ch)
			for i := range chat {
				chat[i] += side[i]
			}
		}
		body = chat
	}

	switch {
	case w.palette != nil:
		body = overlay(body, w.palette.render(w.dash.glyphs, w.width), w.width)
	case w.showHelp:
		body = overlay(body, renderWorkspaceHelp(w.dash.glyphs), w.width)
	}

	body = append(body, fitLine(renderStatusBar(w), w.width))
	return strings.Join(fitBlock(strings.Join(body, "\n"), w.width, w.height), "\n")
}

// overlay draws box centered over base. Rows the box covers are replaced
// whole (the box on a blank row), which reads as a dimmed backdrop without
// slicing styled text mid-escape.
func overlay(base []string, box string, width int) []string {
	lines := strings.Split(box, "\n")
	if len(lines) > len(base) {
		lines = lines[:len(base)]
	}
	top := max((len(base)-len(lines))/2, 0)
	out := append([]string(nil), base...)
	for i, l := range lines {
		out[top+i] = fitLine(lipgloss.PlaceHorizontal(width, lipgloss.Center, l), width)
	}
	return out
}
