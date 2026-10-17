package facts

import (
	"context"
	"encoding/json"
	"os"
	"regexp"
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
// It never dials the reserved non-HTTP service ports (sshd, Samba, databases:
// a stray request is a log line there), nor the OS ephemeral range unless the
// listener's process names a model server. A port that named no model is
// cached on the swept node for 24 h, keyed by its socket (ss inode or lsof
// device), so a restarted service is probed again; confirmed endpoints are
// probed every run.
//
// The python heredocs avoid dollar signs, backticks, and backslashes: on a
// fish login shell the quoted <<'PY' arrives unquoted and bash expands them.
const LocalEndpointDiscoveryScript = `# axis-local-endpoints-v1
OWNED=" ${AXIS_OWNED_PORTS:-} "
RESERVED=" 22 25 53 111 139 445 2049 3306 5432 6379 11211 27017 "
MODE=${AXIS_ENDPOINT_SWEEP:-allowlist}
if [ "$MODE" = off ]; then echo '{"reason":"disabled"}'; exit 0; fi
case "$MODE" in allowlist|full) ;; *) MODE=allowlist ;; esac
EXCLUDE_ENV=$(printf '%s' "${AXIS_ENDPOINT_SWEEP_EXCLUDE:-}" | tr ',' ' ')
DENY=" 3000 $EXCLUDE_ENV "
INCLUDE_ENV=$(printf '%s' "${AXIS_ENDPOINT_SWEEP_INCLUDE:-}" | tr ',' ' ')
INCLUDE=" $INCLUDE_ENV "
EXCLUDE_FILE=""
if [ -n "${AXIS_HOME:-}" ]; then EXCLUDE_FILE="$AXIS_HOME/endpoint-sweep-exclude"; elif [ -n "${HOME:-}" ]; then EXCLUDE_FILE="$HOME/.axis/endpoint-sweep-exclude"; fi
if [ -n "$EXCLUDE_FILE" ] && [ -r "$EXCLUDE_FILE" ]; then
	FILE_DENY=$(awk '{gsub(/,/," "); for (i=1;i<=NF;i++) if ($i ~ /^[0-9]+$/ && $i+0>0 && $i+0<65536) printf "%s ", $i}' "$EXCLUDE_FILE")
	DENY="$DENY $FILE_DENY"
fi
AI_FILE=""
if [ -n "${AXIS_HOME:-}" ]; then AI_FILE="$AXIS_HOME/ai.yaml"; elif [ -n "${HOME:-}" ]; then AI_FILE="$HOME/.axis/ai.yaml"; fi
AI_PORTS=""
if [ -n "$AI_FILE" ] && [ -r "$AI_FILE" ]; then AI_PORTS=$(awk 'match($0,/(base_url|advertise_url):[[:space:]]*[^#]*/) {s=substr($0,RSTART,RLENGTH); sub(/^.*https?:\/\//,"",s); sub(/[^:]*:/,"",s); sub(/[^0-9].*$/,"",s); if (s ~ /^[0-9]+$/) printf "%s ",s}' "$AI_FILE"); fi
if ! command -v curl >/dev/null 2>&1; then echo '{"reason":"no-curl"}'; exit 0; fi
# addr port key process, one listener per line; key names the socket, or -.
# A tool that is present but prints nothing (an old ss without -H support,
# say) falls through.
HAVE_TOOL=false
LISTEN=""
if command -v ss >/dev/null 2>&1; then
	HAVE_TOOL=true
	LISTEN=$(ss -ltnpe 2>/dev/null | awk '{a=$4; p=a; sub(/.*:/,"",p); sub(/:[^:]*$/,"",a); pr="-"; k="-"; if (match($0,/users:\(\("[^"]+"/)) pr=substr($0,RSTART+9,RLENGTH-10); if (match($0,/ino:[1-9][0-9]*/)) k=substr($0,RSTART+4,RLENGTH-4); print a, p, k, pr}')
fi
if [ -z "$LISTEN" ] && command -v lsof >/dev/null 2>&1; then
	HAVE_TOOL=true
	LISTEN=$(lsof -b +c 0 -nP -iTCP -sTCP:LISTEN 2>/dev/null | awk 'NR>1 {a=$(NF-1); p=a; sub(/.*:/,"",p); sub(/:[^:]*$/,"",a); print a, p, $6, $1}')
fi
if [ -z "$LISTEN" ] && command -v netstat >/dev/null 2>&1; then
	HAVE_TOOL=true
	LISTEN=$(netstat -an 2>/dev/null | awk '$1 ~ /^tcp/ && /LISTEN/ {a=$4; p=a; if (a ~ /:[0-9]+$/) {sub(/.*:/,"",p); sub(/:[^:]*$/,"",a)} else {sub(/.*\./,"",p); sub(/\.[^.]*$/,"",a)}; print a, p, "-", "-"}')
fi
if [ "$HAVE_TOOL" = false ]; then echo '{"reason":"no-listener-tool"}'; exit 0; fi
EPH=${AXIS_EPHEMERAL_PORTS:-}
if [ -z "$EPH" ] && [ -r /proc/sys/net/ipv4/ip_local_port_range ]; then EPH=$(cat /proc/sys/net/ipv4/ip_local_port_range); fi
if [ -z "$EPH" ] && command -v sysctl >/dev/null 2>&1; then EPH="$(sysctl -n net.inet.ip.portrange.first 2>/dev/null) $(sysctl -n net.inet.ip.portrange.last 2>/dev/null)"; fi
# Loopback or wildcard binds only; owned ports skipped. One "port bind key
# process" row per port; a wildcard bind on any family wins over loopback.
# Reserved ports become "port reserved" rows and the ephemeral skips one
# "0 ephemeral count" row.
PORTS=$(printf '%s\n' "$LISTEN" | awk -v owned="$OWNED" -v reserved="$RESERVED" -v eph="$EPH" -v mode="$MODE" -v deny="$DENY" -v incstr="$INCLUDE" -v ai="$AI_PORTS" 'BEGIN {
	split(eph, r, " "); lo = r[1] + 0; hi = r[2] + 0
	if (lo <= 0 || hi < lo) lo = hi = 0
	n = split("1234 4000 8000 11434 11435 30000", u, " "); for (i=1;i<=n;i++) unconditional[u[i]]=1
	n = split("5000 5001 7860 8001 8002 8008 8080 8081 8082 8090 8888 9000", c, " "); for (i=1;i<=n;i++) conditional[c[i]]=1
	split(incstr, inc, " "); for (i in inc) if (inc[i] ~ /^[0-9]+$/) included[inc[i]]=1
	split(ai, ap, " "); for (i in ap) if (ap[i] ~ /^[0-9]+$/) aip[ap[i]]=1
} {
	a=$1; gsub(/\[|\]/,"",a); sub(/%.*/,"",a)
	if (a ~ /^127\./ || a=="::1" || a=="localhost" || a=="::ffff:127.0.0.1") k="loopback"
	else if (a=="*" || a=="0.0.0.0" || a=="::") k="any"
	else next
	if ($2 !~ /^[0-9]+$/ || index(owned, " " $2 " ")) next
	if (index(reserved, " " $2 " ")) { res[$2]=1; next }
	if (index(deny, " " $2 " ") && !included[$2]) { excluded[$2]=1; next }
	if (lo && $2+0 >= lo && $2+0 <= hi && tolower($4) !~ /ollama|llama|vllm|lm.?studio|lms|litellm|mlx|kobold|tabby|sglang|lmdeploy|text-gen/) { ep[$2]=1; next }
	procname=tolower($4)
	model=(procname ~ /ollama|llama|vllm|lm.?studio|lms|litellm|mlx|kobold|tabby|sglang|lmdeploy|text-gen/)
	allowed=(mode=="full" || unconditional[$2] || included[$2] || aip[$2] || model)
	if (!allowed) { notallowed[$2]=1; next }
	if (!($2 in bind)) { proc[$2]=$4; key[$2]=$3 }
	if (bind[$2] != "any") bind[$2]=k
} END {
	# Usual model-server ports first, so the cap is not spent on system services.
	n = split("1234 4000 5000 5001 7860 8000 8001 8002 8008 8080 8081 8082 8090 8888 9000 11434 11435 30000", w, " ")
	for (i = 1; i <= n; i++) known[w[i]] = 1
	for (p in bind) { rank = 1; if (p in known) rank = 0; print rank, p, bind[p], key[p], proc[p] }
	for (p in res) print 2, p, "reserved"
	for (p in excluded) print 2, p, "excluded"
	for (p in notallowed) print 2, p, "notallowlisted"
	ne = 0; for (p in ep) if (!(p in bind)) ne++
	if (ne) print 3, 0, "ephemeral", ne
}' | sort -n -k1,1 -k2,2 | cut -d' ' -f2-)
TRUNC=false
# Cached "port key reason at" rows, from this node's own AXIS home.
CACHE=""
if [ -n "${AXIS_HOME:-}" ]; then CACHE="$AXIS_HOME/cache/local-endpoints-v1.json"; elif [ -n "${HOME:-}" ]; then CACHE="$HOME/.axis/cache/local-endpoints-v1.json"; fi
TTL=${AXIS_ENDPOINT_SWEEP_TTL:-900}
case "$TTL" in ''|*[!0-9]*) TTL=900 ;; esac
if [ "$TTL" -ne 0 ] && [ "$TTL" -lt 600 ]; then TTL=600; fi
LISTENER_HASH=""
if command -v python3 >/dev/null 2>&1; then
	LISTENER_HASH=$( { printf 'mode=%s\ndeny=%s\ninclude=%s\nai=%s\n' "$MODE" "$DENY" "$INCLUDE" "$AI_PORTS"; printf '%s\n' "$LISTEN" | awk '{a=$1;p=$2;k=$3; gsub(/\[|\]/,"",a); if ((a ~ /^127\./ || a=="::1" || a=="localhost" || a=="::ffff:127.0.0.1" || a=="*" || a=="0.0.0.0" || a=="::") && p ~ /^[0-9]+$/) print p ":" k}' | sort; } | python3 -c 'import hashlib,sys; print(hashlib.sha256(sys.stdin.buffer.read()).hexdigest())')
	if [ -n "$CACHE" ] && [ -r "$CACHE" ] && [ "$TTL" -gt 0 ]; then
		WHOLE=$(python3 - "$CACHE" "$LISTENER_HASH" "$TTL" <<'PY'
import json, sys, time
try:
    with open(sys.argv[1], encoding="utf-8") as f: doc=json.load(f)
    last=doc.get("last", {})
    if last.get("hash")==sys.argv[2] and 0 <= time.time()-float(last.get("at",0)) < int(sys.argv[3]):
        result=last.get("result")
        if isinstance(result,dict):
            result["cached_sweep"]=True
            print(json.dumps(result,separators=(",",":")))
except Exception:
    pass
PY
)
		if [ -n "$WHOLE" ]; then echo "$WHOLE"; exit 0; fi
	fi
fi
HITS=""
if [ -n "$CACHE" ] && [ -r "$CACHE" ] && command -v python3 >/dev/null 2>&1; then
	HITS=$(python3 - "$CACHE" <<'PY'
import json, sys, time
try:
    with open(sys.argv[1], encoding="utf-8") as f:
        doc = json.load(f)
    now = time.time()
    for port, e in doc.get("ports", {}).items():
        at = float(e.get("at", 0))
        if 0 <= now - at < 86400 and str(e.get("key", "-")) != "-":
            print(int(port), str(e["key"]), str(e.get("reason") or "-"), at)
except Exception:
    pass
PY
)
fi
DATA=$(mktemp 2>/dev/null) || { echo '{"reason":"no-tempfile"}'; exit 0; }
BODY=$(mktemp 2>/dev/null) || { rm -f "$DATA"; echo '{"reason":"no-tempfile"}'; exit 0; }
trap 'rm -f "$DATA" "$BODY"' EXIT
: > "$DATA"
# Record lines start with a per-run token, so a response body cannot forge one.
TOK="@@AXIS-$$-$RANDOM$RANDOM$RANDOM"
START=$SECONDS
# fetch PORT PATH: appends "TOK FETCH port path code" and up to 256 KiB of
# body. -q skips ~/.curlrc; --noproxy keeps the dial on this host; head caps
# the download while it streams. 7 s leaves a margin for SECONDS' whole-second
# resolution inside the 8 s budget.
fetch() {
	REM=$((7 - (SECONDS - START)))
	if [ "$REM" -le 0 ]; then TRUNC=true; printf '%s PARTIAL %s\n' "$TOK" "$1" >> "$DATA"; return 1; fi
	[ "$REM" -gt 1 ] && REM=1
	CODE=$( { curl -q -s --noproxy '*' --max-time "$REM" -o - -w '%{stderr}%{http_code}' "http://127.0.0.1:$1$2" | head -c 262144 > "$BODY"; } 2>&1 )
	case "$CODE" in [0-9][0-9][0-9]) ;; *) CODE=000 ;; esac
	printf '%s FETCH %s %s %s\n' "$TOK" "$1" "$2" "$CODE" >> "$DATA"
	cat "$BODY" >> "$DATA"
	printf '\n' >> "$DATA"
}
PROBED=0
STOP=false
# After the cap or the budget, keep reading rows: cached ports still report.
while read -r PORT BIND KEY PROC; do
	[ -n "$PORT" ] || continue
	case "$BIND" in
	reserved) printf '%s SKIP %s\n' "$TOK" "$PORT" >> "$DATA"; continue ;;
	excluded) printf '%s EXCLUDED %s\n' "$TOK" "$PORT" >> "$DATA"; continue ;;
	notallowlisted) printf '%s NOTALLOWLISTED %s\n' "$TOK" "$PORT" >> "$DATA"; continue ;;
	ephemeral) printf '%s EPHEMERAL %s\n' "$TOK" "$KEY" >> "$DATA"; continue ;;
	esac
	HIT=""
	if [ "$KEY" != "-" ] && [ -n "$HITS" ]; then
		HIT=$(printf '%s\n' "$HITS" | awk -v p="$PORT" -v k="$KEY" '$1 == p && $2 == k {print $3, $4; exit}')
	fi
	if [ -n "$HIT" ]; then
		printf '%s CACHED %s %s %s %s %s\n' "$TOK" "$PORT" "$BIND" "$KEY" "$HIT" "$PROC" >> "$DATA"
		continue
	fi
	[ "$STOP" = false ] || continue
	PROBED=$((PROBED + 1))
	if [ "$PROBED" -gt 32 ]; then TRUNC=true; STOP=true; continue; fi
	printf '%s PROC %s %s %s %s\n' "$TOK" "$PORT" "$BIND" "$KEY" "$PROC" >> "$DATA"
	fetch "$PORT" /api/tags || { STOP=true; continue; }
	# 000: not HTTP or not answering; a second request would only time out too.
	if [ "$CODE" = 000 ] || { [ "$CODE" = 200 ] && grep -q '"models"' "$BODY"; }; then continue; fi
	fetch "$PORT" /v1/models || STOP=true
done <<EOF
$PORTS
EOF
if command -v python3 >/dev/null 2>&1; then
	AXIS_EP_TRUNC="$TRUNC" AXIS_EP_TOK="$TOK" AXIS_EP_CACHE="$CACHE" AXIS_EP_HASH="$LISTENER_HASH" python3 - "$DATA" <<'PY'
import json, os, sys, time

ENGINES = {"llamacpp": "llama.cpp", "vllm": "vllm"}
MAX_MODELS = 256
MAX_CONTEXT = 1 << 31
TOK = os.environ["AXIS_EP_TOK"] + " "
state = {"truncated": os.environ.get("AXIS_EP_TRUNC") == "true", "ephemeral": 0, "excluded": [], "notallowlisted": 0}
ports = {}
order = []
cached, skipped, partial, cacheable = {}, [], set(), {}
cur = None
with open(sys.argv[1], encoding="utf-8", errors="replace") as f:
    for line in f.read().splitlines():
        if line.startswith(TOK + "PROC "):
            parts = line[len(TOK):].split(" ", 4)
            p = int(parts[1])
            ports[p] = {"bind": parts[2], "key": parts[3], "process": parts[4] if len(parts) > 4 and parts[4] != "-" else "", "fetch": {}}
            order.append(p)
            cur = None
        elif line.startswith(TOK + "CACHED "):
            parts = line[len(TOK):].split(" ", 6)
            cached[int(parts[1])] = {"key": parts[3], "reason": parts[4], "at": float(parts[5]),
                                     "process": parts[6] if len(parts) > 6 and parts[6] != "-" else ""}
            cur = None
        elif line.startswith(TOK + "SKIP "):
            skipped.append(int(line[len(TOK):].split(" ")[1]))
            cur = None
        elif line.startswith(TOK + "EXCLUDED "):
            state["excluded"].append(int(line[len(TOK):].split(" ")[1]))
            cur = None
        elif line.startswith(TOK + "NOTALLOWLISTED "):
            state["notallowlisted"] += 1
            cur = None
        elif line.startswith(TOK + "EPHEMERAL "):
            state["ephemeral"] = int(line[len(TOK):].split(" ")[1])
            cur = None
        elif line.startswith(TOK + "PARTIAL "):
            partial.add(int(line[len(TOK):].split(" ")[1]))
            cur = None
        elif line.startswith(TOK + "FETCH "):
            _, p, path, code = line[len(TOK):].split(" ", 3)
            cur = {"code": code, "body": []}
            ports[int(p)]["fetch"][path] = cur
        elif cur is not None:
            cur["body"].append(line)

def body(fetch):
    try:
        return json.loads(" ".join(fetch["body"]))
    except (ValueError, RecursionError):
        return None

confirmed, candidates = [], []

def classify(p):
    info = ports[p]
    got = info["fetch"]
    codes = {path: f["code"] for path, f in got.items()}
    if not codes:
        return
    if all(c == "000" for c in codes.values()):
        cacheable[p] = "-"
        return
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
            state["truncated"], out = True, out[:MAX_MODELS]
        if out:
            confirmed.append({"port": p, "bind": info["bind"], "dialect": "ollama", "engine": "ollama", "process": info["process"],
                              "models": out,
                              "provenance": {"models": "GET /api/tags 200"}})
            return
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
            return
        if rows:
            out = []
            if len(rows) > MAX_MODELS:
                state["truncated"] = True
            for m in sorted(rows, key=lambda m: m["id"])[:MAX_MODELS]:
                e = {"name": m["id"]}
                n = m.get("max_model_len")
                if type(n) is int and 0 < n < MAX_CONTEXT:
                    e["context_window"] = n
                out.append(e)
            prov = {"models": "GET /v1/models 200"}
            if owners:
                prov["engine"] = "owned_by " + ",".join(owners)
            confirmed.append({"port": p, "bind": info["bind"], "dialect": "openai", "engine": engine, "process": info["process"],
                              "models": out, "provenance": prov})
            return
    if any(c in ("401", "403") for c in codes.values()):
        reason = "auth-required"
    else:
        reason = "no-model-listing"
    cacheable[p] = reason
    candidates.append({"port": p, "process": info["process"], "reason": reason})


def save_cache(path):
    try:
        with open(path, encoding="utf-8") as f: old=json.load(f)
    except Exception:
        old={}
    keep = {str(p): {"key": c["key"], "reason": c["reason"], "at": c["at"]} for p, c in cached.items()}
    now = time.time()
    for p, reason in cacheable.items():
        if ports[p]["key"] != "-" and p not in partial:
            keep[str(p)] = {"key": ports[p]["key"], "reason": reason, "at": now}
    os.umask(0o077)
    os.makedirs(os.path.dirname(path), mode=0o700, exist_ok=True)
    tmp = path + ".tmp" + str(os.getpid())
    with open(tmp, "w", encoding="utf-8") as f:
        doc = {"v": 1, "ports": keep}
        if not out.get("truncated"):
            doc["last"] = {"at": now, "hash": os.environ.get("AXIS_EP_HASH", ""), "result": out}
        json.dump(doc, f)
    os.replace(tmp, path)


for p in order:
    try:
        classify(p)
    except Exception:
        cacheable.pop(p, None)
        candidates.append({"port": p, "process": ports[p]["process"], "reason": "parse-error"})
for p in sorted(cached):
    if cached[p]["reason"] != "-":
        candidates.append({"port": p, "process": cached[p]["process"], "reason": cached[p]["reason"], "cached": True})
out = {"confirmed": confirmed, "candidates": candidates, "truncated": state["truncated"]}
if skipped:
    out["skipped_reserved"] = sorted(skipped)
if state["ephemeral"]:
    out["skipped_ephemeral"] = state["ephemeral"]
if state["excluded"]:
    out["skipped_excluded"] = sorted(state["excluded"])
if state["notallowlisted"]:
    out["skipped_not_allowlisted"] = state["notallowlisted"]
if os.environ.get("AXIS_EP_CACHE"):
    try:
        save_cache(os.environ["AXIS_EP_CACHE"])
    except OSError:
        pass
print(json.dumps(out))
PY
else
	# No parser: every port that answered HTTP stays a candidate.
	awk -v trunc="$TRUNC" -v tok="$TOK" '
		$1 == tok && $2 == "PROC" {port=$3; proc=($6=="-") ? "" : $6; next}
		$1 == tok && $2 == "FETCH" {if ($5 != "000" && !done[port]++) {gsub(/["\\]/,"",proc); rows = rows (rows=="" ? "" : ",") "{\"port\":" port ",\"process\":\"" proc "\",\"reason\":\"parser-unavailable\"}"}}
		END {printf "{\"candidates\":[%s],\"truncated\":%s}\n", rows, trunc}' "$DATA"
fi
`

// localEndpointsCommand prefixes the sweep with the ports the dedicated
// probes already own, so their servers are not reported twice.
var endpointSweepEnvValue = regexp.MustCompile(`^[a-z]+$`)
var endpointSweepPortsValue = regexp.MustCompile(`^[0-9 ,]+$`)

func localEndpointsCommand(owned []int) string {
	parts := make([]string, 0, len(owned))
	for _, p := range owned {
		parts = append(parts, strconv.Itoa(p))
	}
	mode := os.Getenv("AXIS_ENDPOINT_SWEEP")
	if !endpointSweepEnvValue.MatchString(mode) || (mode != "allowlist" && mode != "full" && mode != "off") {
		mode = "allowlist"
	}
	clean := func(key string, re *regexp.Regexp) string {
		v := os.Getenv(key)
		if !re.MatchString(v) {
			return ""
		}
		return strings.TrimSpace(v)
	}
	exclude := clean("AXIS_ENDPOINT_SWEEP_EXCLUDE", endpointSweepPortsValue)
	include := clean("AXIS_ENDPOINT_SWEEP_INCLUDE", endpointSweepPortsValue)
	ttl := clean("AXIS_ENDPOINT_SWEEP_TTL", regexp.MustCompile(`^[0-9]+$`))
	return "AXIS_OWNED_PORTS='" + strings.Join(parts, " ") + "'\n" +
		"AXIS_ENDPOINT_SWEEP='" + mode + "'\n" +
		"AXIS_ENDPOINT_SWEEP_EXCLUDE='" + exclude + "'\n" +
		"AXIS_ENDPOINT_SWEEP_INCLUDE='" + include + "'\n" +
		"AXIS_ENDPOINT_SWEEP_TTL='" + ttl + "'\n" + LocalEndpointDiscoveryScript
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
