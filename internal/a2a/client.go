// Copyright (c) 2026 Smith Software Solutions
package a2a

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Client interacts with the A2A HTTP API on an Axis node.
type Client struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

// NewClient returns an initialized A2A Client.
func NewClient(baseURL, token string, httpClient *http.Client) *Client {
	baseURL = strings.TrimSpace(baseURL)
	baseURL = strings.TrimRight(baseURL, "/")
	if !strings.HasPrefix(baseURL, "http://") && !strings.HasPrefix(baseURL, "https://") {
		baseURL = "http://" + baseURL
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{
		BaseURL:    baseURL,
		Token:      strings.TrimSpace(token),
		HTTPClient: httpClient,
	}
}

// FetchCard retrieves the node's agent card from /.well-known/agent-card.json.
func (c *Client) FetchCard(ctx context.Context) (*AgentCard, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/.well-known/agent-card.json", nil)
	if err != nil {
		return nil, fmt.Errorf("creating card request: %w", err)
	}
	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetching agent card: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("agent card request failed (%s): %s", resp.Status, strings.TrimSpace(string(body)))
	}

	var card AgentCard
	if err := json.NewDecoder(resp.Body).Decode(&card); err != nil {
		return nil, fmt.Errorf("decoding agent card: %w", err)
	}
	return &card, nil
}

// Send submits a task request to POST /a2a/v1/message:send.
func (c *Client) Send(ctx context.Context, sendReq SendRequest) (*Task, error) {
	payload, err := json.Marshal(sendReq)
	if err != nil {
		return nil, fmt.Errorf("marshaling send request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/a2a/v1/message:send", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating send request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("dispatching a2a task: %w", err)
	}
	defer resp.Body.Close()

	return decodeTaskOrError(resp)
}

// Get retrieves a task's state from GET /a2a/v1/tasks/{id}.
func (c *Client) Get(ctx context.Context, taskID string) (*Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("task id cannot be empty")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/a2a/v1/tasks/"+url.PathEscape(taskID), nil)
	if err != nil {
		return nil, fmt.Errorf("creating get task request: %w", err)
	}
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("getting a2a task: %w", err)
	}
	defer resp.Body.Close()

	return decodeTaskOrError(resp)
}

// Approve authorizes execution of a pending task at POST /a2a/v1/tasks/{id}/approve.
func (c *Client) Approve(ctx context.Context, taskID, confirm, mode string) (*Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("task id cannot be empty")
	}
	if mode == "" {
		mode = "script"
	}

	body := map[string]string{
		"confirm": confirm,
		"mode":    mode,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling approve body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/a2a/v1/tasks/"+url.PathEscape(taskID)+"/approve", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating approve request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("approving a2a task: %w", err)
	}
	defer resp.Body.Close()

	return decodeTaskOrError(resp)
}

// Reject cancels a pending task at POST /a2a/v1/tasks/{id}/reject.
func (c *Client) Reject(ctx context.Context, taskID, reason string) (*Task, error) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return nil, errors.New("task id cannot be empty")
	}

	body := map[string]string{
		"reason": reason,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, fmt.Errorf("marshaling reject body: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/a2a/v1/tasks/"+url.PathEscape(taskID)+"/reject", bytes.NewReader(payload))
	if err != nil {
		return nil, fmt.Errorf("creating reject request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if c.Token != "" {
		req.Header.Set("Authorization", "Bearer "+c.Token)
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("rejecting a2a task: %w", err)
	}
	defer resp.Body.Close()

	return decodeTaskOrError(resp)
}

func decodeTaskOrError(resp *http.Response) (*Task, error) {
	// Bound only the error body. On a misrouted --addr, or a proxy sitting in
	// front of a downed node, this is the path that can return an arbitrarily
	// large HTML page. Success payloads stay uncapped so a task whose artifacts
	// exceed the cap still decodes instead of failing with a truncated-JSON
	// error. This mirrors FetchCard, which caps only its error body.
	if resp.StatusCode >= 400 {
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return nil, fmt.Errorf("reading response body: %w", err)
		}
		var errResp struct {
			Error string `json:"error"`
		}
		if jsonErr := json.Unmarshal(raw, &errResp); jsonErr == nil && errResp.Error != "" {
			return nil, fmt.Errorf("server error (%s): %s", resp.Status, errResp.Error)
		}
		return nil, fmt.Errorf("server error (%s): %s", resp.Status, strings.TrimSpace(string(raw)))
	}

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("reading response body: %w", err)
	}

	var task Task
	if err := json.Unmarshal(raw, &task); err != nil {
		return nil, fmt.Errorf("decoding task response: %w", err)
	}
	return &task, nil
}
