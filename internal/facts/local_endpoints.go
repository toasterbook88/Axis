package facts

import (
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// LocalEndpointDiscoveryScript sweeps the node's loopback listeners for model
// servers the dedicated probes do not cover (vLLM, LM Studio, a proxy, a
// second Ollama). It dials only 127.0.0.1, skips AXIS_OWNED_PORTS, probes at
// most 32 ports within 8 s (1 s per request), and asks two dialects: Ollama /api/tags, then
// OpenAI-compatible /v1/models. A listing is never a load signal.
// Marker axis-local-endpoints-v1 is the fake-executor contains key.
//
// The python heredoc avoids dollar signs, backticks, and backslashes: on a
// fish login shell the quoted <<'PY' arrives unquoted and bash expands them.
const LocalEndpointDiscoveryScript = `# axis-local-endpoints-v1
OWNED=" ${AXIS_OWNED_PORTS:-} "
if ! command -v curl >/dev/null 2>&1; then echo '{"reason":"no-curl"}'; exit 0; fi
# addr port process, one listener per line.
if command -v ss >/dev/null 2>&1; then
	LISTEN=$(ss -ltnpH 2>/dev/null | awk '{a=$4; p=a; sub(/.*:/,"",p); sub(/:[^:]*$/,"",a); pr="-"; if (match($0,/users:\(\("[^"]+"/)) pr=substr($0,RSTART+9,RLENGTH-10); print a, p, pr}')
elif command -v lsof >/dev/null 2>&1; then
	LISTEN=$(lsof +c 0 -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {a=$9; p=a; sub(/.*:/,"",p); sub(/:[^:]*$/,"",a); print a, p, $1}')
elif command -v netstat >/dev/null 2>&1; then
	LISTEN=$(netstat -an 2>/dev/null | awk '$1 ~ /^tcp/ && /LISTEN/ {a=$4; p=a; if (a ~ /:[0-9]+$/) {sub(/.*:/,"",p); sub(/:[^:]*$/,"",a)} else {sub(/.*\./,"",p); sub(/\.[^.]*$/,"",a)}; print a, p, "-"}')
else
	echo '{"reason":"no-listener-tool"}'; exit 0
fi
# Loopback or wildcard binds only; owned ports skipped. One "port bind
# process" row per port; a wildcard bind on any family wins over loopback.
PORTS=$(printf '%s\n' "$LISTEN" | awk -v owned="$OWNED" '{
	a=$1; gsub(/\[|\]/,"",a); sub(/%.*/,"",a)
	if (a ~ /^127\./ || a=="::1" || a=="localhost" || a=="::ffff:127.0.0.1") k="loopback"
	else if (a=="*" || a=="0.0.0.0" || a=="::") k="any"
	else next
	if ($2 !~ /^[0-9]+$/ || index(owned, " " $2 " ")) next
	if (!($2 in bind)) proc[$2]=$3
	if (bind[$2] != "any") bind[$2]=k
} END {
	# Usual model-server ports first, so the cap is not spent on system services.
	n = split("1234 4000 5000 5001 7860 8000 8001 8002 8008 8080 8081 8082 8090 8888 9000 11434 11435 30000", w, " ")
	for (i = 1; i <= n; i++) known[w[i]] = 1
	for (p in bind) print (p in known) ? 0 : 1, p, bind[p], proc[p]
}' | sort -n -k1,1 -k2,2 | cut -d' ' -f2-)
TRUNC=false
if [ "$(printf '%s\n' "$PORTS" | grep -c .)" -gt 32 ]; then TRUNC=true; PORTS=$(printf '%s\n' "$PORTS" | head -n 32); fi
DATA=$(mktemp 2>/dev/null || echo "/tmp/axis-ep-$$")
BODY=$(mktemp 2>/dev/null || echo "/tmp/axis-ep-body-$$")
trap 'rm -f "$DATA" "$BODY"' EXIT
: > "$DATA"
START=$SECONDS
# fetch PORT PATH: appends "@@AXIS port path code" and up to 256 KiB of body.
fetch() {
	REM=$((8 - (SECONDS - START)))
	if [ "$REM" -le 0 ]; then TRUNC=true; return 1; fi
	[ "$REM" -gt 1 ] && REM=1
	: > "$BODY"
	CODE=$(curl -s --max-time "$REM" -o "$BODY" -w '%{http_code}' "http://127.0.0.1:$1$2" 2>/dev/null)
	CODE=${CODE:-000}
	printf '@@AXIS %s %s %s\n' "$1" "$2" "$CODE" >> "$DATA"
	head -c 262144 "$BODY" >> "$DATA"
	printf '\n' >> "$DATA"
}
while read -r PORT BIND PROC; do
	[ -n "$PORT" ] || continue
	printf '@@PROC %s %s %s\n' "$PORT" "$BIND" "$PROC" >> "$DATA"
	fetch "$PORT" /api/tags || break
	# 000: not HTTP or not answering; a second request would only time out too.
	if [ "$CODE" = 000 ] || { [ "$CODE" = 200 ] && grep -q '"models"' "$BODY"; }; then continue; fi
	fetch "$PORT" /v1/models || break
done <<EOF
$PORTS
EOF
if command -v python3 >/dev/null 2>&1; then
	AXIS_EP_TRUNC="$TRUNC" python3 - "$DATA" <<'PY'
import json, os, sys

ENGINES = {"llamacpp": "llama.cpp", "vllm": "vllm"}
MAX_MODELS = 256
truncated = os.environ.get("AXIS_EP_TRUNC") == "true"
ports = {}
order = []
cur = None
with open(sys.argv[1], encoding="utf-8", errors="replace") as f:
    for line in f.read().splitlines():
        if line.startswith("@@PROC "):
            parts = line.split(" ", 3)
            p = int(parts[1])
            ports[p] = {"bind": parts[2], "process": parts[3] if len(parts) > 3 and parts[3] != "-" else "", "fetch": {}}
            order.append(p)
            cur = None
        elif line.startswith("@@AXIS "):
            _, p, path, code = line.split(" ", 3)
            cur = {"code": code, "body": []}
            ports[int(p)]["fetch"][path] = cur
        elif cur is not None:
            cur["body"].append(line)

def body(fetch):
    try:
        return json.loads(" ".join(fetch["body"]))
    except ValueError:
        return None

confirmed, candidates = [], []
for p in order:
    info = ports[p]
    got = info["fetch"]
    codes = {path: f["code"] for path, f in got.items()}
    if not codes or all(c == "000" for c in codes.values()):
        continue
    tags = got.get("/api/tags")
    doc = body(tags) if tags and tags["code"] == "200" else None
    if isinstance(doc, dict) and isinstance(doc.get("models"), list):
        out = []
        for m in doc["models"]:
            n = (m.get("name") or m.get("model")) if isinstance(m, dict) else None
            if isinstance(n, str) and n:
                e = {"name": n}
                if isinstance(m.get("remote_host"), str) and m["remote_host"]:
                    e["remote_host"] = m["remote_host"]
                out.append(e)
        out.sort(key=lambda e: e["name"])
        if len(out) > MAX_MODELS:
            truncated, out = True, out[:MAX_MODELS]
        if out:
            confirmed.append({"port": p, "bind": info["bind"], "dialect": "ollama", "engine": "ollama", "process": info["process"],
                              "models": out,
                              "provenance": {"models": "GET /api/tags 200"}})
            continue
    listing = got.get("/v1/models")
    doc = body(listing) if listing and listing["code"] == "200" else None
    if isinstance(doc, dict) and isinstance(doc.get("data"), list):
        rows = [m for m in doc["data"] if isinstance(m, dict) and isinstance(m.get("id"), str) and m.get("id")]
        owners = sorted({str(m.get("owned_by") or "") for m in rows} - {""})
        engine = ENGINES.get(owners[0], "openai-compatible") if len(owners) == 1 else "openai-compatible"
        if rows and engine == "llama.cpp":
            # A llama-server the llama.cpp probe did not match (router mode or
            # no -m): its listing is not argv-backed.
            candidates.append({"port": p, "process": info["process"], "reason": "llama.cpp-unmatched"})
            continue
        if rows:
            out = []
            if len(rows) > MAX_MODELS:
                truncated = True
            for m in sorted(rows, key=lambda m: m["id"])[:MAX_MODELS]:
                e = {"name": m["id"]}
                if isinstance(m.get("max_model_len"), int) and m["max_model_len"] > 0:
                    e["context_window"] = m["max_model_len"]
                out.append(e)
            prov = {"models": "GET /v1/models 200"}
            if owners:
                prov["engine"] = "owned_by " + ",".join(owners)
            confirmed.append({"port": p, "bind": info["bind"], "dialect": "openai", "engine": engine, "process": info["process"],
                              "models": out, "provenance": prov})
            continue
    if any(c in ("401", "403") for c in codes.values()):
        reason = "auth-required"
    else:
        reason = "no-model-listing"
    candidates.append({"port": p, "process": info["process"], "reason": reason})

print(json.dumps({"confirmed": confirmed, "candidates": candidates, "truncated": truncated}))
PY
else
	# No parser: every port that answered HTTP stays a candidate.
	awk -v trunc="$TRUNC" '
		/^@@PROC / {port=$2; proc=($4=="-") ? "" : $4; next}
		/^@@AXIS / {if ($4 != "000" && !done[port]++) {gsub(/["\\]/,"",proc); rows = rows (rows=="" ? "" : ",") "{\"port\":" port ",\"process\":\"" proc "\",\"reason\":\"parser-unavailable\"}"}}
		END {printf "{\"candidates\":[%s],\"truncated\":%s}\n", rows, trunc}' "$DATA"
fi
`

// localEndpointsCommand prefixes the sweep with the ports the dedicated
// probes already own, so their servers are not reported twice.
func localEndpointsCommand(owned []int) string {
	parts := make([]string, 0, len(owned))
	for _, p := range owned {
		parts = append(parts, strconv.Itoa(p))
	}
	return "AXIS_OWNED_PORTS='" + strings.Join(parts, " ") + "'\n" + LocalEndpointDiscoveryScript
}

// ownedModelPorts lists the ports the Ollama, llama.cpp, and MLX probes
// reported, read after those probes ran.
func ownedModelPorts(facts *models.NodeFacts) []int {
	seen := map[int]bool{}
	if facts.Ollama != nil && facts.Ollama.Installed {
		port := facts.Ollama.Port
		if port <= 0 {
			port = 11434
		}
		seen[port] = true
	}
	for _, r := range facts.ResidentModels {
		if r.Port > 0 {
			seen[r.Port] = true
		}
	}
	out := make([]int, 0, len(seen))
	for p := range seen {
		out = append(out, p)
	}
	sort.Ints(out)
	return out
}

// discoverLocalEndpoints runs the sweep through run (local bash or the SSH
// executor). A failed or unparseable run leaves the field unset: the sweep
// is best-effort and never fails a collection.
func discoverLocalEndpoints(ctx context.Context, facts *models.NodeFacts, run func(context.Context, string) (string, error)) *models.LocalEndpoints {
	if facts == nil || run == nil {
		return nil
	}
	out, err := run(ctx, localEndpointsCommand(ownedModelPorts(facts)))
	if err != nil {
		return nil
	}
	var parsed models.LocalEndpoints
	if json.Unmarshal([]byte(strings.TrimSpace(out)), &parsed) != nil {
		return nil
	}
	demoteOllamaMirrors(&parsed, facts.Ollama)
	return &parsed
}

// demoteOllamaMirrors turns an Ollama-dialect endpoint that lists exactly the
// node's own Ollama models into a candidate: the macOS Ollama app serves the
// daemon's list on a second loopback port, which is not a second engine.
func demoteOllamaMirrors(le *models.LocalEndpoints, ollama *models.OllamaInfo) {
	if ollama == nil || len(ollama.Models) == 0 {
		return
	}
	own := map[string]bool{}
	for _, m := range ollama.Models {
		own[m] = true
	}
	kept := le.Confirmed[:0]
	for _, ep := range le.Confirmed {
		mirror := ep.Dialect == "ollama" && len(ep.Models) == len(own)
		for _, m := range ep.Models {
			mirror = mirror && own[m.Name]
		}
		if mirror {
			le.Candidates = append(le.Candidates, models.LocalEndpointCandidate{Port: ep.Port, Process: ep.Process, Reason: "mirrors-ollama"})
			continue
		}
		kept = append(kept, ep)
	}
	le.Confirmed = kept
}
