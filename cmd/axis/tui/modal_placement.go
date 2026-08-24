package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/toasterbook88/axis/internal/models"
)

// PlacementModal implements an interactive workload placement wizard.
type PlacementModal struct {
	// Input state
	input    textinput.Model
	loading  bool
	placed   bool
	cancelled bool

	// Results
	decision     *models.PlacementDecision
	candidates   []models.NodeFacts
	scoringError error

	// Dimensions
	width  int
	height int
}

// NewPlacementModal creates an initialized placement wizard modal.
func NewPlacementModal() PlacementModal {
	ti := textinput.New()
	ti.Placeholder = "e.g., run 16GB VRAM model inference"
	ti.PromptStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("63"))
	ti.PlaceholderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 50

	return PlacementModal{
		input:  ti,
		width:  60,
		height: 20,
	}
}

// Init initializes the modal.
func (m PlacementModal) Init() tea.Cmd {
	return textinput.Blink
}

// Update handles events and returns updated model + commands.
func (m PlacementModal) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = min(msg.Width-10, 80)
		m.height = min(msg.Height-10, 30)
		m.input.Width = m.width - 10
		return m, nil

	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc":
			m.cancelled = true
			return m, nil

		case "enter":
			if m.loading || m.placed {
				return m, nil
			}
			if m.input.Value() == "" {
				return m, nil
			}
			// Start placement scoring
			m.loading = true
			return m, placeTaskCmd(m.input.Value())

		case "tab":
			// Cycle through candidates if available
			if len(m.candidates) > 0 {
				// Future: cycle selection
			}
		}

	case placementScoredMsg:
		m.loading = false
		m.decision = msg.Decision
		m.candidates = msg.Candidates
		m.scoringError = msg.Error
		return m, nil

	case placementPlacedMsg:
		m.loading = false
		m.placed = true
		return m, nil
	}

	// Update text input
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View renders the modal overlay.
func (m PlacementModal) View() string {
	var b strings.Builder

	// Modal border
	border := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("63")).
		Padding(1, 2)

	// Title
	title := lipgloss.NewStyle().
		Bold(true).
		Foreground(lipgloss.Color("229")).
		Render("📍 Workload Placement Wizard")

	// Instructions
	instructions := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Render("Enter your workload requirements below")

	// Input field
	inputLabel := lipgloss.NewStyle().
		Foreground(lipgloss.Color("63")).
		Render("Requirements:")

	var content string

	if m.loading {
		content = lipgloss.JoinVertical(lipgloss.Center,
			title,
			"",
			instructions,
			"",
			inputLabel,
			m.input.View(),
			"",
			lipgloss.NewStyle().
				Foreground(lipgloss.Color("226")).
				Render("⏳ Analyzing cluster capacity..."),
		)
	} else if m.placed {
		content = lipgloss.JoinVertical(lipgloss.Center,
			title,
			"",
			lipgloss.NewStyle().
				Foreground(lipgloss.Color("42")).
				Render("✅ Task placed successfully!"),
			"",
			lipgloss.NewStyle().
				Foreground(lipgloss.Color("248")).
				Render("Press Escape to close"),
		)
	} else if m.decision != nil {
		// Show scoring results
		scoreStr := fmt.Sprintf("FitScore: %d/100", m.decision.FitScore)
		if m.decision.FitScore >= 80 {
			scoreStr = lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(scoreStr)
		} else if m.decision.FitScore >= 50 {
			scoreStr = lipgloss.NewStyle().Foreground(lipgloss.Color("226")).Render(scoreStr)
		} else {
			scoreStr = lipgloss.NewStyle().Foreground(lipgloss.Color("196")).Render(scoreStr)
		}

		selectedNode := "None selected"
		if m.decision.Node != "" {
			selectedNode = m.decision.Node
		}

		content = lipgloss.JoinVertical(lipgloss.Left,
			title,
			"",
			instructions,
			"",
			inputLabel,
			m.input.View(),
			"",
			lipgloss.NewStyle().Bold(true).Render("Top Candidate:"),
			fmt.Sprintf("  Node: %s", selectedNode),
			fmt.Sprintf("  %s", scoreStr),
			"",
			lipgloss.NewStyle().Foreground(lipgloss.Color("248")).Render("Press Enter to confirm, Escape to cancel"),
		)
	} else {
		content = lipgloss.JoinVertical(lipgloss.Center,
			title,
			"",
			instructions,
			"",
			inputLabel,
			m.input.View(),
			"",
			lipgloss.NewStyle().
				Foreground(lipgloss.Color("248")).
				Render("Examples:"),
			"  • run ollama inference on a 7b model",
			"  • build go project with 8GB RAM",
			"  • run 16GB VRAM model inference",
		)
	}

	b.WriteString(border.Render(content))
	return b.String()
}

// placementScoredMsg carries placement scoring results.
type placementScoredMsg struct {
	Decision   *models.PlacementDecision
	Candidates []models.NodeFacts
	Error      error
}

// placementPlacedMsg confirms task placement.
type placementPlacedMsg struct{}

// placeTaskCmd runs placement scoring asynchronously.
func placeTaskCmd(prompt string) tea.Cmd {
	return func() tea.Msg {
		// TODO: Wire to actual placement engine
		// For now, return mock results
		decision := &models.PlacementDecision{
			Node:     "cranium",
			FitScore: 85,
			Reasoning: []string{"Sufficient VRAM", "Low thermal pressure"},
			OK:       true,
		}

		candidates := []models.NodeFacts{
			{Name: "cranium", Role: "primary"},
			{Name: "cachyos", Role: "worker"},
		}

		return placementScoredMsg{
			Decision:   decision,
			Candidates: candidates,
			Error:      nil,
		}
	}
}

// min returns the smaller of two integers.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
