package facts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// ssRow is one `ss -ltnpe` LISTEN line with an optional process and inode.
func ssRow(addr, proc, ino string) string {
	row := "LISTEN 0 4096 " + addr + " 0.0.0.0:*"
	if proc != "" {
		row += ` users:(("` + proc + `",pid=11,fd=3))`
	}
	if ino != "" {
		row += " uid:1000 ino:" + ino + " sk:1"
	}
	return row + "\n"
}

func dialedPort(calls []string, port string) bool {
	return slices.ContainsFunc(calls, func(c string) bool { return strings.Contains(c, ":"+port+"/") })
}

func TestLocalEndpointSweepNeverDialsReservedPorts(t *testing.T) {
	got, calls := epSweep{
		listeners: map[string]string{"ss": ssRow("0.0.0.0:22", "", "5") + ssRow("0.0.0.0:445", "", "6") +
			ssRow("127.0.0.1:5432", "", "7") + ssRow("127.0.0.1:8000", "vllm", "8")},
		responses: map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`},
	}.run(t)
	for _, p := range []string{"22", "445", "5432"} {
		if dialedPort(calls, p) {
			t.Fatalf("dialed reserved port %s: %v", p, calls)
		}
	}
	if !slices.Equal(got.SkippedReserved, []int{22, 445, 5432}) || len(got.Confirmed) != 1 {
		t.Fatalf("got %+v", got)
	}
}

func TestLocalEndpointSweepSkipsEphemeralUnlessModelProcess(t *testing.T) {
	got, calls := epSweep{
		ephemeral: "40000 40999",
		listeners: map[string]string{"ss": ssRow("127.0.0.1:40001", "IDE Helper", "1") + ssRow("127.0.0.1:40002", "", "2") +
			ssRow("127.0.0.1:40003", "ollama", "3")},
		responses: map[string]string{"40003/api/tags": "200\n" + `{"models":[{"name":"qwen3:8b"}]}`},
	}.run(t)
	if dialedPort(calls, "40001") || dialedPort(calls, "40002") {
		t.Fatalf("dialed an ephemeral listener with no model process: %v", calls)
	}
	if len(got.Confirmed) != 1 || got.Confirmed[0].Port != 40003 || got.SkippedEphemeral != 2 {
		t.Fatalf("got %+v", got)
	}
}

func TestLocalEndpointSweepCachesNonModelPortsPerSocket(t *testing.T) {
	home := t.TempDir()
	resp := map[string]string{
		"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`,
		"9999/api/tags":  "404\n",
		"4000/api/tags":  "401\n{}",
		"4000/v1/models": "401\n{}",
	}
	listeners := func(ino9999 string) map[string]string {
		return map[string]string{"ss": ssRow("127.0.0.1:8000", "vllm", "8") + ssRow("127.0.0.1:9999", "", ino9999) +
			ssRow("127.0.0.1:4000", "litellm", "40") + ssRow("127.0.0.1:7777", "", "77")}
	}

	first, calls := epSweep{home: home, ttl: "0", listeners: listeners("99"), responses: resp}.run(t)
	for _, p := range []string{"8000", "9999", "4000", "7777"} {
		if !dialedPort(calls, p) {
			t.Fatalf("first sweep must probe %s: %v", p, calls)
		}
	}
	if len(first.Confirmed) != 1 || len(first.Candidates) != 2 {
		t.Fatalf("first = %+v", first)
	}
	dir := filepath.Join(home, "cache")
	if fi, err := os.Stat(dir); err != nil || fi.Mode().Perm() != 0o700 {
		t.Fatalf("cache dir: %v %v", fi, err)
	}
	if fi, err := os.Stat(filepath.Join(dir, "local-endpoints-v1.json")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("cache file: %v %v", fi, err)
	}

	// Same sockets: only the confirmed model server is dialed again, and the
	// cached candidates are still reported, marked cached.
	second, calls := epSweep{home: home, ttl: "0", listeners: listeners("99"), responses: resp}.run(t)
	if len(calls) == 0 || slices.ContainsFunc(calls, func(c string) bool { return !strings.Contains(c, ":8000/") }) {
		t.Fatalf("second sweep dialed %v, want only :8000", calls)
	}
	if len(second.Confirmed) != 1 || len(second.Candidates) != 2 {
		t.Fatalf("second = %+v", second)
	}
	for _, c := range second.Candidates {
		if !c.Cached || (c.Port == 4000) != (c.Reason == "auth-required" && c.Process == "litellm") {
			t.Fatalf("cached candidate %+v", c)
		}
	}

	// A restarted service has a new socket: it is probed again.
	_, calls = epSweep{home: home, ttl: "0", listeners: listeners("100"), responses: resp}.run(t)
	if !dialedPort(calls, "9999") || dialedPort(calls, "7777") || dialedPort(calls, "4000") {
		t.Fatalf("after a socket change dialed %v, want :9999 and :8000 only", calls)
	}
}

