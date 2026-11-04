package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/console"
	"github.com/toasterbook88/axis/internal/persist"
)

func TestConsoleStartupRecordsSessionLogAndModelSwitch(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	res := readyResult("example-model", &stubBackend{name: "first"})
	res.ActiveTarget.ProviderName = "example-local"
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) { return res, nil }
	done := h.launch(t, defaultStartupInputs())
	h.prog.waitMsg(t, "ready", modelStateIs(console.ModelReady))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil || out.agent == nil {
		t.Fatalf("startup: %+v", out)
	}
	if err := out.agent.Run(context.Background(), "first console question"); err != nil {
		t.Fatal(err)
	}
	choice := ModelChoice{Model: "other-model", ProviderName: "example-remote", Protocol: agent.ProtocolCloud, SecurityClass: agent.BackendRemote}
	bindAgentModelChoice(out.agent, choice, &stubBackend{name: "second"}, nil)
	if err := out.agent.Run(context.Background(), "second console question"); err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob(filepath.Join(persist.AxisPath("logs"), "agent-*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	providers := []string{}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			var rec struct {
				Kind       string `json:"kind"`
				Provenance struct {
					Provider string `json:"provider"`
				} `json:"provenance"`
			}
			if err := json.Unmarshal([]byte(line), &rec); err != nil {
				t.Fatal(err)
			}
			if rec.Kind == "turn.finished" {
				providers = append(providers, rec.Provenance.Provider)
			}
		}
	}
	if len(providers) != 2 {
		t.Fatalf("console path recorded %d finished turns, want 2", len(providers))
	}
	if providers[0] != "example-local" || providers[1] != "example-remote" {
		t.Fatalf("console/model-switch provenance = %v", providers)
	}
}
