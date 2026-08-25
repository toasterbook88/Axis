package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/state"
)

// PlacementModal implements an interactive workload placement preview.
type PlacementModal struct {
	input     textinput.Model
	loading   bool
	confirmed bool
	cancelled bool

	decision     *models.PlacementDecision
	candidates   []models.NodeFacts
	scoringError error
	width        int
}

// NewPlacementModal creates an initialized placement wizard modal.
func NewPlacementModal() PlacementModal {
	input := textinput.New()
	input.Placeholder = "e.g., run 16GB VRAM model inference"
	input.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	input.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	input.Focus()
	input.CharLimit = 200
	input.Width = 50

	return PlacementModal{input: input, width: 60}
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
		m.decision = msg.Decision
		m.candidates = msg.Candidates
		m.scoringError = msg.Error
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, nil
		case "enter":
			if m.loading || m.confirmed {
				return m, nil
			}
			if m.decision != nil {
				m.confirmed = true
				return m, nil
			}
			if strings.TrimSpace(m.input.Value()) == "" {
				return m, nil
			}
			m.loading = true
			m.scoringError = nil
			return m, placeTaskCmd(m.input.Value())
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
		Render("Preview the advisory placement decision from the current snapshot")
	inputLabel := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Render("Requirements:")

	var content string
	switch {
	case m.loading:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, "", instructions, "", inputLabel, m.input.View(), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Render("Analyzing cluster capacity..."),
		)
	case m.confirmed:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render("Placement selected (advisory)"), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render("No task was executed"),
			"Press Escape to close",
		)
	case m.scoringError != nil:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render("Unable to score placement"), "",
			m.scoringError.Error(), "", "Press Escape to close",
		)
	case m.decision != nil:
		content = renderPlacementDecision(m, title, instructions, inputLabel)
	default:
		content = lipgloss.JoinVertical(lipgloss.Left,
			title, "", instructions, "", inputLabel, m.input.View(), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render("Examples:"),
			"  • run ollama inference on a 7b model",
			"  • build go project with 8GB RAM",
			"  • run 16GB VRAM model inference",
		)
	}
	return border.Render(content)
}

func renderPlacementDecision(m PlacementModal, title, instructions, inputLabel string) string {
	score := fmt.Sprintf("FitScore: %d/100", m.decision.FitScore)
	scoreColor := "196"
	if m.decision.FitScore >= 80 {
		scoreColor = "42"
	} else if m.decision.FitScore >= 50 {
		scoreColor = "226"
	}

	node := m.decision.Node
	if node == "" {
		node = "No suitable node"
	}

	reasoning := make([]string, 0, min(len(m.decision.Reasoning), 4))
	for i, reason := range m.decision.Reasoning {
		if i >= 4 {
			break
		}
		reasoning = append(reasoning, "  • "+reason)
	}
	return lipgloss.JoinVertical(lipgloss.Left,
		title, "", instructions, "", inputLabel, m.input.View(), "",
		lipgloss.NewStyle().Bold(true).Render("Top candidate: ")+node,
		lipgloss.NewStyle().Foreground(lipgloss.Color(scoreColor)).Render(score), "",
		lipgloss.NewStyle().Bold(true).Render("Reasoning:"),
		strings.Join(reasoning, "\n"), "",
		lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render("Press Enter to confirm selection, Escape to cancel"),
	)
}

type placementScoredMsg struct {
	Decision   *models.PlacementDecision
	Candidates []models.NodeFacts
	Error      error
}

// placeTaskCmd scores the prompt against the current daemon snapshot.
// The existing axis task place command is advisory, so confirmation here does
// not execute a task or mutate cluster state.
func placeTaskCmd(prompt string) tea.Cmd {
	return func() tea.Msg {
		snapshot, _, err := loadDaemonSnapshot()
		if err != nil {
			return placementScoredMsg{Error: err}
		}

		requirements := placement.InferRequirements(prompt)
		clusterState, stateErr := state.Load()
		if stateErr != nil && clusterState == nil {
			return placementScoredMsg{Error: stateErr}
		}

		decision := placement.SelectBestNode(requirements, snapshot.Nodes, clusterState)
		if stateErr != nil {
			decision.Reasoning = append(decision.Reasoning, "warning: cluster state could not be fully loaded: "+stateErr.Error())
		}
		return placementScoredMsg{Decision: &decision, Candidates: snapshot.Nodes}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
