package tui

import (
	"fmt"
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

// RenderLogoWithVersion returns the logo with version string.
func RenderLogoWithVersion(version string) string {
	logo := RenderLogo()
	versionLine := fmt.Sprintf("  Version: %s", version)
	versionStyled := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Render(versionLine)
	return logo + "\n" + versionStyled
}

// RenderHeaderLine returns a single-line header with logo and status.
func RenderHeaderLine(status string) string {
	left := logoSmallStyle.Render("AXIS")
	right := lipgloss.NewStyle().
		Foreground(lipgloss.Color("248")).
		Render(status)
	return lipgloss.JoinHorizontal(lipgloss.Center, left, "  |  ", right)
}
