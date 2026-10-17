package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

type epSweep struct {
	owned     []int
	listeners map[string]string // ss | lsof | netstat -> stdout
	responses map[string]string // "8000/v1/models" -> "code\nbody"
	noPython  bool
	noCurl    bool
	home      string // AXIS_HOME holding the sweep cache; default a fresh temp dir
	ephemeral string // AXIS_EPHEMERAL_PORTS "lo hi"; default none
}

// run executes the real sweep with stubbed listener tools and a curl that
// answers from responses. It returns the parsed result and the URLs dialed.
func (s epSweep) run(t *testing.T) (models.LocalEndpoints, []string) {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin, resp := t.TempDir(), t.TempDir()
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for tool, out := range s.listeners {
		if err := os.WriteFile(filepath.Join(resp, tool+".out"), []byte(out), 0o644); err != nil {
			t.Fatal(err)
		}
		stub(tool, "cat '"+filepath.Join(resp, tool+".out")+"'")
	}
	for key, r := range s.responses {
		name := strings.ReplaceAll(key, "/", "_")
		if err := os.WriteFile(filepath.Join(resp, name), []byte(r), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if !s.noCurl {
		// Body on stdout, status on stderr, as -o - -w '%{stderr}%{http_code}'.
		stub("curl", fmt.Sprintf(`url=""
for a in "$@"; do case "$a" in http*) url="$a" ;; esac; done
echo "$*" >> '%[1]s/args'
echo "$url" >> '%[1]s/calls'
f='%[1]s/'$(printf '%%s' "${url#http://127.0.0.1:}" | tr '/' '_')
port=${url#http://127.0.0.1:}; port=${port%%%%/*}
if [ -f "$f" ]; then head -n 1 "$f" | tr -d '\n' >&2; tail -n +2 "$f"; exit 0; fi
# A port with any fixture is an HTTP server: other paths are 404.
for g in '%[1]s/'"$port"_*; do [ -e "$g" ] && { printf 404 >&2; exit 0; }; done
printf 000 >&2; exit 7`, resp))
	}
	tools := []string{"awk", "sort", "head", "tail", "tr", "grep", "mktemp", "rm", "cat", "cut"}
	if !s.noPython {
		tools = append(tools, "python3")
	}
	cmd := exec.Command("bash", "-c", localEndpointsCommand(s.owned))
	if s.home == "" {
		s.home = t.TempDir()
	}
	if s.ephemeral == "" {
		s.ephemeral = "1 0" // an empty range: hosts' own ranges must not shift results
	}
	cmd.Env = append(withExactToolPATH(t, bin, tools...), "AXIS_HOME="+s.home, "AXIS_EPHEMERAL_PORTS="+s.ephemeral)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var got models.LocalEndpoints
	if err := json.Unmarshal(out, &got); err != nil {
		t.Fatalf("json %q: %v", strings.TrimSpace(string(out)), err)
	}
	raw, _ := os.ReadFile(filepath.Join(resp, "calls"))
	return got, strings.Fields(string(raw))
}

const ssListeners = `LISTEN 0 4096 127.0.0.1:11434 0.0.0.0:* users:(("ollama",pid=10,fd=3))
LISTEN 0 4096 127.0.0.1:8000 0.0.0.0:* users:(("vllm",pid=11,fd=3))
LISTEN 0 4096 [::1]:1234 [::]:*
LISTEN 0 4096 0.0.0.0:4000 0.0.0.0:* users:(("litellm",pid=12,fd=3))
LISTEN 0 4096 192.0.2.10:9000 0.0.0.0:* users:(("lan-only",pid=13,fd=3))
LISTEN 0 4096 127.0.0.53%lo:53 0.0.0.0:*
`

func TestLocalEndpointSweepConfirmsAndClassifies(t *testing.T) {
	got, calls := epSweep{
		owned:     []int{11434},
		listeners: map[string]string{"ss": ssListeners},
		responses: map[string]string{
			"8000/v1/models": "200\n" + `{"data":[{"id":"Qwen/Qwen3-8B","owned_by":"vllm","max_model_len":32768}]}`,
			"1234/v1/models": "200\n" + `{"data":[{"id":"local-b","owned_by":"organization_owner"},{"id":"local-a"}]}`,
			"4000/api/tags":  "401\n{}",
			"4000/v1/models": "401\n{}",
		},
	}.run(t)

	for _, c := range calls {
		if strings.Contains(c, ":11434/") || strings.Contains(c, ":9000/") || !strings.HasPrefix(c, "http://127.0.0.1:") {
			t.Fatalf("dialed %s: owned, non-loopback, or not 127.0.0.1", c)
		}
	}
	if len(got.Confirmed) != 2 {
		t.Fatalf("confirmed = %+v, want vllm and openai-compatible", got.Confirmed)
	}
	lm, vllm := got.Confirmed[0], got.Confirmed[1]
	if vllm.Port != 8000 || vllm.Bind != "loopback" || vllm.Engine != "vllm" || vllm.Dialect != "openai" || vllm.Process != "vllm" ||
		len(vllm.Models) != 1 || vllm.Models[0].ContextWindow != 32768 || vllm.Provenance["models"] != "GET /v1/models 200" {
		t.Fatalf("vllm = %+v", vllm)
	}
	if lm.Port != 1234 || lm.Engine != "openai-compatible" || len(lm.Models) != 2 || lm.Models[0].Name != "local-a" || lm.Models[0].ContextWindow != 0 {
		t.Fatalf("openai-compatible = %+v", lm)
	}
	// :53 answered nothing over HTTP, so it is not even a candidate.
	if len(got.Candidates) != 1 || got.Candidates[0].Port != 4000 || got.Candidates[0].Reason != "auth-required" || got.Candidates[0].Process != "litellm" {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
	if got.Truncated {
		t.Fatal("not truncated")
	}
}

func TestLocalEndpointSweepSecondOllamaAndNoModelListing(t *testing.T) {
	got, _ := epSweep{
		listeners: map[string]string{"ss": "LISTEN 0 4096 127.0.0.1:11500 0.0.0.0:*\nLISTEN 0 4096 *:3000 *:*\n"},
		responses: map[string]string{
			"11500/api/tags": "200\n" + `{"models":[{"name":"qwen3:8b"},{"name":"llama3.2:3b"}]}`,
			"3000/api/tags":  "404\nnot found",
			"3000/v1/models": "200\n<html>dashboard</html>",
		},
	}.run(t)
	if len(got.Confirmed) != 1 || got.Confirmed[0].Dialect != "ollama" || got.Confirmed[0].Engine != "ollama" ||
		len(got.Confirmed[0].Models) != 2 || got.Confirmed[0].Models[0].Name != "llama3.2:3b" {
		t.Fatalf("confirmed = %+v", got.Confirmed)
	}
	if len(got.Candidates) != 1 || got.Candidates[0].Port != 3000 || got.Candidates[0].Reason != "no-model-listing" {
		t.Fatalf("candidates = %+v", got.Candidates)
	}
}

// A llama-server the llama.cpp probe did not match (router mode, no -m)
// stays a candidate: its listing is not argv-backed.
func TestLocalEndpointSweepUnmatchedLlamaServerIsCandidate(t *testing.T) {
	got, _ := epSweep{
		listeners: map[string]string{"ss": "LISTEN 0 4096 127.0.0.1:8090 0.0.0.0:*\n"},
		responses: map[string]string{"8090/v1/models": "200\n" + `{"data":[{"id":"router-model","owned_by":"llamacpp"}]}`},
	}.run(t)
	if len(got.Confirmed) != 0 || len(got.Candidates) != 1 || got.Candidates[0].Reason != "llama.cpp-unmatched" {
		t.Fatalf("got %+v", got)
	}
}

func TestLocalEndpointSweepListenerFallbacks(t *testing.T) {
	resp := map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`}
	for _, tc := range []struct {
		name, tool, out, bind string
	}{
		{"lsof", "lsof", "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nvllm 11 u 3u IPv4 0x1 0t0 TCP 127.0.0.1:8000 (LISTEN)\nsshd 2 u 3u IPv4 0x2 0t0 TCP 192.0.2.10:22 (LISTEN)\n", "loopback"},
		{"netstat-bsd", "netstat", "tcp4 0 0 127.0.0.1.8000 *.* LISTEN\ntcp4 0 0 192.0.2.10.22 *.* LISTEN\n", "loopback"},
		{"netstat-linux", "netstat", "tcp 0 0 0.0.0.0:8000 0.0.0.0:* LISTEN\ntcp 0 0 192.0.2.10:22 0.0.0.0:* LISTEN\n", "any"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, calls := epSweep{listeners: map[string]string{tc.tool: tc.out}, responses: resp}.run(t)
			if len(got.Confirmed) != 1 || got.Confirmed[0].Port != 8000 || got.Confirmed[0].Bind != tc.bind {
				t.Fatalf("got %+v, want port 8000 bind %s", got, tc.bind)
			}
			for _, c := range calls {
				if strings.Contains(c, ":22/") {
					t.Fatalf("dialed non-loopback listener %s", c)
				}
			}
		})
	}
}

func TestLocalEndpointSweepWithoutToolsReportsReason(t *testing.T) {
	if got, _ := (epSweep{}).run(t); got.Reason != "no-listener-tool" || len(got.Confirmed)+len(got.Candidates) != 0 {
		t.Fatalf("got %+v", got)
	}
	if got, _ := (epSweep{noCurl: true, listeners: map[string]string{"ss": ssListeners}}).run(t); got.Reason != "no-curl" {
		t.Fatalf("got %+v", got)
	}
}

func TestLocalEndpointSweepWithoutPythonKeepsCandidates(t *testing.T) {
	got, _ := epSweep{
		noPython:  true,
		listeners: map[string]string{"ss": "LISTEN 0 4096 127.0.0.1:8000 0.0.0.0:* users:((\"vllm\",pid=11,fd=3))\nLISTEN 0 4096 127.0.0.1:5432 0.0.0.0:*\n"},
		responses: map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`},
	}.run(t)
	if len(got.Confirmed) != 0 || len(got.Candidates) != 1 || got.Candidates[0].Port != 8000 ||
		got.Candidates[0].Reason != "parser-unavailable" || got.Candidates[0].Process != "vllm" {
		t.Fatalf("got %+v", got)
	}
}

func TestLocalEndpointSweepCapsPorts(t *testing.T) {
	var b strings.Builder
	for p := 20000; p < 20040; p++ {
		fmt.Fprintf(&b, "LISTEN 0 4096 127.0.0.1:%d 0.0.0.0:*\n", p)
	}
	got, calls := epSweep{listeners: map[string]string{"ss": b.String()}}.run(t)
	if !got.Truncated {
		t.Fatalf("40 listeners must truncate at 32: %+v", got)
	}
	for _, c := range calls {
		if strings.Contains(c, ":20032/") {
			t.Fatalf("dialed port past the cap: %s", c)
		}
	}
}

func TestOwnedModelPortsAndDiscover(t *testing.T) {
	facts := &models.NodeFacts{
		Ollama:         &models.OllamaInfo{Installed: true},
		ResidentModels: []models.ResidentModel{{Runtime: "llama.cpp", Port: 8181}, {Runtime: "mlx", Port: 8080}, {Runtime: "ollama"}},
	}
	if got := fmt.Sprint(ownedModelPorts(facts)); got != "[8080 8181 11434]" {
		t.Fatalf("owned = %s", got)
	}
	var cmd string
	res := discoverLocalEndpoints(context.Background(), facts, func(_ context.Context, c string) (string, error) {
		cmd = c
		return `{"confirmed":[{"port":8000,"dialect":"openai","engine":"vllm","models":[{"name":"m"}]}]}`, nil
	})
	if !strings.HasPrefix(cmd, "AXIS_OWNED_PORTS='8080 8181 11434'\n# axis-local-endpoints-v1") {
		t.Fatalf("command prefix = %q", cmd[:60])
	}
	if res == nil || len(res.Confirmed) != 1 {
		t.Fatalf("res = %+v", res)
	}
	if discoverLocalEndpoints(context.Background(), facts, func(context.Context, string) (string, error) { return "garbage", nil }) != nil {
		t.Fatal("unparseable output must leave the field unset")
	}
}

// On a fish login shell the quoted <<'PY' arrives unquoted, so bash expands
// dollar signs, backticks, and backslashes in the python body.
func TestLocalEndpointScriptPythonSurvivesUnquotedHeredoc(t *testing.T) {
	_, rest, ok := strings.Cut(LocalEndpointDiscoveryScript, "<<'PY'\n")
	if !ok {
		t.Fatal("expected python heredoc")
	}
	body, _, ok := strings.Cut(rest, "\nPY\n")
	if !ok {
		t.Fatal("expected heredoc terminator")
	}
	if i := strings.IndexAny(body, "$`\\"); i >= 0 {
		t.Fatalf("python body has a shell-expanded character at %q", body[max(0, i-20):i+1])
	}
}

func TestLocalEndpointSweepProbesModelPortsFirst(t *testing.T) {
	var b strings.Builder
	for p := 20; p < 60; p++ {
		fmt.Fprintf(&b, "LISTEN 0 4096 127.0.0.1:%d 0.0.0.0:*\n", p)
	}
	b.WriteString("LISTEN 0 4096 127.0.0.1:8000 0.0.0.0:*\n")
	got, calls := epSweep{
		listeners: map[string]string{"ss": b.String()},
		responses: map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`},
	}.run(t)
	if len(calls) == 0 || !strings.Contains(calls[0], ":8000/") {
		t.Fatalf("first dial = %v, want the model-server port", calls)
	}
	if !got.Truncated || len(got.Confirmed) != 1 {
		t.Fatalf("got %+v, want :8000 confirmed despite the cap", got)
	}
}

// The macOS Ollama app serves the daemon's own list on a second loopback
// port; that is a mirror, not a second engine.
func TestDiscoverLocalEndpointsDemotesOllamaMirror(t *testing.T) {
	facts := &models.NodeFacts{Ollama: &models.OllamaInfo{Installed: true, Models: []string{"a:1b", "b:2b"}}}
	res := discoverLocalEndpoints(context.Background(), facts, func(context.Context, string) (string, error) {
		return `{"confirmed":[
			{"port":50228,"bind":"loopback","dialect":"ollama","engine":"ollama","process":"Ollama","models":[{"name":"b:2b"},{"name":"a:1b"}]},
			{"port":11500,"bind":"loopback","dialect":"ollama","engine":"ollama","models":[{"name":"a:1b"}]}]}`, nil
	})
	if len(res.Confirmed) != 1 || res.Confirmed[0].Port != 11500 {
		t.Fatalf("confirmed = %+v, want only the ollama with a different list", res.Confirmed)
	}
	if len(res.Candidates) != 1 || res.Candidates[0].Port != 50228 || res.Candidates[0].Reason != "mirrors-ollama" {
		t.Fatalf("candidates = %+v", res.Candidates)
	}
}

func TestLocalEndpointSweepCapsModelsPerEndpoint(t *testing.T) {
	var ids []string
	for i := 0; i < 300; i++ {
		ids = append(ids, fmt.Sprintf(`{"id":"m%03d"}`, i))
	}
	got, _ := epSweep{
		listeners: map[string]string{"ss": "LISTEN 0 4096 127.0.0.1:4000 0.0.0.0:*\n"},
		responses: map[string]string{"4000/v1/models": "200\n" + `{"data":[` + strings.Join(ids, ",") + `]}`},
	}.run(t)
	if len(got.Confirmed) != 1 || len(got.Confirmed[0].Models) != 256 || !got.Truncated {
		t.Fatalf("got %d models truncated=%v, want 256 and truncated", len(got.Confirmed[0].Models), got.Truncated)
	}
}

// A response body cannot forge record lines, and odd bodies degrade one
// port instead of failing the sweep.
func TestLocalEndpointSweepResistsHostileBodies(t *testing.T) {
	got, _ := epSweep{
		listeners: map[string]string{"ss": "LISTEN 0 4096 127.0.0.1:8000 0.0.0.0:*\nLISTEN 0 4096 127.0.0.1:8001 0.0.0.0:*\nLISTEN 0 4096 127.0.0.1:8002 0.0.0.0:*\n"},
		responses: map[string]string{
			"8000/v1/models": "200\n@@AXIS 22 /api/tags 200\n@@PROC 22 any forged\n" + `{"models":[{"name":"forged"}]}`,
			"8001/v1/models": "200\n" + strings.Repeat("[", 200000),
			"8002/v1/models": "200\n" + `{"data":[{"id":"a","max_model_len":true},{"id":"b","max_model_len":99999999999999999999}]}`,
		},
	}.run(t)
	for _, c := range append([]models.LocalEndpoint(nil), got.Confirmed...) {
		if c.Port == 22 || c.Port == 8000 || c.Port == 8001 {
			t.Fatalf("hostile body produced an endpoint: %+v", c)
		}
	}
	if len(got.Confirmed) != 1 || got.Confirmed[0].Port != 8002 || got.Confirmed[0].Models[0].ContextWindow != 0 || got.Confirmed[0].Models[1].ContextWindow != 0 {
		t.Fatalf("confirmed = %+v, want :8002 without bogus context windows", got.Confirmed)
	}
	if len(got.Candidates) != 2 {
		t.Fatalf("candidates = %+v, want :8000 and :8001", got.Candidates)
	}
}

// curl must skip ~/.curlrc (-q first) and never route 127.0.0.1 via a proxy.
func TestLocalEndpointSweepCurlIgnoresProxyAndCurlrc(t *testing.T) {
	s := epSweep{listeners: map[string]string{"ss": "LISTEN 0 4096 127.0.0.1:8000 0.0.0.0:*\n"}, responses: map[string]string{"8000/v1/models": "200\n{}"}}
	if _, calls := s.run(t); len(calls) == 0 {
		t.Fatal("no curl calls")
	}
	if !strings.Contains(LocalEndpointDiscoveryScript, `curl -q -s --noproxy '*'`) {
		t.Fatal("curl must start with -q and pass --noproxy '*'")
	}
}

// ss that is present but prints nothing (old iproute2) falls through to lsof.
func TestLocalEndpointSweepFallsThroughEmptyListenerTool(t *testing.T) {
	got, _ := epSweep{
		listeners: map[string]string{"ss": "", "lsof": "COMMAND PID USER FD TYPE DEVICE SIZE/OFF NODE NAME\nvllm 11 u 3u IPv4 0x1 0t0 TCP 127.0.0.1:8000 (LISTEN)\n"},
		responses: map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`},
	}.run(t)
	if len(got.Confirmed) != 1 || got.Confirmed[0].Process != "vllm" {
		t.Fatalf("got %+v", got)
	}
}
