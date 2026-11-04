package main

import (
	"context"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/console"
)

// axis tui supplies its own program factory. Startup must build the program
// through it (not the standalone console), handing it the same options and
// then driving it like any console program.
func TestConsoleStartup_UsesInjectedProgram(t *testing.T) {
	h := newConsoleStartupHarness(t, nil)
	consoleSetupBackendFn = func(agentStartupBackendParams) (agentStartupBackendResult, error) {
		return readyResult("model-a", &stubBackend{name: "model-a"}), nil
	}
	var got console.Options
	injected := 0
	in := defaultStartupInputs()
	in.program = func(_ context.Context, opts console.Options) consoleProgram {
		injected++
		got = opts
		return h.prog
	}
	done := h.launch(t, in)
	h.prog.waitMsg(t, "model ready", modelStateIs(console.ModelReady))
	h.prog.exit()
	out := awaitOutcome(t, done, 5*time.Second)
	if out.err != nil {
		t.Fatalf("startup with injected program failed: %v", out.err)
	}
	if injected != 1 || h.programs.Load() != 0 {
		t.Fatalf("injected factory calls = %d, default factory calls = %d; want 1 and 0", injected, h.programs.Load())
	}
	if got.Submit == nil || got.Start == nil {
		t.Fatal("injected program did not receive the console options")
	}
}

func TestTUICommandFlags(t *testing.T) {
	cmd := tuiCmd()
	for _, name := range []string{"dashboard", "model", "autonomy"} {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("axis tui missing --%s", name)
		}
	}
}