func TestLocalEndpointSweepNeverProbes3000(t *testing.T) {
	for _, mode := range []string{"allowlist", "full", ""} {
		for _, bind := range []string{"0.0.0.0", "127.0.0.1"} {
			for _, process := range []string{"node", "", "next-server"} {
				t.Run(mode+"/"+bind+"/"+process, func(t *testing.T) {
					got, calls := epSweep{mode: mode, listeners: map[string]string{"ss": ssRow(bind+":3000", process, "30")}}.run(t)
					if dialedPort(calls, "3000") || !slices.Contains(got.SkippedExcluded, 3000) {
						t.Fatalf("result=%+v calls=%v", got, calls)
					}
				})
			}
		}
	}
	got, calls := epSweep{mode: "full", include: "3000", listeners: map[string]string{"ss": ssRow("127.0.0.1:3000", "node", "30")}}.run(t)
	if !dialedPort(calls, "3000") || slices.Contains(got.SkippedExcluded, 3000) {
		t.Fatalf("explicit include did not override deny: %+v %v", got, calls)
	}
}

func TestLocalEndpointSweepCacheExpiresAndNeedsSocketKey(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	stale, _ := json.Marshal(map[string]any{"v": 1, "ports": map[string]any{"7777": map[string]any{"key": "77", "reason": "-", "at": 1}}})
	if err := os.WriteFile(filepath.Join(home, "cache", "local-endpoints-v1.json"), stale, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, calls := (epSweep{home: home, ttl: "0", listeners: map[string]string{"ss": ssRow("127.0.0.1:7777", "", "77")}}).run(t); !dialedPort(calls, "7777") {
		t.Fatalf("an entry older than 24 h must be probed again: %v", calls)
	}

	// netstat names no socket, so nothing it lists is ever served from cache.
	ns := epSweep{home: t.TempDir(), ttl: "0", listeners: map[string]string{"netstat": "tcp 0 0 127.0.0.1:7777 0.0.0.0:* LISTEN\n"}}
	ns.run(t)
	if _, calls := ns.run(t); !dialedPort(calls, "7777") {
		t.Fatalf("a listener without a socket key must not be cached: %v", calls)
	}
}

func TestLocalEndpointSweepCorruptCacheIsIgnored(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "cache"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, "cache", "local-endpoints-v1.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, calls := epSweep{
		home:      home,
		listeners: map[string]string{"ss": ssRow("127.0.0.1:8000", "vllm", "8")},
		responses: map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`},
	}.run(t)
	if len(got.Confirmed) != 1 || !dialedPort(calls, "8000") {
		t.Fatalf("got %+v calls %v", got, calls)
	}
}

func TestLocalEndpointSweepExcludeEnvAndFile(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "endpoint-sweep-exclude"), []byte("9001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	got, calls := epSweep{home: home, mode: "full", exclude: "8000,4000", listeners: map[string]string{"ss": ssRow("127.0.0.1:8000", "vllm", "8") + ssRow("127.0.0.1:4000", "litellm", "4") + ssRow("127.0.0.1:9001", "node", "9")}}.run(t)
	if len(calls) != 0 || !slices.Equal(got.SkippedExcluded, []int{4000, 8000, 9001}) {
		t.Fatalf("calls=%v result=%+v", calls, got)
	}
	t.Setenv("AXIS_ENDPOINT_SWEEP", "full")
	t.Setenv("AXIS_ENDPOINT_SWEEP_EXCLUDE", "8000;rm")
	cmd := localEndpointsCommand(nil)
	if !strings.Contains(cmd, "AXIS_ENDPOINT_SWEEP_EXCLUDE=''\n") {
		t.Fatalf("unsafe exclude was not dropped: %q", cmd[:strings.Index(cmd, "# axis-local-endpoints-v1")])
	}
}

func TestLocalEndpointSweepAiYamlPorts(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, "ai.yaml"), []byte("endpoint:\n  base_url: http://127.0.0.1:8083/v1\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, calls := epSweep{home: home, mode: "allowlist", listeners: map[string]string{"ss": ssRow("127.0.0.1:8083", "node", "83")}}.run(t)
	if !dialedPort(calls, "8083") {
		t.Fatalf("configured endpoint port was not probed: %v", calls)
	}
}

func TestLocalEndpointSweepTTL(t *testing.T) {
	home := t.TempDir()
	listeners := func(inode string) map[string]string {
		return map[string]string{"ss": ssRow("127.0.0.1:8000", "vllm", inode)}
	}
	resp := map[string]string{"8000/v1/models": "200\n" + `{"data":[{"id":"m"}]}`}
	first, calls := epSweep{home: home, mode: "allowlist", listeners: listeners("80"), responses: resp}.run(t)
	if !dialedPort(calls, "8000") || len(first.Confirmed) != 1 {
		t.Fatalf("first sweep: %+v %v", first, calls)
	}
	second, calls := epSweep{home: home, mode: "allowlist", listeners: listeners("80"), responses: resp}.run(t)
	if len(calls) != 0 || !second.CachedSweep || len(second.Confirmed) != len(first.Confirmed) {
		t.Fatalf("cached sweep: %+v %v", second, calls)
	}
	_, calls = epSweep{home: home, mode: "allowlist", listeners: listeners("81"), responses: resp}.run(t)
	if !dialedPort(calls, "8000") {
		t.Fatalf("changed listener inode did not invalidate whole cache: %v", calls)
	}
	// A nominal 60-second TTL is clamped to 600 seconds.
	path := filepath.Join(home, "cache", "local-endpoints-v1.json")
	var doc map[string]any
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, &doc); err != nil {
		t.Fatal(err)
	}
	last := doc["last"].(map[string]any)
	last["at"] = float64(time.Now().Unix() - 100)
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	clamped, calls := epSweep{home: home, mode: "allowlist", ttl: "60", listeners: listeners("81"), responses: resp}.run(t)
	if len(calls) != 0 || !clamped.CachedSweep {
		t.Fatalf("TTL minimum not honored: %+v %v", clamped, calls)
	}
	last["at"] = float64(time.Now().Unix() - 901)
	b, _ = json.Marshal(doc)
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	expired, calls := epSweep{home: home, mode: "allowlist", listeners: listeners("81"), responses: resp}.run(t)
	if !dialedPort(calls, "8000") || expired.CachedSweep {
		t.Fatalf("expired whole cache reused: %+v %v", expired, calls)
	}
}

func TestLocalEndpointSweepOff(t *testing.T) {
	got, calls := epSweep{mode: "off", listeners: map[string]string{"ss": ssRow("127.0.0.1:8000", "vllm", "8")}}.run(t)
	if len(calls) != 0 || got.Reason != "disabled" {
		t.Fatalf("result=%+v calls=%v", got, calls)
	}
}
