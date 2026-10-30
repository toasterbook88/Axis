package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/runtimectx"
	"github.com/toasterbook88/axis/internal/snapshotview"
	"github.com/toasterbook88/axis/internal/state"
)

// PlacementModal implements an interactive workload placement preview.
type PlacementModal struct {
	input     textinput.Model
	loading   bool
	confirmed bool
	cancelled bool

	snapshot     *models.ClusterSnapshot
	source       string
	freshness    string
	requirements models.TaskRequirements
	explanation  *models.PlacementExplanation
	scoringError error
	width        int
	height       int      // terminal rows; 0 until the first WindowSizeMsg
	glyphs       glyphSet // follows the dashboard (ASCII when needed)
}

// NewPlacementModal creates a placement wizard bound to the snapshot already
// displayed by the dashboard. Scoring never silently switches authority.
func NewPlacementModal(snapshot *models.ClusterSnapshot, source, freshness string) PlacementModal {
	input := textinput.New()
	input.Placeholder = "e.g., run ollama inference on a 7b model"
	input.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	input.Focus()
	input.CharLimit = 200
	input.Width = 60
	if strings.TrimSpace(source) == "" {
		source = "unknown"
	}
	if strings.TrimSpace(freshness) == "" {
		freshness = "unknown"
	}

	return PlacementModal{
		input:     input,
		snapshot:  snapshotview.Clone(snapshot),
		source:    source,
		freshness: freshness,
		width:     76,
		glyphs:    unicodeGlyphs,
	}
}

// Init initializes the modal.
func (m PlacementModal) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles events and returns the updated modal and commands.
func (m PlacementModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = min(msg.Width-10, 80)
		if m.width < 30 {
			m.width = 30
		}
		m.input.Width = m.width - 10
		m.height = msg.Height
		return m, nil

	case placementScoredMsg:
		m.loading = false
		m.requirements = msg.Requirements
		m.explanation = msg.Explanation
		m.scoringError = msg.Error
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, nil
		case "e":
			if m.explanation != nil || m.scoringError != nil {
				m.explanation = nil
				m.scoringError = nil
				m.input.Focus()
				return m, textinput.Blink
			}
		case "enter":
			if m.loading || m.confirmed {
				return m, nil
			}
			if m.explanation != nil {
				if m.explanation.Decision.OK {
					m.confirmed = true
				}
				return m, nil
			}
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, nil
			}
			m.loading = true
			m.scoringError = nil
			m.input.Blur()
			return m, placeTaskCmd(m.input.Value(), m.snapshot)
		}
		if m.explanation != nil || m.scoringError != nil {
			return m, nil
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the modal overlay, at most m.width columns wide and, once
// the terminal size is known, at most m.height rows tall.
func (m PlacementModal) View() string {
	g := m.glyphs
	vpad := 1
	if m.height > 0 && m.height < 26 {
		vpad = 0
	}
	border := lipgloss.NewStyle().
		Border(g.border).
		BorderForeground(lipgloss.Color("63")).
		Padding(vpad, 2)

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("229")).
		Render("Workload Placement Wizard")
	instructions := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Render("Preview deterministic placement from the snapshot on screen")
	provenance := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("Snapshot: %s%sobserved %s", m.source, g.sep, m.freshness))
	inputLabel := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Render("Requirements:")

	var content string
	switch {
	case m.loading:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, provenance, "", instructions, "", inputLabel, m.input.View(), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Render("Analyzing eligible nodes and rank order..."),
		)
	case m.scoringError != nil:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, provenance, "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Unable to score placement"), "",
			truncPlain(m.scoringError.Error(), m.innerWidth(), g.ellipsis), "", "Press e to edit, Escape to close",
		)
	case m.explanation != nil:
		budget := 0
		if m.height > 0 {
			budget = m.height - 2 - 2*vpad // border and padding rows
		}
		content = renderPlacementDecision(m, title, provenance, instructions, inputLabel, budget)
	default:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, provenance, "", instructions, "", inputLabel, m.input.View(), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render("Try a recognized workload description:"),
			"  "+g.bullet+" build a Go project",
			"  "+g.bullet+" run ollama inference on a 7b model",
			"",
			"Enter score"+g.sep+"Escape close",
		)
	}
	return border.Render(content)
}

// innerWidth is the text width inside the border and padding. A modal
// built without a size (width 0) is not width-limited.
func (m PlacementModal) innerWidth() int {
	if m.width <= 0 {
		return 1 << 16
	}
	return max(m.width-6, 20)
}

