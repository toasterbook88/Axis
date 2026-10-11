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
		# Match the daemon by process name first (precise), then fall back to
		# a bracketed cmdline pattern. The bracket keeps the probe's own
		# bash -c argv -- which embeds this script text -- from matching
		# itself; head -1 collapses multi-instance output to one PID; the
		# numeric guard rejects any non-PID line so the value can never
		# word-split a later test.
		PGREP=$(pgrep -x ollama 2>/dev/null | head -1 || true)
		if [ -z "$PGREP" ]; then PGREP=$(pgrep -f '[o]llama serve' 2>/dev/null | head -1 || true); fi
		case "$PGREP" in ''|*[!0-9]*) PGREP="";; esac
		RUNNING=false
		[ -n "$PGREP" ] && RUNNING=true
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
		# Installed-model catalog from this node's own Ollama API. Cloud
		# proxies carry remote_host; /api/show capabilities separate
		# embedding models from chat models. Bounded to 64 models and a 6s
		# capability budget; entries past the budget keep no capabilities.
		# interlinked-ignore: ubs_hardcoded_localhost — the node probes its own loopback Ollama API, same as KEEPALIVE above
		OLLAMA_API=http://127.0.0.1:11434
		CATALOG=$(curl -s --max-time 2 "$OLLAMA_API/api/tags" 2>/dev/null | AXIS_OLLAMA_API="$OLLAMA_API" python3 -c "
import json, os, subprocess, sys, time
api = os.environ.get('AXIS_OLLAMA_API', '')
try:
    tags = json.load(sys.stdin).get('models') or []
except Exception:
    sys.exit(0)
deadline = time.monotonic() + 6
out = []
for m in tags[:64]:
    name = m.get('name') or ''
    if not name:
        continue
    d = m.get('details') or {}
    e = {'name': name}
    for k, v in (('remote_host', m.get('remote_host')), ('remote_model', m.get('remote_model')), ('family', d.get('family')), ('parameter_size', d.get('parameter_size')), ('quantization', d.get('quantization_level'))):
        if isinstance(v, str) and v:
            e[k] = v
    if isinstance(m.get('size'), int) and m['size'] > 0:
        e['size_bytes'] = m['size']
    if api and time.monotonic() < deadline:
        try:
            r = subprocess.run(['curl', '-s', '--max-time', '2', '-d', json.dumps({'model': name}), api + '/api/show'], capture_output=True, timeout=3)
            caps = json.loads(r.stdout or b'{}').get('capabilities')
            if isinstance(caps, list):
                e['capabilities'] = [c for c in caps if isinstance(c, str)]
        except Exception:
            pass
    out.append(e)
print(json.dumps(out))
" 2>/dev/null || echo "")
		[ -n "$CATALOG" ] || CATALOG="[]"
		echo "{\"installed\":true,\"path\":\"$OLLAMA_BIN\",\"version\":\"${VERSION:-unknown}\",\"running\":$RUNNING,\"listening\":$LISTENING,\"port\":11434,\"models\":$MODELS,\"resident_models\":$RESIDENT,\"gpu_offload\":\"${GPU:-none}\",\"default_keep_alive\":\"${KEEPALIVE}\",\"catalog\":$CATALOG}"
	`

// LlamaServerDiscoveryScript is the bash script used to detect running
// llama-server processes, extract each loaded model from that process's
// command line, and report one resident model per PID. Works locally and
// over SSH.
//
// A GPU index is recorded only when nvidia-smi maps that PID to a device,
// or, when that row is missing, when the argv carries --main-gpu / -mg.
// A missing observation is omitted. It is not reported as GPU 0.
// --model/-m and --port/-p accept both --flag=value and --flag value.
// Port defaults to 8080 when a process does not set one. The payload's
// top-level port is the first resident's port.
const LlamaServerDiscoveryScript = `set -o pipefail;
		RAW_PIDS=$(pgrep -x llama-server 2>/dev/null || true)
		if [ -z "$RAW_PIDS" ]; then RAW_PIDS=$(pgrep -f '[l]lama-server' 2>/dev/null || true); fi
		PIDS=""
		for _pid in $RAW_PIDS; do
			case "$_pid" in ''|*[!0-9]*) continue;; esac
			# pgrep -f also matches any shell whose command line carries this
			# script; only a process whose argv[0] is llama-server counts.
			_argv0=$(ps -p "$_pid" -o args= 2>/dev/null | awk '{print $1; exit}')
			case "${_argv0##*/}" in llama-server*) ;; *) continue;; esac
			PIDS="$PIDS $_pid"
		done
		PIDS=$(echo "$PIDS" | awk '{$1=$1; print}')
		FIRST=$(echo "$PIDS" | awk '{print $1}')
		LSBIN=$(command -v llama-server || echo "")
		if [ -z "$LSBIN" ] && [ -n "$FIRST" ]; then
			LSBIN=$(readlink /proc/"$FIRST"/exe 2>/dev/null || echo "")
		fi
		if [ -z "$LSBIN" ] && [ -n "$FIRST" ]; then
			LSBIN=$(ps -p "$FIRST" -o args= 2>/dev/null | awk '{print $1; exit}' || echo "")
		fi
		if [ -z "$LSBIN" ]; then echo '{"installed":false}'; exit 0; fi
		VERSION=unknown
		if [ -x "$LSBIN" ]; then VERSION=$("$LSBIN" --version 2>/dev/null | head -1); fi
		RUNNING=false
		[ -n "$PIDS" ] && RUNNING=true
		GPU_TABLE=""
		APP_TABLE=""
		if command -v nvidia-smi >/dev/null 2>&1; then
			GPU_TABLE=$(nvidia-smi --query-gpu=index,uuid --format=csv,noheader,nounits 2>/dev/null || true)
			APP_TABLE=$(nvidia-smi --query-compute-apps=gpu_uuid,pid --format=csv,noheader,nounits 2>/dev/null || true)
		fi
		axis_gpu_indices_for_pid() {
			_pid=$1
			_cmd=$2
			_idx=$(printf '%s\n__AXIS_APPS__\n%s\n' "$GPU_TABLE" "$APP_TABLE" | awk -F, -v pid="$_pid" '
				function trim(s) { gsub(/^[ \t]+|[ \t]+$/, "", s); return s }
				$0 == "__AXIS_APPS__" { phase = 1; next }
				phase == 0 {
					idx = trim($1); uuid = trim($2)
					if (idx ~ /^[0-9]+$/ && uuid != "") u2i[uuid] = idx
					next
				}
				{
					uuid = trim($1); p = trim($2)
					if (p == pid && (uuid in u2i)) {
						idx = u2i[uuid]
						if (!(idx in seen)) { seen[idx] = 1; list = (list == "" ? idx : list "," idx) }
					}
				}
				END { print list }
			')
			if [ -z "$_idx" ]; then
				_idx=$(printf '%s\n' "$_cmd" | awk '{for(i=1;i<=NF;i++){if($i=="--main-gpu"||$i=="-mg"){print $(i+1);exit}if($i~/^(--main-gpu=|-mg=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			fi
			if printf '%s' "$_idx" | grep -qE '^[0-9]+(,[0-9]+)*$'; then
				printf '%s' "$_idx"
			fi
		}
		RESIDENT_ITEMS=""
		PORT=8080
		for PGREP in $PIDS; do
			CMDLINE=$(ps -p "$PGREP" -o args= 2>/dev/null || tr '\0' ' ' < /proc/"$PGREP"/cmdline 2>/dev/null || echo "")
			PROCESS_OWNER=$(ps -p "$PGREP" -o user= 2>/dev/null | awk '{$1=$1; print}' || echo "")
			PROCESS_START_TOKEN=$(ps -p "$PGREP" -o lstart= 2>/dev/null | awk '{$1=$1; print}' || echo "")
			THIS_PORT=8080
			PORT_ARG=$(printf '%s\n' "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--port"||$i=="-p"){print $(i+1);exit}if($i~/^(--port=|-p=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			if printf '%s' "$PORT_ARG" | grep -qE '^[0-9]+$'; then THIS_PORT="$PORT_ARG"; fi
			MODEL=$(printf '%s\n' "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--model"||$i=="-m"){print $(i+1);exit}if($i~/^(--model=|-m=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			if [ -z "$MODEL" ]; then continue; fi
			SUPERVISOR="none"
			SUPERVISOR_UNIT=""
			CGROUP_DATA=$(cat /proc/"$PGREP"/cgroup 2>/dev/null || echo "")
			if [ -n "$CGROUP_DATA" ]; then
				if echo "$CGROUP_DATA" | grep -q "user@"; then
					SUPERVISOR="systemd-user"
					SUPERVISOR_UNIT=$(echo "$CGROUP_DATA" | grep -oE '[^/:]+\.service' | awk 'END {print}' || echo "")
				elif echo "$CGROUP_DATA" | grep -q "system.slice"; then
					SUPERVISOR="systemd-system"
					SUPERVISOR_UNIT=$(echo "$CGROUP_DATA" | grep -oE '[^/:]+\.service' | awk 'END {print}' || echo "")
				fi
			fi
			MNAME=$(basename "$MODEL" | sed 's/\.[^.]*$//')
			GPU_LAYERS=$(printf '%s\n' "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--n-gpu-layers"||$i=="-ngl"){print $(i+1);exit}if($i~/^(--n-gpu-layers=|-ngl=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			PROC="cpu"
			[ -n "$GPU_LAYERS" ] && [ "$GPU_LAYERS" -gt 0 ] 2>/dev/null && PROC="gpu"
			SIZE_BYTES=$(stat -f%z "$MODEL" 2>/dev/null || stat -c%s "$MODEL" 2>/dev/null || echo 0)
			SIZE_MB=$((SIZE_BYTES / 1048576))
			STATE_JSON=""
			CTX_JSON=""
			if command -v curl >/dev/null 2>&1; then
				# /health and /v1/health are the only endpoints llama-server
				# exempts from --api-key; 200 means the model finished loading.
				HEALTH=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$THIS_PORT/health" 2>/dev/null || true)
				case "$HEALTH" in
					200) STATE="loaded"; LOAD_SIGNAL="health-ok" ;;
					503) STATE="loading"; LOAD_SIGNAL="health-loading" ;;
					*) STATE="down"; LOAD_SIGNAL="health-silent"; HEALTH="${HEALTH:-000}" ;;
				esac
				PROV="\"state\":\"GET /health $HEALTH\""
				if [ "$STATE" = "loaded" ]; then
					N_CTX=$(curl -s --max-time 2 "http://127.0.0.1:$THIS_PORT/props" 2>/dev/null | grep -o '"n_ctx":[0-9]*' | head -1 | sed 's/.*://')
					if printf '%s' "$N_CTX" | grep -qE '^[0-9]+$'; then
						CTX_JSON=",\"context_window\":$N_CTX"
						PROV="$PROV,\"context_window\":\"GET /props default_generation_settings.n_ctx\""
					fi
					SERVED=$(curl -s --max-time 2 "http://127.0.0.1:$THIS_PORT/v1/models" 2>/dev/null | grep -o '"id":"[^"]*"' | head -1 | sed 's/^"id":"//; s/"$//')
					ALIAS=$(printf '%s\n' "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--alias"||$i=="-a"){print $(i+1);exit}if($i~/^(--alias=|-a=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
					if [ -n "$SERVED" ] && ! case ",$ALIAS," in *",$SERVED,"*) true ;; *) false ;; esac; then
						SERVED_NAME=$(basename "$SERVED" | sed 's/\.[^.]*$//')
						if [ "$SERVED_NAME" != "$MNAME" ]; then
							STATE="listed"; LOAD_SIGNAL="served-id-differs"; MNAME="$SERVED_NAME"
							PROV="$PROV,\"name\":\"GET /v1/models data[0].id\""
						fi
					fi
				fi
				STATE_JSON=",\"state\":\"$STATE\",\"load_signal\":\"$LOAD_SIGNAL\",\"provenance\":{$PROV}"
			fi
			MNAME_ESC=${MNAME//\\/\\\\}; MNAME_ESC=${MNAME_ESC//\"/\\\"}
			LSBIN_ESC=$(echo "$LSBIN" | sed 's/\\/\\\\/g; s/"/\\"/g')
			PROCESS_OWNER_ESC=$(echo "$PROCESS_OWNER" | sed 's/\\/\\\\/g; s/"/\\"/g')
			PROCESS_START_TOKEN_ESC=$(echo "$PROCESS_START_TOKEN" | sed 's/\\/\\\\/g; s/"/\\"/g')
			SUPERVISOR_ESC=$(echo "$SUPERVISOR" | sed 's/"/\\"/g')
			SUPERVISOR_UNIT_ESC=$(echo "$SUPERVISOR_UNIT" | sed 's/"/\\"/g')
			GPU_JSON=""
			GPU_IDX=$(axis_gpu_indices_for_pid "$PGREP" "$CMDLINE")
			if [ -n "$GPU_IDX" ]; then GPU_JSON=",\"gpu_indices\":[$GPU_IDX]"; fi
			if [ -z "$RESIDENT_ITEMS" ]; then PORT="$THIS_PORT"; fi
			ITEM="{\"name\":\"$MNAME_ESC\",\"runtime\":\"llama.cpp\",\"processor\":\"$PROC\",\"weight_size_mb\":$SIZE_MB,\"pid\":$PGREP,\"port\":$THIS_PORT,\"executable\":\"$LSBIN_ESC\",\"process_owner\":\"$PROCESS_OWNER_ESC\",\"process_start_token\":\"$PROCESS_START_TOKEN_ESC\",\"supervisor_type\":\"$SUPERVISOR_ESC\",\"supervisor_unit\":\"$SUPERVISOR_UNIT_ESC\",\"source\":\"llama-server-ps\"$STATE_JSON$CTX_JSON$GPU_JSON}"
			if [ -n "$RESIDENT_ITEMS" ]; then
				RESIDENT_ITEMS="$RESIDENT_ITEMS,$ITEM"
			else
				RESIDENT_ITEMS="$ITEM"
			fi
		done
		if [ -n "$RESIDENT_ITEMS" ]; then RESIDENT="[$RESIDENT_ITEMS]"; else RESIDENT="[]"; fi
		LISTENING=false
		if command -v lsof >/dev/null 2>&1 && lsof -i :"$PORT" 2>/dev/null | grep -q LISTEN; then
			LISTENING=true
		elif command -v ss >/dev/null 2>&1 && ss -ltn 2>/dev/null | grep -q ":$PORT "; then
			LISTENING=true
		elif command -v netstat >/dev/null 2>&1 && netstat -ltn 2>/dev/null | grep -q ":$PORT "; then
			LISTENING=true
		fi
		echo "{\"installed\":true,\"path\":\"$LSBIN\",\"version\":\"${VERSION:-unknown}\",\"running\":$RUNNING,\"listening\":$LISTENING,\"port\":$PORT,\"resident_models\":$RESIDENT}"
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

// MLXDiscoveryScript detects a running mlx_lm.server process and publishes the
// model on its command line (--model / -m) as the one resident. Its
// /v1/models lists the Hugging Face cache, so it is read only for liveness:
// an answer is listed, silence is down. MLX gives no load signal.
// The server defaults to port 8080; the script respects an explicit --port
// argument.
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
		# pgrep -f also matches any shell whose command line carries this
		# script, so a candidate counts only when argv[0] is the mlx_lm.server
		# entry point or a python interpreter whose arguments name it.
		PGREP=""
		for _pid in $(pgrep -f "[m]lx_lm.server" 2>/dev/null; pgrep -f "[m]lx_lm server" 2>/dev/null); do
			case "$_pid" in ''|*[!0-9]*) continue;; esac
			if ps -p "$_pid" -o args= 2>/dev/null | awk 'NR == 1 {
				n = split($1, p, "/"); a0 = p[n]
				if (a0 ~ /^mlx_lm\.server/) ok = 1
				if (a0 == "mlx_lm" && $2 == "server") ok = 1
				if (a0 ~ /^[Pp]ython/) for (i = 2; i <= NF; i++) if ($i == "mlx_lm.server" || ($i == "mlx_lm" && $(i+1) == "server")) ok = 1
			} END { exit !ok }'; then PGREP="$_pid"; break; fi
		done
		RUNNING=false
		[ -n "$PGREP" ] && RUNNING=true
		PORT=8080
		EXECUTABLE=""
		PROCESS_START_TOKEN=""
		if [ -n "$PGREP" ]; then
			CMDLINE=$(ps -p "$PGREP" -o args= 2>/dev/null || tr '\0' ' ' < /proc/"$PGREP"/cmdline 2>/dev/null || echo "")
			EXECUTABLE=$(echo "$CMDLINE" | awk '{print $1; exit}')
			PROCESS_START_TOKEN=$(ps -p "$PGREP" -o lstart= 2>/dev/null | awk '{$1=$1; print}' || echo "")
			PORT_ARG=$(echo "$CMDLINE" | awk '{for(i=1;i<=NF;i++){if($i=="--port"){print $(i+1);exit}if($i~/^--port=/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
			# Validate PORT_ARG is numeric before accepting it.
			if printf '%s' "$PORT_ARG" | grep -qE '^[0-9]+$'; then PORT="$PORT_ARG"; fi
		fi
		RSS_KB=$(ps -o rss= -p "$PGREP" 2>/dev/null || echo 0)
		SIZE_MB=$((RSS_KB / 1024))
		RESIDENT="[]"
		axis_json_esc() { _v=${1//\\/\\\\}; printf '%s' "${_v//\"/\\\"}"; }
		# /v1/models lists the Hugging Face cache, not the loaded model; the
		# served model is the one on the command line. Never a load signal.
		# Flags are read after the server token: "python -m mlx_lm.server"
		# uses -m for the module.
		MODEL_ARG=$(echo "$CMDLINE" | awk '{s=0; for(i=1;i<=NF;i++){n=split($i,p,"/"); if(p[n]~/^mlx_lm\.server/||(p[n]=="server"&&i>1&&$(i-1)~/(^|\/)mlx_lm$/)){s=i;break}} for(i=s+1;i<=NF;i++){if($i=="--model"||$i=="-m"){print $(i+1);exit}if($i~/^(--model=|-m=)/){sub(/^[^=]*=/,"",$i);print $i;exit}}}')
		if [ "$RUNNING" = "true" ] && [ -n "$MODEL_ARG" ]; then
			MNAME="${MODEL_ARG%/}"; MNAME="${MNAME##*/}"
			STATE_JSON=""
			if command -v curl >/dev/null 2>&1; then
				HTTP=$(curl -s -o /dev/null -w '%{http_code}' --max-time 2 "http://127.0.0.1:$PORT/v1/models" 2>/dev/null || true)
				case "$HTTP" in 2*) STATE="listed" ;; *) STATE="down"; HTTP="${HTTP:-000}" ;; esac
				STATE_JSON=",\"state\":\"$STATE\",\"load_signal\":\"none\",\"provenance\":{\"name\":\"argv --model\",\"state\":\"GET /v1/models $HTTP\"}"
			fi
			RESIDENT="[{\"name\":\"$(axis_json_esc "$MNAME")\",\"runtime\":\"mlx\",\"processor\":\"gpu\",\"size_ram_mb\":$SIZE_MB,\"source\":\"mlx-argv\",\"pid\":$PGREP,\"executable\":\"$(axis_json_esc "$EXECUTABLE")\",\"process_start_token\":\"$(axis_json_esc "$PROCESS_START_TOKEN")\"$STATE_JSON}]"
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
		{name: "mlx_lm.server", class: models.ToolClassAICLI, versionCmd: "mlx_lm.server --help"},
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
