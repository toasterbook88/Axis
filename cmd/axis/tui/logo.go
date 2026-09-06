package tui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// AXIS logo in ASCII art with gradient styling.
const logoASCII = `
    █████╗ ██████╗  ██████╗ ██╗   ██╗███████╗
   ██╔══██╗██╔══██╗██╔═══██╗██║   ██║██╔════╝
   ███████║██████╔╝██║   ██║██║   ██║███████╗
   ██╔══██║██╔══██╗██║   ██║██║   ██║╚════██║
   ██║  ██║██║  ██║╚██████╔╝╚██████╔╝███████║
   ╚═╝  ╚═╝╚═╝  ╚═╝ ╚═════╝  ╚═════╝ ╚══════╝
`

var (
	logoStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")).
			Bold(true)

	logoSmallStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("63")).
			Bold(true)
)

// RenderLogo returns the full ASCII logo with styling.
func RenderLogo() string {
	lines := strings.Split(strings.TrimSpace(logoASCII), "\n")
	var styled []string
	for _, line := range lines {
		styled = append(styled, logoStyle.Render(line))
	}
	return strings.Join(styled, "\n")
}

// RenderLogoSmall returns a compact one-line logo for the header.
func RenderLogoSmall() string {
	return logoSmallStyle.Render("██████╗ ██████╗ ███████╗")
}
