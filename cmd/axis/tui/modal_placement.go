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

// View renders the modal overlay.
func (m PlacementModal) View() string {
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(1, 2)

	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("229")).
		Render("Workload Placement Wizard")
	instructions := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Render("Preview deterministic placement from the snapshot on screen")
	provenance := lipgloss.NewStyle().
		Foreground(lipgloss.Color("245")).
		Render(fmt.Sprintf("Snapshot: %s · observed %s", m.source, m.freshness))
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
			m.scoringError.Error(), "", "Press e to edit, Escape to close",
		)
	case m.explanation != nil:
		content = renderPlacementDecision(m, title, provenance, instructions, inputLabel)
	default:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, provenance, "", instructions, "", inputLabel, m.input.View(), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render("Try a recognized workload description:"),
			"  • build a Go project",
			"  • run ollama inference on a 7b model",
			"",
			"Enter score · Escape close",
		)
	}
	return border.Render(content)
}

func renderPlacementDecision(m PlacementModal, title, provenance, instructions, inputLabel string) string {
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
		reasoning = append(reasoning, "  • "+reason)
	}
	if len(reasoning) == 0 {
		reasoning = append(reasoning, "  • No eligible node satisfied the inferred requirements")
	}

	ranked := make([]string, 0, min(len(m.explanation.Eligible), 3))
	for i, candidate := range m.explanation.Eligible {
		if i >= 3 {
			break
		}
		ranked = append(ranked, fmt.Sprintf("  %d. %s · %s",
			i+1, candidate.Node, candidateSummary(candidate, m.snapshot)))
	}
	if len(ranked) == 0 {
		ranked = append(ranked, "  No eligible candidates")
	}

	diagnostic := ""
	if decision.OK {
		diagnostic = fmt.Sprintf("Diagnostic fit: %d/100 · rank order is authoritative", decision.FitScore)
	}
	action := "No eligible recommendation to accept · e edit · Escape close"
	if decision.OK {
		action = "Advisory only — Enter accepts and closes; no task is executed\ne edit · Escape cancel"
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		title, provenance, "", instructions, "", inputLabel, m.input.View(), "",
		lipgloss.NewStyle().Bold(true).Render("Inferred requirements"),
		"  "+formatRequirements(m.requirements), "",
		lipgloss.NewStyle().Bold(true).Render("Recommendation: ")+node,
		lipgloss.NewStyle().Foreground(lipgloss.Color("245")).Render(diagnostic), "",
		lipgloss.NewStyle().Bold(true).Render("Deterministic rank order"),
		strings.Join(ranked, "\n"), "",
		lipgloss.NewStyle().Bold(true).Render("Why"),
		strings.Join(reasoning, "\n"), "",
		lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render(action),
	)
}

func formatRequirements(req models.TaskRequirements) string {
	parts := []string{"class " + string(req.Workload.Class)}
	if req.MinFreeRAMMB > 0 {
		parts = append(parts, fmt.Sprintf("RAM ≥ %s", formatMB(req.MinFreeRAMMB)))
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
	return strings.Join(parts, " · ")
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
			if len(node.ResidentModels) > 0 {
				parts = append(parts, fmt.Sprintf("%d warm model", len(node.ResidentModels)))
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

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
