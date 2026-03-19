package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"time"
)

// OllamaClient interacts with the local Ollama API
type OllamaClient struct {
	Endpoint string
	Model    string
	client   *http.Client
}

func NewOllamaClient(endpoint, model string) *OllamaClient {
	return &OllamaClient{
		Endpoint: endpoint,
		Model:    model,
		client:   &http.Client{},
	}
}

type ollamaRequest struct {
	Model  string `json:"model"`
	Prompt string `json:"prompt"`
	Stream bool   `json:"stream"`
}

type ollamaResponse struct {
	Response string `json:"response"`
	Done     bool   `json:"done"`
}

// GenerateStream streams the inference response to an io.Writer
func (c *OllamaClient) GenerateStream(ctx context.Context, prompt string, w io.Writer) error {
	if err := c.ensureRunning(ctx, w); err != nil {
		return fmt.Errorf("ollama not ready: %w", err)
	}

	reqBody := ollamaRequest{
		Model:  c.Model,
		Prompt: prompt,
		Stream: true,
	}

	bodyData, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+"/api/generate", bytes.NewReader(bodyData))
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return fmt.Errorf("connect to ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("server returned status: %s", resp.Status)
	}

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk ollamaResponse
		if err := json.Unmarshal(line, &chunk); err != nil {
			return fmt.Errorf("parse chunk: %w", err)
		}

		fmt.Fprint(w, chunk.Response)

		if chunk.Done {
			break
		}
	}

	return scanner.Err()
}

func (c *OllamaClient) ensureRunning(ctx context.Context, w io.Writer) error {
	// 1. Check if daemon is responsive
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint, nil)
	if err != nil {
		return fmt.Errorf("create ollama readiness request: %w", err)
	}
	resp, err := c.client.Do(req)
	if err == nil {
		resp.Body.Close()
	} else {
		// Try starting the daemon
		fmt.Fprintf(w, "[AXIS] Auto-starting local Ollama daemon...\n")
		cmd := exec.Command("ollama", "serve")
		// Start in background, detached from our process grouping if possible, but standard Start() is fine
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("could not start ollama serve: %w", err)
		}

		// Wait for it to come up
		up := false
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			if r, e := c.client.Get(c.Endpoint); e == nil {
				r.Body.Close()
				up = true
				break
			}
		}
		if !up {
			return fmt.Errorf("ollama daemon failed to become responsive after start")
		}
	}

	// 2. Check if model is available locally.
	checkReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.Endpoint+"/api/show", bytes.NewBufferString(fmt.Sprintf(`{"model":"%s"}`, c.Model)))
	if err != nil {
		return fmt.Errorf("create model readiness request: %w", err)
	}
	checkReq.Header.Set("Content-Type", "application/json")
	checkResp, err := c.client.Do(checkReq)
	if err != nil {
		return fmt.Errorf("check model %q: %w", c.Model, err)
	}
	defer checkResp.Body.Close()

	if checkResp.StatusCode == http.StatusOK {
		return nil
	}

	if checkResp.StatusCode == http.StatusNotFound {
		return fmt.Errorf("model %q is not available locally; run: ollama pull %s", c.Model, c.Model)
	}

	return fmt.Errorf("model check for %q failed with status: %s", c.Model, checkResp.Status)
}
