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

// ToolDef describes a tool that the model may invoke (Ollama tool-calling schema).
type ToolDef struct {
	Type     string          `json:"type"`
	Function ToolDefFunction `json:"function"`
}

// ToolDefFunction is the function metadata within a tool definition.
type ToolDefFunction struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

// Client talks to the Ollama /api/chat endpoint with structured messages
// and optional tool-calling support.
type Client struct {
	Endpoint string
	Model    string
	http     *http.Client
}

// NewClient creates a Client for the given Ollama endpoint and model.
func NewClient(endpoint, model string) *Client {
	return &Client{
		Endpoint: endpoint,
		Model:    model,
		http:     &http.Client{},
	}
}

// chatRequest is the JSON body for POST /api/chat.
type chatRequest struct {
	Model    string    `json:"model"`
	Messages []Message `json:"messages"`
	Tools    []ToolDef `json:"tools,omitempty"`
	Stream   bool      `json:"stream"`
}

// chatStreamChunk is one line of the streaming response from /api/chat.
type chatStreamChunk struct {
	Message chatChunkMessage `json:"message"`
	Done    bool             `json:"done"`
}

type chatChunkMessage struct {
	Role      string     `json:"role"`
	Content   string     `json:"content"`
	ToolCalls []ToolCall `json:"tool_calls,omitempty"`
}

// ChatStream sends the conversation to Ollama and streams the assistant's text
// content to w. It returns the complete assistant message (including any tool
// calls) so the caller can inspect and act on them.
//
// If tools is nil, no tool-calling header is sent and the model produces only text.
func (c *Client) ChatStream(ctx context.Context, msgs []Message, tools []ToolDef, w io.Writer) (Message, error) {
	if err := c.EnsureRunning(ctx, w); err != nil {
		return Message{}, fmt.Errorf("ollama not ready: %w", err)
	}

	body := chatRequest{
		Model:    c.Model,
		Messages: msgs,
		Tools:    tools,
		Stream:   true,
	}
	data, err := json.Marshal(body)
	if err != nil {
		return Message{}, fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", c.Endpoint+"/api/chat", bytes.NewReader(data))
	if err != nil {
		return Message{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		return Message{}, fmt.Errorf("connect to ollama: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return Message{}, fmt.Errorf("server returned status: %s", resp.Status)
	}

	var result Message
	result.Role = RoleAssistant

	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var chunk chatStreamChunk
		if err := json.Unmarshal(line, &chunk); err != nil {
			return result, fmt.Errorf("parse chunk: %w", err)
		}

		if chunk.Message.Content != "" {
			if w != nil {
				fmt.Fprint(w, chunk.Message.Content)
			}
			result.Content += chunk.Message.Content
		}

		if len(chunk.Message.ToolCalls) > 0 {
			result.ToolCalls = append(result.ToolCalls, chunk.Message.ToolCalls...)
		}

		if chunk.Done {
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return result, err
	}

	return result, nil
}

// EnsureRunning checks the Ollama daemon health and model availability.
// If the daemon is not responding it attempts to start it.
func (c *Client) EnsureRunning(ctx context.Context, w io.Writer) error {
	// 1. Check daemon health.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.Endpoint, nil)
	if err != nil {
		return fmt.Errorf("create readiness request: %w", err)
	}
	resp, err := c.http.Do(req)
	if err == nil {
		resp.Body.Close()
	} else {
		if w != nil {
			fmt.Fprintf(w, "[AXIS] Auto-starting local Ollama daemon...\n")
		}
		cmd := exec.Command("ollama", "serve")
		if err := cmd.Start(); err != nil {
			return fmt.Errorf("could not start ollama serve: %w", err)
		}
		up := false
		for i := 0; i < 10; i++ {
			time.Sleep(500 * time.Millisecond)
			if r, e := c.http.Get(c.Endpoint); e == nil {
				r.Body.Close()
				up = true
				break
			}
		}
		if !up {
			return fmt.Errorf("ollama daemon failed to become responsive after start")
		}
	}

	// 2. Check model availability.
	checkReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.Endpoint+"/api/show",
		bytes.NewBufferString(fmt.Sprintf(`{"model":%q}`, c.Model)))
	if err != nil {
		return fmt.Errorf("create model check request: %w", err)
	}
	checkReq.Header.Set("Content-Type", "application/json")
	checkResp, err := c.http.Do(checkReq)
	if err != nil {
		return fmt.Errorf("check model %q: %w", c.Model, err)
	}
	defer checkResp.Body.Close()

	switch checkResp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return fmt.Errorf("model %q is not available locally; run: ollama pull %s", c.Model, c.Model)
	default:
		return fmt.Errorf("model check for %q failed with status: %s", c.Model, checkResp.Status)
	}
}
