package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/seatlease"
)

// deskLeaseComplete sends one non-streaming completion to the seat URL in
// the lease. Tests replace it. It is not a cluster gateway client.
var deskLeaseComplete = postDeskCompletion

func runDeskLease(w io.Writer, path, prompt string, now time.Time) error {
	lease, err := readDeskLease(path)
	if err != nil {
		return ExitCodeError{Code: ExitErrIO, Message: seatlease.RefusalMissing}
	}
	refusal, ok := seatlease.Decide(lease, now)
	if !ok {
		return ExitCodeError{Code: ExitErrGeneric, Message: refusal}
	}
	prompt = strings.TrimSpace(prompt)
	if prompt == "" {
		return ExitCodeError{Code: ExitErrGeneric, Message: "a prompt is required"}
	}
	body, err := deskLeaseComplete(lease.CompletionURL, lease.Token, prompt)
	if err != nil {
		return ExitCodeError{Code: ExitErrGeneric, Message: seatlease.RefusalUnavailable}
	}
	if _, err := io.WriteString(w, body); err != nil {
		return ExitCodeError{Code: ExitErrIO, Message: err.Error()}
	}
	return nil
}

func readDeskLease(path string) (*seatlease.Lease, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var lease seatlease.Lease
	if err := json.Unmarshal(raw, &lease); err != nil {
		return nil, nil
	}
	return &lease, nil
}

func postDeskCompletion(rawURL, token, prompt string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", errors.New(seatlease.RefusalMissing)
	}
	payload, err := json.Marshal(map[string]any{
		"messages": []map[string]string{{"role": "user", "content": prompt}},
	})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequest(http.MethodPost, parsed.String(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	client := &http.Client{
		Timeout: 30 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("seat status %d", resp.StatusCode)
	}
	return string(body), nil
}
