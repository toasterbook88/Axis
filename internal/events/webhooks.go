package events

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var (
	webhookURLs []string
	webhookMu   sync.RWMutex
	httpClient  = &http.Client{Timeout: 5 * time.Second}
	backoffBase = 1 * time.Second
)

// SetWebhooks registers a list of webhook URLs for operational event dispatching.
func SetWebhooks(urls []string) {
	webhookMu.Lock()
	defer webhookMu.Unlock()
	webhookURLs = make([]string, len(urls))
	copy(webhookURLs, urls)
}

// getWebhooks returns a copy of the registered webhook URLs.
func getWebhooks() []string {
	webhookMu.RLock()
	defer webhookMu.RUnlock()
	urls := make([]string, len(webhookURLs))
	copy(urls, webhookURLs)
	return urls
}

// dispatchWebhooks async posts the event to all registered webhook URLs.
func dispatchWebhooks(evt Event) {
	urls := getWebhooks()
	if len(urls) == 0 {
		return
	}

	payload, err := json.Marshal(evt)
	if err != nil {
		slog.Error("failed to marshal event for webhook dispatch", "event", evt.Name, "error", err)
		return
	}

	for _, url := range urls {
		inflightEvents.Add(1)
		go func(targetURL string, body []byte) {
			defer inflightEvents.Done()
			if err := postWithRetry(targetURL, body); err != nil {
				slog.Error("failed to dispatch webhook", "url", targetURL, "event", evt.Name, "error", err)
				_ = writeDeadLetter(targetURL, err.Error(), evt)
			}
		}(url, payload)
	}
}

func postWithRetry(url string, body []byte) error {
	backoff := backoffBase
	var lastErr error

	for attempt := 0; attempt <= 3; attempt++ {
		req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err == nil {
			defer resp.Body.Close()
			if resp.StatusCode >= 200 && resp.StatusCode < 300 {
				return nil
			}
			lastErr = fmt.Errorf("http status %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		if attempt < 3 {
			time.Sleep(backoff)
			backoff *= 2
		}
	}

	return lastErr
}

// WebhookDeadLetter represents a failed webhook delivery recorded for auditing.
type WebhookDeadLetter struct {
	EventID   string    `json:"event_id"`
	URL       string    `json:"url"`
	Error     string    `json:"error"`
	Timestamp time.Time `json:"timestamp"`
	Event     Event     `json:"event"`
}

func writeDeadLetter(targetURL string, errMsg string, evt Event) error {
	path := defaultLogPath()
	dir := filepath.Dir(path)
	dlPath := filepath.Join(dir, "webhook-deadletter.jsonl")

	dl := WebhookDeadLetter{
		EventID:   evt.ID,
		URL:       targetURL,
		Error:     errMsg,
		Timestamp: time.Now().UTC(),
		Event:     evt,
	}

	data, err := json.Marshal(dl)
	if err != nil {
		return err
	}

	f, err := os.OpenFile(dlPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}
	defer f.Close()

	if _, err := f.Write(append(data, '\n')); err != nil {
		return err
	}
	return nil
}
