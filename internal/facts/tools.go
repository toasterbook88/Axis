package facts

import (
	"context"
	"os/exec"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

var (
	lookPathTool          = exec.LookPath
	runToolVersionCommand = exec.CommandContext
)

type ollamaDiscoveryPayload struct {
	models.OllamaInfo
	ResidentModels []models.ResidentModel `json:"resident_models,omitempty"`
}

// withResidentPort stamps the runtime's listening port onto each resident
// model. The discovery scripts report the server port at the top level of the
// payload rather than per-model, so callers copy it down here. A model that
// already carries an explicit port is left untouched.
func withResidentPort(rms []models.ResidentModel, port int) []models.ResidentModel {
	if port <= 0 {
		return rms
	}
	for i := range rms {
		if rms[i].Port == 0 {
			rms[i].Port = port
		}
	}
	return rms
}

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
		# 'ollama ps -qq' (added in Ollama 0.3.10) emits JSON: each entry
		# includes name, expires_at (RFC3339) and size_vram. Parse it
		# with python3 (always present on nodes with ollama) and emit
		# one JSON object per model. Falls back to the existing awk
		# parser when the JSON is unavailable (older Ollama).
		PS_JSON=$($OLLAMA_BIN ps -qq 2>/dev/null || echo "")
		if [ -n "$PS_JSON" ]; then
			RESIDENT=$(printf '%s' "$PS_JSON" | python3 - 2>/dev/null <<'PYEOF' || echo ""
import json, sys
try:
    data = json.loads(sys.stdin.read() or "[]")
    entries = data.get("models", data) if isinstance(data, dict) else data
    if not isinstance(entries, list):
        entries = []
    out = []
    for e in entries:
        if not isinstance(e, dict):
            continue
        name = e.get("name", "")
        if not name:
            continue
        vram = e.get("size_vram")
        try:
            vram_val = int(vram) if vram is not None else 0
        except (ValueError, TypeError):
            vram_val = 0
        out.append(json.dumps({
            "name": name,
            "runtime": "ollama",
            "processor": e.get("processor", "gpu"),
            "size_vram_mb": vram_val // (1024*1024),
            "source": "ollama-ps",
            "expires_at": e.get("expires_at", ""),
        }))
    print(",".join(out))
except Exception:
    sys.exit(0)
PYEOF
)
		else
			RESIDENT=""
		fi
		if [ -z "$RESIDENT" ]; then
			# Fallback: original awk parser (older Ollama, no 'ps -qq').
			# No expires_at field is emitted by this path; the local
			# parser will leave ExpiresAt zero and WarmthScore at 0.
			RESIDENT=$($OLLAMA_BIN ps 2>/dev/null | awk 'NR>1 && NF { proc=""; size_mb=0; for(i=1;i<=NF;i++){if($i~/[0-9]+%/){proc=$i" "$(i+1)} if(($i=="GB"||$i=="GiB")&&i>1&&($(i-1)+0)>0){size_mb=int($(i-1)*1024+0.5)} if(($i=="MB"||$i=="MiB")&&i>1&&($(i-1)+0)>0){size_mb=int($(i-1)+0.5)}} gsub(/"/, "\\\"", proc); printf "%s{\"name\":\"%s\",\"runtime\":\"ollama\",\"processor\":\"%s\",\"size_vram_mb\":%d,\"source\":\"ollama-ps\"}", (n++ ? "," : ""), $1, proc, size_mb }')
		fi
		if [ -n "$RESIDENT" ]; then
			RESIDENT="[$RESIDENT]"
		else
			RESIDENT="[]"
		fi
		# Process-level default_keep_alive (added in Ollama 0.3.10). Read
		# from /api/ps; tolerate older Ollama (or versions that omit
		# the field) by emitting an empty string. Treat null and any
		# failure to parse as empty.
		KEEPALIVE=$(curl -s --max-time 2 http://127.0.0.1:11434/api/ps 2>/dev/null | python3 -c "import sys,json; d=json.load(sys.stdin); v=d.get('default_keep_alive'); print('' if v is None else v)" 2>/dev/null || echo "")
		echo "{\"installed\":true,\"path\":\"$OLLAMA_BIN\",\"version\":\"${VERSION:-unknown}\",\"running\":$( [ -n \"$PGREP\" ] && echo true || echo false ),\"listening\":$LISTENING,\"port\":11434,\"models\":$MODELS,\"resident_models\":$RESIDENT,\"gpu_offload\":\"${GPU:-none}\",\"default_keep_alive\":\"${KEEPALIVE}\"}"
	`

// LlamaServerDiscoveryScript is the bash script used to detect a running
// llama-server process, extract its loaded model from the command line, and
// report it as a resident model. Works locally and over SSH.
//
// pgrep is trimmed to a single PID with | head -1 to handle multiple instances
// deterministically. The --model/-m flag is parsed with awk to handle both
// --model=path and --model path forms and avoid fragile column assumptions.
const LlamaServerDiscoveryScript = `set -o pipefail;
		LSBIN=$(command -v llama-server || echo "")
		if [ -z "$LSBIN" ]; then echo '{"installed":false}'; exit 0; fi
		VERSION=$($LSBIN --version 2>/dev/null | head -1)
		PGREP=$(pgrep -x llama-server 2>/dev/null | head -1 || pgrep -f llama-server 2>/dev/null | head -1 || echo "")
		RUNNING=false
		[ -n "$PGREP" ] && RUNNING=true
		LISTENING=false
		if command -v lsof >/dev/null 2>&1 && lsof -i :8080 2>/dev/null | grep -q LISTEN; then
			LISTENING=true
		elif command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ':8080 '; then
			LISTENING=true
		elif command -v netstat >/dev/null 2>&1 && netstat -ltn 2>/dev/null | grep -q ':8080 '; then
			LISTENING=true
		fi
		RESIDENT="[]"
		if [ -n "$PGREP" ]; then
			CMDLINE=$(ps -p "$PGREP" -o args= 2>/dev/null || tr '\0' ' ' < /proc/"$PGREP"/cmdline 2>/dev/null || echo "")
			MODEL=$(echo "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--model"||$i=="-m"){print $(i+1);exit}if($i~/^(--model=|-m=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			if [ -n "$MODEL" ]; then
				MNAME=$(basename "$MODEL" | sed 's/\.[^.]*$//')
				GPU_LAYERS=$(echo "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--n-gpu-layers"||$i=="-ngl"){print $(i+1);exit}if($i~/^(--n-gpu-layers=|-ngl=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
				PROC="cpu"
				[ -n "$GPU_LAYERS" ] && [ "$GPU_LAYERS" -gt 0 ] 2>/dev/null && PROC="gpu"
				SIZE_BYTES=$(stat -f%z "$MODEL" 2>/dev/null || stat -c%s "$MODEL" 2>/dev/null || echo 0)
				SIZE_MB=$((SIZE_BYTES / 1048576))
				MNAME_ESC=$(echo "$MNAME" | sed 's/"/\\"/g')
				RESIDENT="[{\"name\":\"$MNAME_ESC\",\"runtime\":\"llama.cpp\",\"processor\":\"$PROC\",\"size_vram_mb\":$SIZE_MB,\"source\":\"llama-server-ps\"}]"
			fi
		fi
		echo "{\"installed\":true,\"path\":\"$LSBIN\",\"version\":\"${VERSION:-unknown}\",\"running\":$RUNNING,\"listening\":$LISTENING,\"port\":8080,\"resident_models\":$RESIDENT}"
	`

type llamaServerDiscoveryPayload struct {
	Installed      bool                   `json:"installed"`
	Path           string                 `json:"path,omitempty"`
	Version        string                 `json:"version,omitempty"`
	Running        bool                   `json:"running,omitempty"`
	Listening      bool                   `json:"listening,omitempty"`
	Port           int                    `json:"port,omitempty"`
	ResidentModels []models.ResidentModel `json:"resident_models,omitempty"`
}

// MLXDiscoveryScript detects a running mlx_lm.server process and queries its
// OpenAI-compatible /v1/models endpoint to enumerate resident models.
//
// mlx_lm is a Python package (pip install mlx-lm), so python3 is used for JSON
// parsing — it is always present on any node that can run MLX. The server
// defaults to port 8080; the script respects an explicit --port argument.
//
// Note: llama-server also defaults to port 8080. On nodes running both, only
// the first server to bind the port will be reachable; the other probe will
// return an empty resident-model list rather than an error.
const MLXDiscoveryScript = `set -o pipefail;
		MLX_OK=false
		if command -v mlx_lm >/dev/null 2>&1; then
			MLX_OK=true
		elif python3 -c "import mlx_lm" 2>/dev/null; then
			MLX_OK=true
		fi
		if [ "$MLX_OK" = "false" ]; then echo '{"installed":false}'; exit 0; fi
		# Bracket trick: [m]lx_lm.server matches the process mlx_lm.server but not
		# the pgrep command's own cmdline (which contains the literal "[m]lx_lm.server").
		PGREP=$(pgrep -f "[m]lx_lm.server" 2>/dev/null | head -1 || pgrep -f "[m]lx_lm server" 2>/dev/null | head -1 || echo "")
		RUNNING=false
		[ -n "$PGREP" ] && RUNNING=true
		PORT=8080
		if [ -n "$PGREP" ]; then
			CMDLINE=$(ps -p "$PGREP" -o args= 2>/dev/null || tr '\0' ' ' < /proc/"$PGREP"/cmdline 2>/dev/null || echo "")
			PORT_ARG=$(echo "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--port"){print $(i+1);exit}if($i~/^--port=/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			# Validate PORT_ARG is numeric before accepting it.
			if printf '%s' "$PORT_ARG" | grep -qE '^[0-9]+$'; then PORT="$PORT_ARG"; fi
		fi
		RSS_KB=$(ps -o rss= -p "$PGREP" 2>/dev/null || echo 0)
		SIZE_MB=$((RSS_KB / 1024))
		RESIDENT="[]"
		if [ "$RUNNING" = "true" ] && command -v curl >/dev/null 2>&1; then
			RESP=$(curl -s --max-time 2 "http://localhost:$PORT/v1/models" 2>/dev/null || echo "")
			if [ -n "$RESP" ]; then
				RESIDENT=$(echo "$RESP" | python3 -c "
import sys, json
try:
    d = json.load(sys.stdin)
    items = []
    for m in d.get('data', []):
        mid = m.get('id', '')
        if not mid:
            continue
        name = mid.split('/')[-1]
        items.append({'name': name, 'runtime': 'mlx', 'processor': 'gpu', 'size_vram_mb': $SIZE_MB, 'source': 'mlx-lm-api'})
    print(json.dumps(items))
except Exception:
    print('[]')
" 2>/dev/null || echo "[]")
			fi
		fi
		echo "{\"installed\":true,\"running\":$RUNNING,\"port\":$PORT,\"resident_models\":$RESIDENT}"
	`

type mlxDiscoveryPayload struct {
	Installed      bool                   `json:"installed"`
	Running        bool                   `json:"running,omitempty"`
	Port           int                    `json:"port,omitempty"`
	ResidentModels []models.ResidentModel `json:"resident_models,omitempty"`
}

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
		{name: "jq", class: models.ToolClassRuntime, versionCmd: "jq --version"},
		{name: "nix", class: models.ToolClassRuntime, versionCmd: "nix --version"},
		{name: "docker", class: models.ToolClassContainer, versionCmd: "docker --version"},
		{name: "ollama", class: models.ToolClassAICLI, versionCmd: "ollama --version"},
		{name: "mlx_lm", class: models.ToolClassAICLI, versionCmd: "mlx_lm --help"},
		{name: "llama-cli", class: models.ToolClassAICLI, versionCmd: "llama-cli --version"},
		{name: "llama-server", class: models.ToolClassAICLI, versionCmd: "llama-server --version"},
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
		path, err := lookPathTool(td.name)
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
			if out, err := runToolVersionCommand(ctx, parts[0], parts[1:]...).Output(); err == nil {
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
	line := raw
	if idx := strings.IndexByte(raw, '\n'); idx != -1 {
		line = raw[:idx]
	}
	line = strings.TrimSpace(line)

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
