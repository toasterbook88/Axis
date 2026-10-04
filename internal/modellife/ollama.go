package modellife

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OllamaLoadScript preloads a model on the Ollama server that is already
// listening on 127.0.0.1:11434. It does not exec ollama serve. The last
// command's stdout is GET /api/ps, which is the load fact.
func OllamaLoadScript(model, keepAlive string, numCtx *int) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("ollama model is required")
	}
	if numCtx != nil && *numCtx < 1 {
		return "", fmt.Errorf("ollama num_ctx must be >= 1")
	}
	payload := struct {
		Model     string         `json:"model"`
		Stream    bool           `json:"stream"`
		KeepAlive string         `json:"keep_alive,omitempty"`
		Options   map[string]int `json:"options,omitempty"`
	}{
		Model:     model,
		Stream:    false,
		KeepAlive: keepAlive,
	}
	if numCtx != nil {
		payload.Options = map[string]int{"num_ctx": *numCtx}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	probe := "curl -fsS --max-time 5 http://127.0.0.1:11434/api/ps"
	post := "curl -fsS --max-time 30 -X POST http://127.0.0.1:11434/api/generate -H 'Content-Type: application/json' -d " + shellSingleQuote(string(raw))
	return probe + " >/dev/null && " + post + " >/dev/null && " + probe, nil
}

// OllamaUnloadScript asks the existing server to drop a model, then prints
// GET /api/ps. It does not kill comm=ollama or stop a supervisor unit.
func OllamaUnloadScript(model string) (string, error) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", fmt.Errorf("ollama model is required")
	}
	payload := struct {
		Model     string `json:"model"`
		KeepAlive int    `json:"keep_alive"`
	}{Model: model, KeepAlive: 0}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}
	post := "curl -fsS --max-time 30 -X POST http://127.0.0.1:11434/api/generate -H 'Content-Type: application/json' -d " + shellSingleQuote(string(raw))
	probe := "curl -fsS --max-time 5 http://127.0.0.1:11434/api/ps"
	return post + " >/dev/null && " + probe, nil
}

// OllamaPSHasModel reports whether /api/ps lists the requested model name.
func OllamaPSHasModel(body, name string) (bool, error) {
	var doc struct {
		Models []struct {
			Name  string `json:"name"`
			Model string `json:"model"`
		} `json:"models"`
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		return false, fmt.Errorf("ollama /api/ps: %w", err)
	}
	name = strings.TrimSpace(name)
	for _, model := range doc.Models {
		if ollamaNameMatches(model.Name, name) || ollamaNameMatches(model.Model, name) {
			return true, nil
		}
	}
	return false, nil
}

// ollamaNameMatches accepts an exact name, and the default tag when one side
// is untagged. A different tag, such as q4, does not match.
func ollamaNameMatches(listed, requested string) bool {
	listed = strings.TrimSpace(listed)
	requested = strings.TrimSpace(requested)
	if listed == "" || requested == "" {
		return false
	}
	if listed == requested {
		return true
	}
	if !strings.Contains(requested, ":") && listed == requested+":latest" {
		return true
	}
	if !strings.Contains(listed, ":") && requested == listed+":latest" {
		return true
	}
	return false
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
