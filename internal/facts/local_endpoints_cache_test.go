package facts

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
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

	first, calls := epSweep{home: home, listeners: listeners("99"), responses: resp}.run(t)
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
	second, calls := epSweep{home: home, listeners: listeners("99"), responses: resp}.run(t)
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
	_, calls = epSweep{home: home, listeners: listeners("100"), responses: resp}.run(t)
	if !dialedPort(calls, "9999") || dialedPort(calls, "7777") || dialedPort(calls, "4000") {
		t.Fatalf("after a socket change dialed %v, want :9999 and :8000 only", calls)
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
	if _, calls := (epSweep{home: home, listeners: map[string]string{"ss": ssRow("127.0.0.1:7777", "", "77")}}).run(t); !dialedPort(calls, "7777") {
		t.Fatalf("an entry older than 24 h must be probed again: %v", calls)
	}

	// netstat names no socket, so nothing it lists is ever served from cache.
	ns := epSweep{home: t.TempDir(), listeners: map[string]string{"netstat": "tcp 0 0 127.0.0.1:7777 0.0.0.0:* LISTEN\n"}}
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
