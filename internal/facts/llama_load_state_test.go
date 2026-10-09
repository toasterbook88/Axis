package facts

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// runLlamaProbe runs the real script with a llama-server process on :8181
// serving served-model.gguf (plus extraArgs), and the given curl stub. An
// empty curlBody leaves curl off the PATH.
func runLlamaProbe(t *testing.T, extraArgs, curlBody string) models.ResidentModel {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	model := filepath.Join(t.TempDir(), "served-model.gguf")
	if err := os.WriteFile(model, []byte("gguf"), 0o644); err != nil {
		t.Fatal(err)
	}
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("pgrep", `echo 4242`)
	stub("ps", `echo "/opt/llama.cpp/bin/llama-server --model `+model+` --port 8181 `+extraArgs+`"`)
	stub("readlink", `echo /opt/llama.cpp/bin/llama-server`)
	stub("lsof", `exit 1`)
	stub("ss", `exit 1`)
	stub("netstat", `exit 1`)
	if curlBody != "" {
		stub("curl", curlBody)
	}
	cmd := exec.Command("bash", "-c", LlamaServerDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "basename", "sed", "stat")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload llamaServerDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	if len(payload.ResidentModels) != 1 {
		t.Fatalf("residents = %+v, want 1", payload.ResidentModels)
	}
	return payload.ResidentModels[0]
}

func TestLlamaServerHealthOKIsLoadedWithServedContext(t *testing.T) {
	got := runLlamaProbe(t, "", `case "$*" in
  *"/health"*) printf 200 ;;
  *"/props"*) echo '{"default_generation_settings":{"n_ctx":4096,"model":"x"},"total_slots":1}' ;;
  *"/v1/models"*) echo '{"data":[{"id":"/models/served-model.gguf","meta":{"n_ctx_train":131072}}]}' ;;
esac`)
	if got.State != models.ModelCatalogLoaded || got.LoadSignal != "health-ok" {
		t.Fatalf("state = %q signal %q, want loaded/health-ok", got.State, got.LoadSignal)
	}
	if got.ContextWindow != 4096 {
		t.Fatalf("context_window = %d, want served n_ctx 4096 (not n_ctx_train)", got.ContextWindow)
	}
	if got.Name != "served-model" || got.Provenance["state"] != "GET /health 200" {
		t.Fatalf("row = %+v", got)
	}
}

func TestLlamaServerHealth503IsLoading(t *testing.T) {
	got := runLlamaProbe(t, "", `case "$*" in *"/health"*) printf 503 ;; esac`)
	if got.State != models.ModelCatalogLoading || got.ContextWindow != 0 {
		t.Fatalf("row = %+v, want loading without context", got)
	}
}

func TestLlamaServerSilentIsDown(t *testing.T) {
	got := runLlamaProbe(t, "", `printf 000; exit 7`)
	if got.State != models.ModelCatalogDown || got.LoadSignal != "health-silent" || got.Provenance["state"] != "GET /health 000" {
		t.Fatalf("row = %+v, want down/health-silent", got)
	}
}

// A keyed server: /health is public, the rest answers 401.
func TestLlamaServerKeyedIsLoadedFromHealth(t *testing.T) {
	got := runLlamaProbe(t, "", `case "$*" in
  *"/health"*) printf 200 ;;
  *) echo '{"error":{"message":"Invalid API Key","code":401}}' ;;
esac`)
	if got.State != models.ModelCatalogLoaded || got.ContextWindow != 0 || got.Name != "served-model" {
		t.Fatalf("row = %+v, want loaded under argv name, no context", got)
	}
}

func TestLlamaServerServedIDDifferentFromArgvIsListed(t *testing.T) {
	got := runLlamaProbe(t, "", `case "$*" in
  *"/health"*) printf 200 ;;
  *"/v1/models"*) echo '{"data":[{"id":"other-model.gguf"}]}' ;;
esac`)
	if got.State != models.ModelCatalogListed || got.Name != "other-model" || got.LoadSignal != "served-id-differs" {
		t.Fatalf("row = %+v, want listed under served id", got)
	}
}

// --alias is argv too: a served id equal to it is the same loaded model.
func TestLlamaServerServedIDMatchingArgvAliasIsLoaded(t *testing.T) {
	got := runLlamaProbe(t, "--alias coder", `case "$*" in
  *"/health"*) printf 200 ;;
  *"/v1/models"*) echo '{"data":[{"id":"coder"}]}' ;;
esac`)
	if got.State != models.ModelCatalogLoaded || got.Name != "served-model" || got.LoadSignal != "health-ok" {
		t.Fatalf("row = %+v, want loaded under argv name", got)
	}
}

// --alias takes a comma-separated list; the served id is one of them.
func TestLlamaServerServedIDInArgvAliasListIsLoaded(t *testing.T) {
	got := runLlamaProbe(t, "--alias coder,helper", `case "$*" in
  *"/health"*) printf 200 ;;
  *"/v1/models"*) echo '{"data":[{"id":"helper"}]}' ;;
esac`)
	if got.State != models.ModelCatalogLoaded || got.Name != "served-model" {
		t.Fatalf("row = %+v, want loaded under argv name", got)
	}
}

// A served id with a backslash must still produce valid JSON.
func TestLlamaServerServedIDWithBackslashIsEscaped(t *testing.T) {
	got := runLlamaProbe(t, "", `case "$*" in
  *"/health"*) printf 200 ;;
  *"/v1/models"*) printf '%s\n' '{"data":[{"id":"C:\\models\\other.gguf"}]}' ;;
esac`)
	if got.State != models.ModelCatalogListed || got.Name == "" {
		t.Fatalf("row = %+v, want listed under escaped served id", got)
	}
}

// Without curl the collector cannot observe load state; it leaves state
// empty and Catalog keeps the #507 mapping.
func TestLlamaServerWithoutCurlLeavesStateEmpty(t *testing.T) {
	got := runLlamaProbe(t, "", "")
	if got.State != "" || got.LoadSignal != "" {
		t.Fatalf("row = %+v, want no state without curl", got)
	}
}