// renderPlacementDecision lays out a scored result in at most budget rows
// (0 = unlimited). The header, the recommendation and the advisory footer
// with its keys always show; requirements, rank order and reasons fill the
// rows left, and say how much they had to leave out.
func renderPlacementDecision(m PlacementModal, title, provenance, instructions, inputLabel string, budget int) string {
	g := m.glyphs
	if g.sep == "" {
		g = unicodeGlyphs // zero-value modal
	}
	inner := m.innerWidth()
	clip := func(s string) string { return truncPlain(s, inner, g.ellipsis) }
	bold := lipgloss.NewStyle().Bold(true)
	faint := lipgloss.NewStyle().Foreground(lipgloss.Color("245"))

	decision := m.explanation.Decision
	node := decision.Node
	if node == "" {
		node = "No suitable node"
	}

	reasoning := make([]string, 0, min(len(decision.Reasoning), 4)+1)
	for i, reason := range decision.Reasoning {
		if i >= 4 {
			reasoning = append(reasoning, fmt.Sprintf("  + %d more", len(decision.Reasoning)-4))
			break
		}
		reasoning = append(reasoning, clip("  "+g.bullet+" "+reason))
	}
	if len(reasoning) == 0 {
		reasoning = append(reasoning, "  "+g.bullet+" No eligible node satisfied the inferred requirements")
	}

	ranked := make([]string, 0, min(len(m.explanation.Eligible), 3))
	for i, candidate := range m.explanation.Eligible {
		if i >= 3 {
			break
		}
		ranked = append(ranked, clip(fmt.Sprintf("  %d. %s%s%s",
			i+1, candidate.Node, g.sep, strings.ReplaceAll(candidateSummary(candidate, m.snapshot), " · ", g.sep))))
	}
	if len(ranked) == 0 {
		ranked = append(ranked, "  No eligible candidates")
	}

	head := []string{title, provenance, "", inputLabel, m.input.View(), ""}
	rec := []string{bold.Render("Recommendation: ") + clip(node)}
	if decision.OK {
		rec = append(rec, faint.Render(fmt.Sprintf("Diagnostic fit: %d/100%srank order is authoritative", decision.FitScore, g.sep)))
	}
	action := []string{"No eligible recommendation to accept" + g.sep + "e edit" + g.sep + "Escape close"}
	if decision.OK {
		action = []string{
			"Advisory only " + g.dash + " Enter accepts and closes; no task is executed",
			"e edit" + g.sep + "Escape cancel",
		}
	}
	footStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("248"))
	footer := []string{""}
	for _, a := range action {
		footer = append(footer, footStyle.Render(clip(a)))
	}

	var optional []string
	optional = append(optional, "", bold.Render("Inferred requirements"), clip("  "+formatRequirements(m.requirements, g)))
	optional = append(optional, "", bold.Render("Deterministic rank order"))
	optional = append(optional, ranked...)
	optional = append(optional, "", bold.Render("Why"))
	optional = append(optional, reasoning...)

	if budget > 0 {
		room := budget - len(head) - len(rec) - len(footer)
		if room >= len(optional)+2 {
			head = append([]string{title, provenance, "", instructions}, head[2:]...)
		}
		if len(optional) > room {
			hidden := len(optional) - max(room-1, 0)
			optional = optional[:max(room-1, 0)]
			if room >= 1 {
				optional = append(optional, faint.Render(fmt.Sprintf("%s %d more line(s); enlarge the terminal", g.ellipsis, hidden)))
			}
		}
	} else {
		head = append([]string{title, provenance, "", instructions}, head[2:]...)
	}

	lines := append(append(append(head, rec...), optional...), footer...)
	return strings.Join(lines, "\n")
}

func formatRequirements(req models.TaskRequirements, g glyphSet) string {
	parts := []string{"class " + string(req.Workload.Class)}
	if req.MinFreeRAMMB > 0 {
		parts = append(parts, fmt.Sprintf("RAM %s %s", g.ge, formatMB(req.MinFreeRAMMB)))
	}
	if req.ContextWindowTokens > 0 {
		parts = append(parts, fmt.Sprintf("context %dK", req.ContextWindowTokens/1000))
	}
	if len(req.RequiredTools) > 0 {
		parts = append(parts, "tools "+strings.Join(req.RequiredTools, ", "))
	}
	if len(req.PreferredBackends) > 0 {
		parts = append(parts, "prefers "+strings.Join(req.PreferredBackends, ", "))
	}
	return strings.Join(parts, g.sep)
}

func candidateSummary(candidate models.PlacementCandidateExplanation, snapshot *models.ClusterSnapshot) string {
	parts := []string{fmt.Sprintf("headroom %s", formatMB(candidate.HeadroomMB))}
	if snapshot != nil {
		for _, node := range snapshot.Nodes {
			if node.Name != candidate.Node {
				continue
			}
			if node.Resources != nil && len(node.Resources.GPUs) > 0 {
				parts = append(parts, fmt.Sprintf("%d GPU", len(node.Resources.GPUs)))
			}
			// Only rows that are serving count as warm; loading, down and
			// merely listed models are not.
			warm := 0
			for _, rm := range node.ResidentModels {
				if rm.Serving() {
					warm++
				}
			}
			if warm > 0 {
				parts = append(parts, fmt.Sprintf("%d warm model", warm))
			}
			break
		}
	}
	return strings.Join(parts, " · ")
}

func formatMB(value int64) string {
	if value >= 1024 {
		return fmt.Sprintf("%.1fGiB", float64(value)/1024)
	}
	return fmt.Sprintf("%dMiB", value)
}

type placementScoredMsg struct {
	Requirements models.TaskRequirements
	Explanation  *models.PlacementExplanation
	Error        error
}

// placeTaskCmd scores the prompt against the exact snapshot displayed when the
// wizard opened. It is advisory and never executes or mutates cluster state.
func placeTaskCmd(prompt string, snapshot *models.ClusterSnapshot) tea.Cmd {
	return func() tea.Msg {
		if snapshot == nil {
			return placementScoredMsg{Error: fmt.Errorf("no displayed snapshot is available; close the wizard and recover the dashboard first")}
		}

		requirements := placement.InferRequirements(prompt)
		clusterState, stateErr := state.Load()

		explanation := placement.ExplainPlacement(requirements, snapshot.Nodes, clusterState)
		warnings := append([]models.Warning(nil), snapshot.Warnings...)
		if stateErr != nil {
			warnings = append(warnings, models.Warning{
				Kind:    "state",
				Message: "cluster state could not be fully loaded: " + stateErr.Error(),
			})
		}
		explanation.Decision.Reasoning = runtimectx.PrependWarningReasoning(
			explanation.Decision.Reasoning, warnings)
		return placementScoredMsg{Requirements: requirements, Explanation: &explanation}
	}
}
