package ui

import (
	"github.com/spf13/cobra"
)

// ApplyHelpTemplate sets a custom help template on the root command.
// Headers are bold, examples are dimmed.
func ApplyHelpTemplate(root *cobra.Command) {
	root.SetUsageTemplate(usageTpl)
}

const usageTpl = `{{bold "USAGE"}}
  {{.UseLine}}{{if .HasAvailableSubCommands}} <command> [flags]{{end}}
{{if .HasAvailableSubCommands}}
{{bold "COMMANDS"}}{{range .Commands}}{{if .IsAvailableCommand}}
  {{rpad .Name .NamePadding}}  {{.Short}}{{end}}{{end}}
{{end}}{{if .HasAvailableLocalFlags}}
{{bold "FLAGS"}}
{{.LocalFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .HasAvailableInheritedFlags}}
{{bold "GLOBAL FLAGS"}}
{{.InheritedFlags.FlagUsages | trimTrailingWhitespaces}}
{{end}}{{if .Example}}
{{bold "EXAMPLES"}}
{{dim .Example}}
{{end}}Use "{{.CommandPath}} <command> --help" for more information about a command.
`

func init() {
	cobra.AddTemplateFunc("bold", Bold)
	cobra.AddTemplateFunc("dim", Dim)
}
