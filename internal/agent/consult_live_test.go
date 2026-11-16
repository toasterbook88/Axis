//go:build liveconsult

package agent

import (
	"context"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/chat"
)

// TestConsultLive calls consult through the real OpenAI-compatible client.
// The default suite does not compile this file. Set AXIS_CONSULT_LIVE=1 and
// pass a loopback endpoint, comma-separated model ids, and a bearer token in
// the environment. This file does not name a deployment.
func TestConsultLive(t *testing.T) {
	if os.Getenv("AXIS_CONSULT_LIVE") != "1" {
		t.Skip("set AXIS_CONSULT_LIVE=1 to call a loopback seat")
	}
	endpoint := strings.TrimSpace(os.Getenv("AXIS_CONSULT_ENDPOINT"))
	if err := consultLiveLoopback(endpoint); err != nil {
		t.Fatal(err)
	}
	key := os.Getenv("AXIS_CONSULT_KEY")
	if strings.TrimSpace(key) == "" {
		t.Fatal("AXIS_CONSULT_KEY is empty")
	}
	models, err := consultLiveModels(os.Getenv("AXIS_CONSULT_MODELS"))
	if err != nil {
		t.Fatal(err)
	}

	const prompt = "Reply with the single word pong and nothing else."
	driver := &stubBackend{name: "driver", response: chat.Message{Role: chat.RoleAssistant, Content: "driver"}}
	a := New(Config{Backend: driver, Model: "driver-model", Output: io.Discard})

	for i, model := range models {
		t.Run(fmt.Sprintf("seat-%d", i+1), func(t *testing.T) {
			seat, err := NewOpenAICompatibleBackend(endpoint, model, key)
			if err != nil {
				t.Fatalf("backend: %s", consultLiveRedact(err.Error(), key))
			}
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Second)
			defer cancel()
			start := time.Now()
			text, err := consult(ctx, seat, consultRequest{
				SeatID:     fmt.Sprintf("seat-%d", i+1),
				Prompt:     prompt,
				SingleSlot: true,
			})
			elapsed := time.Since(start).Round(time.Millisecond)
			if err != nil {
				t.Errorf("consult: %s (%s)", consultLiveRedact(err.Error(), key), elapsed)
				return
			}
			t.Logf("model=%s text=%s (%s)", model, consultLiveRedact(text, key), elapsed)
		})
	}

	t.Run("seat-down", func(t *testing.T) {
		seat, err := NewOpenAICompatibleBackend("http://127.0.0.1:1", "unused", "")
		if err != nil {
			t.Fatalf("backend: %v", err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_, err = consult(ctx, seat, consultRequest{SeatID: "seat-down", Prompt: prompt})
		if err == nil {
			t.Fatal("closed port returned text")
		}
		if !strings.Contains(err.Error(), "seat-down") {
			t.Fatalf("error does not name the seat: %s", consultLiveRedact(err.Error(), key))
		}
		t.Logf("closed port: %s", consultLiveRedact(err.Error(), key))
	})

	if a.Model() != "driver-model" || a.Backend() != ChatBackend(driver) || driver.callCount() != 0 {
		t.Errorf("live consult changed the driver: model=%q calls=%d", a.Model(), driver.callCount())
	}
}

func consultLiveLoopback(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil {
		return fmt.Errorf("AXIS_CONSULT_ENDPOINT must be an http loopback URL with no userinfo")
	}
	switch u.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return nil
	default:
		return fmt.Errorf("AXIS_CONSULT_ENDPOINT must use a loopback host")
	}
}

func consultLiveModels(raw string) ([]string, error) {
	var models []string
	for _, part := range strings.Split(raw, ",") {
		model := strings.TrimSpace(part)
		if model == "" {
			continue
		}
		if len(model) > 80 || strings.ContainsAny(model, " \t/:\\") {
			return nil, fmt.Errorf("AXIS_CONSULT_MODELS has an unusable id")
		}
		models = append(models, model)
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("AXIS_CONSULT_MODELS is empty")
	}
	return models, nil
}

func consultLiveRedact(s, secret string) string {
	if secret != "" {
		s = strings.ReplaceAll(s, secret, "[redacted]")
	}
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 180 {
		s = s[:180] + "..."
	}
	return s
}
