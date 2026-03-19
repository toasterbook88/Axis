package facts

import (
	"context"
	"os/exec"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// OllamaDiscoveryScript is the bash script used to robustly discover Ollama state
// and models across both local and remote nodes.
const OllamaDiscoveryScript = `set -o pipefail;
		OLLAMA_BIN=$(command -v ollama || echo "/usr/local/bin/ollama /opt/ollama/ollama ~/.ollama/bin/ollama" | tr ' ' '\n' | while read p; do [ -x "$p" ] && echo "$p" && break; done)
		if [ -z "$OLLAMA_BIN" ]; then echo '{"installed":false}'; exit 0; fi
		VERSION=$($OLLAMA_BIN --version 2>/dev/null | head -1)
		PGREP=$(pgrep -f "$OLLAMA_BIN" || echo "")
		MODELS=$($OLLAMA_BIN list 2>/dev/null | tail -n +2 | awk 'NF { printf "%s\"%s\"", (n++ ? "," : ""), $1 }')
		if [ -n "$MODELS" ]; then
			MODELS="[$MODELS]"
		else
			MODELS="[]"
		fi
		LISTENING=false
		if command -v lsof >/dev/null 2>&1 && lsof -i :11434 2>/dev/null | grep -q LISTEN; then
			LISTENING=true
		elif command -v netstat >/dev/null 2>&1 && netstat -ltn 2>/dev/null | grep -q ':11434 '; then
			LISTENING=true
		elif command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ':11434 '; then
			LISTENING=true
		fi
		GPU=$($OLLAMA_BIN ps 2>/dev/null | grep -o 'gpu:[^ ]*' | head -1)
		echo "{\"installed\":true,\"path\":\"$OLLAMA_BIN\",\"version\":\"${VERSION:-unknown}\",\"running\":$( [ -n \"$PGREP\" ] && echo true || echo false ),\"listening\":$LISTENING,\"port\":11434,\"models\":$MODELS,\"gpu_offload\":\"${GPU:-none}\"}"
	`

// toolDef defines a tool to probe during discovery.
type toolDef struct {
	name       string
	class      models.ToolClass
	versionCmd string // command to get version, empty if none
}

// defaultToolDefs returns the tightly scoped set of tools to detect in Phase 1.
func defaultToolDefs() []toolDef {
	return []toolDef{
		{name: "go", class: models.ToolClassBuild, versionCmd: "go version"},
		{name: "python3", class: models.ToolClassRuntime, versionCmd: "python3 --version"},
		{name: "git", class: models.ToolClassVCS, versionCmd: "git --version"},
		{name: "docker", class: models.ToolClassContainer, versionCmd: "docker --version"},
		{name: "ollama", class: models.ToolClassAICLI, versionCmd: "ollama --version"},
		{name: "node", class: models.ToolClassRuntime, versionCmd: "node --version"},
		{name: "swift", class: models.ToolClassBuild, versionCmd: "swift --version"},
		{name: "cargo", class: models.ToolClassBuild, versionCmd: "cargo --version"},
		{name: "gcc", class: models.ToolClassBuild, versionCmd: "gcc --version"},
	}
}

// DiscoverTools probes for installed tools on the local machine.
// Silent failure allowed — missing tools are simply not reported.
func DiscoverTools(ctx context.Context) []models.ToolInfo {
	defs := defaultToolDefs()
	var tools []models.ToolInfo

	for _, td := range defs {
		path, err := exec.LookPath(td.name)
		if err != nil {
			continue
		}

		ti := models.ToolInfo{
			Name:  td.name,
			Path:  path,
			Class: td.class,
		}

		if td.versionCmd != "" {
			parts := strings.Fields(td.versionCmd)
			if out, err := exec.CommandContext(ctx, parts[0], parts[1:]...).Output(); err == nil {
				ti.Version = parseVersionString(string(out))
			}
		}

		tools = append(tools, ti)
	}
	return tools
}

// parseVersionString extracts a clean version from command output.
// Handles formats like "go version go1.24.1 darwin/arm64", "Python 3.11.0",
// "git version 2.39.5", "v20.11.0", etc.
func parseVersionString(raw string) string {
	line := strings.TrimSpace(strings.Split(raw, "\n")[0])

	// Try to find a version-like token (starts with digit or v+digit)
	for _, field := range strings.Fields(line) {
		clean := strings.TrimPrefix(field, "v")
		clean = strings.TrimPrefix(clean, "go")
		if len(clean) > 0 && clean[0] >= '0' && clean[0] <= '9' {
			return strings.TrimRight(clean, ",;")
		}
	}
	return line
}
