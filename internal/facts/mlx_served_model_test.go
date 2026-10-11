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

// runMLXProbe runs the real script with an mlx_lm.server process whose argv
// is args, and the given curl stub. An empty curlBody leaves curl off PATH.
func runMLXProbe(t *testing.T, args, curlBody string) mlxDiscoveryPayload {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	bin := t.TempDir()
	stub := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(bin, name), []byte("#!/bin/sh\n"+body+"\n"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	stub("mlx_lm", `exit 0`)
	stub("pgrep", `echo 4242`)
	stub("ps", `case "$*" in
  "-p 4242 -o args=") echo "`+args+`" ;;
  "-o rss= -p 4242") echo 3145728 ;;
esac`)
	if curlBody != "" {
		stub("curl", curlBody)
	}
	cmd := exec.Command("bash", "-c", MLXDiscoveryScript)
	cmd.Env = withExactToolPATH(t, bin, "head", "awk", "grep", "python3")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	var payload mlxDiscoveryPayload
	if err := json.Unmarshal(out, &payload); err != nil {
		t.Fatalf("json %q: %v", bytes.TrimSpace(out), err)
	}
	return payload
}

// mlx_lm.server's /v1/models lists the Hugging Face cache, not the loaded
// model. Only the argv model is published, and as listed, never loaded.
func TestMLXDiscoveryPublishesArgvModelNotCacheListing(t *testing.T) {
	got := runMLXProbe(t, "python -m mlx_lm.server --model mlx-community/Served-4bit --port 8183",
		`case "$*" in *"-w"*) printf 200 ;; *) echo '{"data":[{"id":"mlx-community/Cached-A"},{"id":"mlx-community/Cached-B"},{"id":"mlx-community/Served-4bit"}]}' ;; esac`)
	if len(got.ResidentModels) != 1 {
		t.Fatalf("residents = %+v, want only the served model", got.ResidentModels)
	}
	r := got.ResidentModels[0]
	if r.Name != "Served-4bit" || r.Source != "mlx-argv" || r.State != models.ModelCatalogListed || r.LoadSignal != "none" {
		t.Fatalf("resident = %+v", r)
	}
	if r.Provenance["name"] != "argv --model" || r.Provenance["state"] != "GET /v1/models 200" {
		t.Fatalf("provenance = %v", r.Provenance)
	}
}

func TestMLXDiscoverySilentServerIsDown(t *testing.T) {
	got := runMLXProbe(t, "python -m mlx_lm.server --model /models/local-mlx/ --port 8183", `printf 000; exit 7`)
	if len(got.ResidentModels) != 1 || got.ResidentModels[0].State != models.ModelCatalogDown || got.ResidentModels[0].Name != "local-mlx" {
		t.Fatalf("residents = %+v, want local-mlx down", got.ResidentModels)
	}
}

func TestMLXDiscoveryShortModelFlagAndNoCurl(t *testing.T) {
	got := runMLXProbe(t, `mlx_lm.server -m org/quoted\"name --port 8183`, "")
	if len(got.ResidentModels) != 1 {
		t.Fatalf("residents = %+v", got.ResidentModels)
	}
	if r := got.ResidentModels[0]; r.Name != `quoted"name` || r.State != "" {
		t.Fatalf("resident = %+v, want escaped name and no state without curl", r)
	}
}

// A server started without --model serves nothing named; no resident row.
func TestMLXDiscoveryWithoutModelArgPublishesNoResident(t *testing.T) {
	got := runMLXProbe(t, "python -m mlx_lm.server --port 8183", `printf 200`)
	if !got.Running || len(got.ResidentModels) != 0 {
		t.Fatalf("payload = %+v, want running with no residents", got)
	}
}
