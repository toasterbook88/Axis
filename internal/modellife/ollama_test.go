package modellife

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestOllamaLoadScriptIsLoopbackGenerateWithoutPrompt(t *testing.T) {
	script, err := OllamaLoadScript("mistral", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(script, "curl -fsS --max-time 5 http://127.0.0.1:11434/api/ps") < 2 {
		t.Fatalf("load must probe /api/ps before and after POST:\n%s", script)
	}
	if !strings.Contains(script, "curl -fsS --max-time 30 -X POST http://127.0.0.1:11434/api/generate") {
		t.Fatalf("post missing:\n%s", script)
	}
	if !strings.Contains(script, `grep -q '"done_reason":"load"'`) {
		t.Fatalf("load must accept only the Ollama load response:\n%s", script)
	}
	if strings.Contains(script, "/v1/models") || strings.Contains(script, "ollama serve") || strings.Contains(script, "OLLAMA_HOST") || strings.Contains(script, "OLLAMA_KEEP_ALIVE") {
		t.Fatalf("script widens or starts ollama:\n%s", script)
	}
	body := ollamaJSONBody(t, script)
	if body["model"] != "mistral" || body["stream"] != false {
		t.Fatalf("body=%v", body)
	}
	if _, ok := body["prompt"]; ok {
		t.Fatalf("prompt must be omitted: %v", body)
	}
	if _, ok := body["keep_alive"]; ok {
		t.Fatalf("unset keep_alive must be omitted: %v", body)
	}
	if _, ok := body["options"]; ok {
		t.Fatalf("unset options must be omitted: %v", body)
	}
	for _, banned := range []string{"num_gpu", "main_gpu", "num_batch", "num_thread"} {
		if strings.Contains(script, banned) {
			t.Fatalf("script contains %s:\n%s", banned, script)
		}
	}
}

func TestOllamaLoadScriptAddsOnlyKeepAliveAndNumCtx(t *testing.T) {
	ctx := 2048
	script, err := OllamaLoadScript("mistral", "10m", &ctx)
	if err != nil {
		t.Fatal(err)
	}
	body := ollamaJSONBody(t, script)
	if body["keep_alive"] != "10m" {
		t.Fatalf("keep_alive=%v", body["keep_alive"])
	}
	options, ok := body["options"].(map[string]any)
	if !ok || len(options) != 1 || options["num_ctx"] != float64(2048) {
		t.Fatalf("options=%v", body["options"])
	}
}

func TestOllamaServerReadyRefusesIncompleteOrSilentNodes(t *testing.T) {
	err := OllamaServerReady(models.NodeFacts{Name: "storage", Status: models.StatusPartial, Ollama: &models.OllamaInfo{Listening: true}})
	if err == nil || !strings.Contains(err.Error(), "refusing ollama ssh") {
		t.Fatalf("partial err=%v", err)
	}
	err = OllamaServerReady(models.NodeFacts{Name: "storage", Status: models.StatusComplete})
	if err == nil || !strings.Contains(err.Error(), "no listening ollama") {
		t.Fatalf("silent err=%v", err)
	}
	if err := OllamaServerReady(models.NodeFacts{Name: "storage", Status: models.StatusComplete, Ollama: &models.OllamaInfo{Listening: true}}); err != nil {
		t.Fatal(err)
	}
}

func TestOllamaUnloadScriptKeepsAliveZeroAndDoesNotKill(t *testing.T) {
	script, err := OllamaUnloadScript("mistral")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "curl -fsS --max-time 30 -X POST http://127.0.0.1:11434/api/generate") {
		t.Fatalf("unload post missing:\n%s", script)
	}
	if !strings.Contains(script, "curl -fsS --max-time 5 http://127.0.0.1:11434/api/ps") {
		t.Fatalf("unload probe missing:\n%s", script)
	}
	body := ollamaJSONBody(t, script)
	if body["model"] != "mistral" || body["keep_alive"] != float64(0) {
		t.Fatalf("body=%v", body)
	}
	if _, ok := body["stream"]; ok {
		t.Fatalf("unload body=%v", body)
	}
	for _, banned := range []string{"kill", "systemctl", "fuser", "comm=ollama", "ollama serve", "num_gpu", "main_gpu"} {
		if strings.Contains(script, banned) {
			t.Fatalf("unload contains %s:\n%s", banned, script)
		}
	}
}

func TestOllamaPSHasModelReadsAPIPs(t *testing.T) {
	ok, err := OllamaPSHasModel(`{"models":[{"name":"mistral","model":"mistral"}]}`, "mistral")
	if err != nil || !ok {
		t.Fatalf("ok=%v err=%v", ok, err)
	}
	ok, err = OllamaPSHasModel(`{"models":[{"name":"other"}]}`, "mistral")
	if err != nil || ok {
		t.Fatalf("other ok=%v err=%v", ok, err)
	}
	if _, err := OllamaPSHasModel("not-json", "mistral"); err == nil {
		t.Fatal("expected json error")
	}
	ok, err = OllamaPSHasModel(`{"models":[{"name":"mistral:latest","model":"mistral:latest"}]}`, "mistral")
	if err != nil || !ok {
		t.Fatalf("untagged request latest listing ok=%v err=%v", ok, err)
	}
	ok, err = OllamaPSHasModel(`{"models":[{"name":"mistral","model":"mistral"}]}`, "mistral:latest")
	if err != nil || !ok {
		t.Fatalf("latest request untagged listing ok=%v err=%v", ok, err)
	}
	ok, err = OllamaPSHasModel(`{"models":[{"name":"mistral:q4","model":"mistral:q4"}]}`, "mistral")
	if err != nil || ok {
		t.Fatalf("other tag ok=%v err=%v", ok, err)
	}
}

func ollamaJSONBody(t *testing.T, script string) map[string]any {
	t.Helper()
	const marker = "-d '"
	start := strings.Index(script, marker)
	if start < 0 {
		t.Fatalf("no json body in %s", script)
	}
	rest := script[start+len(marker):]
	end := strings.Index(rest, "'")
	if end < 0 {
		t.Fatalf("unclosed body in %s", script)
	}
	var body map[string]any
	if err := json.Unmarshal([]byte(rest[:end]), &body); err != nil {
		t.Fatal(err)
	}
	return body
}
