package console

import "strings"

// WiredSlashes are the slash commands the REPL actually dispatches.
// The palette must not advertise a name that has no handler.
var WiredSlashes = []string{
	"/plan", "/todo", "/diff", "/undo", "/compact", "/autonomy",
	"/tools", "/mcp", "/context", "/help", "/exit", "/quit",
	"/fleet", "/export", "/facts", "/cluster", "/nodes",
	"/reservations", "/skills", "/models", "/model", "/clear", "/history",
}

// SlashPaletteText is the one-screen list shown when the operator submits /.
func SlashPaletteText() string {
	return "commands: " + strings.Join(WiredSlashes, " ")
}

// KeymapText is the one-screen keymap shown when the operator submits ?.
func KeymapText() string {
	return "esc stop turn   ctrl-c twice quit   y/n only when boxed   enter expands the last tool cell   /last expands it too   enter does not approve   / commands   ? keymap"
}
