package main

import (
	"context"
	"io"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/chat"
)

// consultCountBackend counts ChatStream calls. The resolver test uses it as
// the driver and never as a live seat.
type consultCountBackend struct {
	calls atomic.Int32
	text  string
}

func (b *consultCountBackend) ChatStream(context.Context, []chat.Message, []chat.ToolDef, io.Writer) (chat.Message, error) {
	b.calls.Add(1)
	return chat.Message{Role: chat.RoleAssistant, Content: b.text}, nil
}

func consultFixture(choices []ModelChoice) (*agent.Agent, *consultCountBackend, *atomic.Int32) {
	var built atomic.Int32
	driver := &consultCountBackend{text: "driver"}
	resolver := consultResolver(func(context.Context) []ModelChoice { return choices }, nil, func(target agent.ModelTarget, _ agent.CloudBackendOptions) (agent.ChatBackend, error) {
		built.Add(1)
		return &consultCountBackend{text: "seat " + target.ID}, nil
	})
	a := agent.New(agent.Config{
		Backend:  driver,
		Model:    "driver-model",
		Output:   io.Discard,
		Confirm:  func(string, string, int) agent.ConfirmResult { return agent.ConfirmYes },
		MaxTurns: 1,
	})
	a.SetConsultResolver(resolver)
	return a, driver, &built
}

func TestConsultResolverDisablesOllamaAutoStart(t *testing.T) {
	choice := ModelChoice{
		ID: "local:seat-a", Model: "seat-a", Protocol: agent.ProtocolOllama,
		Endpoint: "http://127.0.0.1:9",
	}
	resolver := consultResolver(func(context.Context) []ModelChoice {
		return []ModelChoice{choice}
	}, nil, agent.BuildBackend)
	backend, id, err := resolver(context.Background(), "seat-a")
	if err != nil {
		t.Fatal(err)
	}
	if id != choice.ID {
		t.Fatalf("id = %q", id)
	}
	client, ok := backend.(*chat.Client)
	if !ok {
		t.Fatalf("backend = %T", backend)
	}
	if client.AutoStartEnabled() {
		t.Fatal("consult ollama client still auto-starts")
	}
	if !chat.NewClient(choice.Endpoint, "driver-model").AutoStartEnabled() {
		t.Fatal("the driver client lost auto-start")
	}
}

func TestConsultRealResolverUnknownSeat(t *testing.T) {
	choices := []ModelChoice{
		{ID: "local:driver-model", Model: "driver-model", Protocol: agent.ProtocolOllama, Endpoint: "http://127.0.0.1:9"},
		{ID: "local:seat-a", Model: "seat-a", Protocol: agent.ProtocolOllama, Endpoint: "http://127.0.0.1:9"},
		{ID: "local:off", Model: "off", Protocol: agent.ProtocolOllama, Disabled: true},
		{ID: "local:shared-a", Model: "shared", Protocol: agent.ProtocolOllama, Endpoint: "http://127.0.0.1:9"},
		{ID: "local:shared-b", Model: "shared", Protocol: agent.ProtocolOllama, Endpoint: "http://127.0.0.1:9"},
	}

	assertNoBuild := func(t *testing.T, seat, want string) {
		t.Helper()
		a, driver, built := consultFixture(choices)
		_, err := a.ExecuteToolDirect(context.Background(), "consult_model", []byte(`{"seat":"`+seat+`","prompt":"hello"}`))
		if err == nil {
			t.Fatal("expected an error")
		}
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not quote %q", err, want)
		}
		if strings.Contains(strings.ToLower(err.Error()), "pull") {
			t.Fatalf("error tells the driver to pull: %q", err)
		}
		if built.Load() != 0 {
			t.Fatalf("BuildBackend calls = %d, want 0", built.Load())
		}
		if driver.calls.Load() != 0 || a.Model() != "driver-model" || a.Backend() != agent.ChatBackend(driver) {
			t.Fatal("unknown seat changed the driver")
		}
	}

	t.Run("missing", func(t *testing.T) { assertNoBuild(t, "missing-seat", "missing-seat") })
	t.Run("empty", func(t *testing.T) { assertNoBuild(t, "", `seat ""`) })
	t.Run("disabled id", func(t *testing.T) { assertNoBuild(t, "local:off", "local:off") })
	t.Run("disabled name", func(t *testing.T) { assertNoBuild(t, "off", `"off"`) })

	t.Run("ambiguous", func(t *testing.T) {
		a, driver, built := consultFixture(choices)
		_, err := a.ExecuteToolDirect(context.Background(), "consult_model", []byte(`{"seat":"shared","prompt":"hello"}`))
		if err == nil {
			t.Fatal("expected an error")
		}
		msg := err.Error()
		for _, want := range []string{`"shared"`, "local:shared-a", "local:shared-b"} {
			if !strings.Contains(msg, want) {
				t.Fatalf("error %q missing %s", msg, want)
			}
		}
		if strings.Contains(strings.ToLower(msg), "pull") {
			t.Fatalf("error tells the driver to pull: %q", msg)
		}
		if built.Load() != 0 || driver.calls.Load() != 0 || a.Model() != "driver-model" {
			t.Fatal("ambiguous seat built a backend or changed the driver")
		}
	})

	t.Run("two refs one id", func(t *testing.T) {
		var built atomic.Int32
		resolver := consultResolver(func(context.Context) []ModelChoice { return choices }, nil, func(target agent.ModelTarget, _ agent.CloudBackendOptions) (agent.ChatBackend, error) {
			built.Add(1)
			return &consultCountBackend{text: target.ID}, nil
		})
		_, idByID, err := resolver(context.Background(), "local:seat-a")
		if err != nil {
			t.Fatal(err)
		}
		_, idByName, err := resolver(context.Background(), "seat-a")
		if err != nil {
			t.Fatal(err)
		}
		if idByID != "local:seat-a" || idByName != idByID {
			t.Fatalf("ids = %q and %q", idByID, idByName)
		}
		if built.Load() != 2 {
			t.Fatalf("build calls = %d, want 2", built.Load())
		}
	})
}
