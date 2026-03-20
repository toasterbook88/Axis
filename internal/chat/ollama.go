package chat

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
