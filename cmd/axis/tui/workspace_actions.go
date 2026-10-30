package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// action is one workspace command, reachable from the ctrl+o palette and as
// ctrl+x <key>.
type action struct {
	key   string
	label string // short name for the leader menu
	title string
	run   func(Workspace) (tea.Model, tea.Cmd)
}

func chatCommand(text string) func(Workspace) (tea.Model, tea.Cmd) {
	return func(w Workspace) (tea.Model, tea.Cmd) { return w.runInChat(text) }
}

func dashKey(k string) func(Workspace) (tea.Model, tea.Cmd) {
	return func(w Workspace) (tea.Model, tea.Cmd) { return w.sendDashKey(k) }
}

var workspaceActions = []action{
	{"d", "dashboard", "Cluster dashboard", func(w Workspace) (tea.Model, tea.Cmd) { return w.toggleDashboard() }},
	{"p", "place", "Placement preview", dashKey("p")},
	{"r", "refresh", "Refresh cluster snapshot", dashKey("r")},
	{"b", "sidebar", "Toggle sidebar", func(w Workspace) (tea.Model, tea.Cmd) {
		w.sidebarHidden = !w.sidebarHidden
		next, cmd := w.resizeChat()
		return next, cmd
	}},
	{"m", "model", "Switch model", chatCommand("/model")},
	{"s", "skills", "Run a learned skill", chatCommand("/skills")},
	{"c", "mcp", "MCP servers", chatCommand("/mcp")},
	{"f", "fleet", "Fleet summary in chat", chatCommand("/fleet")},
	{"u", "usage", "Token usage", chatCommand("/usage")},
	{"n", "clear", "Clear conversation", chatCommand("/clear")},
	{"h", "help", "Keyboard help", func(w Workspace) (tea.Model, tea.Cmd) {
		w.showHelp = true
		return w, nil
	}},
	{"q", "quit", "Quit", func(w Workspace) (tea.Model, tea.Cmd) { return w, tea.Quit }},
}

func leaderAction(k string) (action, bool) {
	for _, a := range workspaceActions {
		if a.key == k {
			return a, true
		}
	}
	return action{}, false
}

// palette is the ctrl+o command list with type-to-filter.
type palette struct {
	query  string
	cursor int
}

func newPalette() *palette { return &palette{} }

func (p *palette) matches() []action {
	q := strings.ToLower(strings.TrimSpace(p.query))
	var out []action
	for _, a := range workspaceActions {
		if q == "" || strings.Contains(strings.ToLower(a.title), q) {
			out = append(out, a)
		}
	}
	return out
}

func (w Workspace) updatePalette(key tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := *w.palette
	switch key.String() {
	case "esc", "ctrl+o":
		w.palette = nil
		return w, nil
	case "ctrl+c":
		return w, tea.Quit
	case "up", "ctrl+p", "shift+tab":
		if p.cursor > 0 {
			p.cursor--
		}
	case "down", "ctrl+n", "tab":
		if p.cursor < len(p.matches())-1 {
			p.cursor++
		}
	case "enter":
		w.palette = nil
		if m := p.matches(); p.cursor < len(m) {
			return m[p.cursor].run(w)
		}
		return w, nil
	case "backspace":
		if r := []rune(p.query); len(r) > 0 {
			p.query = string(r[:len(r)-1])
			p.cursor = 0
		}
	default:
		if key.Type == tea.KeyRunes || key.Type == tea.KeySpace {
			p.query += string(key.Runes)
			if key.Type == tea.KeySpace {
				p.query += " "
			}
			p.cursor = 0
		}
	}
	w.palette = &p
	return w, nil
}

func (p *palette) render(g glyphSet, width int) string {
	boxW := min(56, max(width-6, 20))
	inner := boxW - 4
	prompt := styleAccent.Render(g.cursor+" ") + styleText.Render(p.query) + styleAccent.Render("_")
	if p.query == "" {
		prompt = styleAccent.Render(g.cursor+" ") + styleFaint.Render("type to filter")
	}
	lines := []string{styleBold.Render("Commands"), prompt, styleFaint.Render(strings.Repeat(g.rule, inner))}
	m := p.matches()
	if len(m) == 0 {
		lines = append(lines, styleMuted.Render("no matching command"))
	}
	for i, a := range m {
		hint := "ctrl+x " + a.key
		title := padPlain(a.title, inner-len(hint)-1, g.ellipsis)
		if i == p.cursor {
			sel := lipgloss.NewStyle().Background(colSelBg)
			lines = append(lines, sel.Inherit(styleBold).Render(title+" ")+sel.Inherit(styleAccent).Render(hint))
		} else {
			lines = append(lines, styleText.Render(title+" ")+styleFaint.Render(hint))
		}
	}
	return lipgloss.NewStyle().Border(g.border).BorderForeground(colAccent).Padding(0, 1).
		Width(boxW - 2).Render(strings.Join(lines, "\n"))
}

func renderWorkspaceHelp(g glyphSet) string {
	row := func(k, d string) string { return styleKey.Render(padPlain(k, 16, "")) + styleText.Render(d) }
	lines := []string{
		styleBold.Render("Conversation"),
		row("enter", "send"),
		row("esc", "cancel a running turn"),
		row("pgup  pgdown", "scroll the conversation"),
		row("@node", "complete a node name"),
		row("/  !", "slash command  ·  shell command"),
		row("ctrl+c ctrl+c", "quit"),
		"",
		styleBold.Render("Workspace"),
		row("ctrl+o", "command palette"),
		row("ctrl+x d  f2", "cluster dashboard"),
		row("ctrl+x b", "toggle sidebar"),
		row("ctrl+x p", "placement preview"),
		row("ctrl+x m", "switch model"),
		row("ctrl+x q", "quit"),
		"",
		styleBold.Render("Dashboard"),
		row(g.navKeys+"  "+g.tabKeys, "select node  ·  tab"),
		row("a  enter", "ask the agent about the node"),
		row("esc", "back to the conversation"),
		"",
		styleMuted.Render("Agent output is advisory; the sidebar and dashboard"),
		styleMuted.Render("show only what the cached snapshot observed."),
	}
	return lipgloss.NewStyle().Border(g.border).BorderForeground(colAccent).Padding(1, 3).
		Render(strings.Join(lines, "\n"))
}
