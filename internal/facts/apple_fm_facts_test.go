package facts

import (
	"context"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

func TestAppleFMFromProbeReady(t *testing.T) {
	got := appleFMFromProbe("27.2", `{"availability":"available","context_size":8192,"model":"AFM 3 Core Advanced","capabilities":["toolCalling","guidedGeneration","vision"],"languages":24}`, nil)
	if !got.Available || !got.Verified || got.State != models.AppleFMReady {
		t.Fatalf("got %+v, want available/verified/ready", got)
	}
	if got.ContextWindow != 8192 || got.Model != "AFM 3 Core Advanced" || !slices.Contains(got.Capabilities, "vision") || got.Version != "27.2" {
		t.Fatalf("facts = %+v", got)
	}
}

func TestAppleFMFromProbeModelNotReadyIsCold(t *testing.T) {
	got := appleFMFromProbe("27.2", `{"availability":"unavailable","reason":"modelNotReady"}`, nil)
	if got.Available || got.State != models.AppleFMCold || got.Reason != "modelNotReady" {
		t.Fatalf("got %+v, want cold/modelNotReady", got)
	}
}

func TestAppleFMFromProbeNotEnabledIsUnavailable(t *testing.T) {
	got := appleFMFromProbe("27.2", `{"availability":"unavailable","reason":"appleIntelligenceNotEnabled"}`, nil)
	if got.Available || got.State != models.AppleFMUnavailable || got.Reason != "appleIntelligenceNotEnabled" {
		t.Fatalf("got %+v", got)
	}
}

// A timeout says nothing about availability.
func TestAppleFMFromProbeTimeoutIsUnknown(t *testing.T) {
	got := appleFMFromProbe("27.2", "", context.DeadlineExceeded)
	if got.Available || got.State != models.AppleFMUnknown {
		t.Fatalf("got %+v, want unknown", got)
	}
	got = appleFMFromProbe("27.2", "", errors.New("Process exited with status 1: context deadline exceeded"))
	if got.State != models.AppleFMUnknown {
		t.Fatalf("got %+v, want unknown for wrapped deadline", got)
	}
}

// Marker outputs from the framework-only fallback and older helpers.
func TestAppleFMFromProbeMarkerOutputs(t *testing.T) {
	for _, out := range []string{"OK\n", "AVAILABLE\n", "OK\nOK\n"} {
		if got := appleFMFromProbe("27.2", out, nil); !got.Available || !got.Verified || got.State != models.AppleFMReady {
			t.Fatalf("%q -> %+v", out, got)
		}
	}
	if got := appleFMFromProbe("27.2", "UNAVAILABLE:modelNotReady", nil); got.Available || got.State != models.AppleFMCold || got.Reason != "modelNotReady" {
		t.Fatalf("UNAVAILABLE:modelNotReady -> %+v", got)
	}
	if got := appleFMFromProbe("27.2", "UNAVAILABLE:deviceNotEligible", nil); got.Available || got.State != models.AppleFMUnavailable {
		t.Fatalf("UNAVAILABLE:deviceNotEligible -> %+v", got)
	}
	if got := appleFMFromProbe("27.2", "UNVERIFIED", nil); got.Available || got.State != models.AppleFMUnknown || !strings.Contains(got.Error, "unverified") {
		t.Fatalf("UNVERIFIED -> %+v", got)
	}
	if got := appleFMFromProbe("27.2", "OK\nUNAVAILABLE:modelNotReady\nOK\n", nil); got.Available || got.Verified {
		t.Fatalf("mixed markers -> %+v, want not available", got)
	}
}

// The checked-in helper is the embedded helper; they must not drift.
func TestAppleFMHelperSourceMatchesHackCopy(t *testing.T) {
	hack, err := os.ReadFile("../../hack/apple-foundation-models.swift")
	if err != nil {
		t.Fatal(err)
	}
	if string(hack) != appleFoundationModelsHelperSource {
		t.Fatal("hack/apple-foundation-models.swift differs from appleFoundationModelsHelperSource")
	}
}

func TestAppleFMHelperEmbedsFactsFunction(t *testing.T) {
	if !strings.Contains(appleFoundationModelsHelperSource, appleFoundationModelsFactsSwift) {
		t.Fatal("helper source must contain appleFoundationModelsFactsSwift verbatim")
	}
	// The facts body also travels inside a single-quoted xcrun swift -e argument.
	if strings.Contains(appleFoundationModelsFactsSwift, "'") {
		t.Fatal("appleFoundationModelsFactsSwift must not contain single quotes")
	}
}

// Sweeps never generate: the discovery script runs --facts with no stdin,
// never --self-test. An older cached helper reads a prompt from stdin.
func TestAppleFMDiscoveryScriptNeverGenerates(t *testing.T) {
	if !strings.Contains(AppleFoundationModelsDiscoveryScript, "--facts </dev/null") || strings.Contains(AppleFoundationModelsDiscoveryScript, "--self-test") {
		t.Fatal("discovery script must run --facts </dev/null and never --self-test")
	}
}

// A disk scan that spends the node deadline must not starve the Apple probe:
// on a fleet Mac the scan ran past the deadline and the probe, run after it,
// reported a timeout. Apple facts are read right after the core bundle.
func TestRemoteCollectorReadsAppleFactsBeforeDiskScan(t *testing.T) {
	bundle := "__AXIS_BUNDLE_V1__\nos=Darwin\narch=arm64\nos_version=27.2\nhostname=mac\ntool_swift=/usr/bin/swift\n__AXIS_BUNDLE_END__\n"
	exec := &fakeRemoteExecutor{exact: map[string]fakeRunResult{
		remoteFactBundleScript:               {out: bundle},
		AppleFoundationModelsDiscoveryScript: {out: `{"availability":"available","context_size":8192,"model":"AFM 3 Core"}`},
		OllamaDiscoveryScript:                {out: `{"installed":false}`},
		LlamaServerDiscoveryScript:           {out: `{"installed":false}`},
		MLXDiscoveryScript:                   {out: `{"installed":false}`},
		DiskWeightsDiscoveryScript:           {err: context.DeadlineExceeded},
	}}
	facts, err := NewRemoteCollector("mac", "worker", "mac.example.com", exec).Collect(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if facts.AppleFM == nil || facts.AppleFM.State != models.AppleFMReady || facts.AppleFM.ContextWindow != 8192 {
		t.Fatalf("apple = %+v, want ready with context 8192", facts.AppleFM)
	}
	index := func(script string) int {
		return slices.IndexFunc(exec.runs, func(cmd string) bool { return cmd == script || cmd == transport.WrapBash(script) })
	}
	apple, disk := index(AppleFoundationModelsDiscoveryScript), index(DiskWeightsDiscoveryScript)
	if apple < 0 || disk < 0 || apple > disk {
		t.Fatalf("apple probe at %d, disk scan at %d; want apple first", apple, disk)
	}
}

func TestRemoteCollectorAppleFactsReadyAndTimeout(t *testing.T) {
	run := func(res fakeRunResult) *models.NodeFacts {
		t.Helper()
		exec := &fakeRemoteExecutor{exact: map[string]fakeRunResult{AppleFoundationModelsDiscoveryScript: res}}
		facts := &models.NodeFacts{OS: "darwin", Arch: "arm64", OSVersion: "27.2", Tools: []models.ToolInfo{{Name: "swift", Path: "/usr/bin/swift"}}}
		NewRemoteCollector("mac", "worker", "mac.example.com", exec).discoverAppleFoundationModels(context.Background(), facts)
		return facts
	}
	hasTool := func(f *models.NodeFacts) bool {
		return slices.ContainsFunc(f.Tools, func(tool models.ToolInfo) bool { return tool.Name == "apple-foundation-models" })
	}

	ready := run(fakeRunResult{out: `{"availability":"available","context_size":4096,"model":"AFM 3 Core"}` + "\n"})
	if ready.AppleFM.State != models.AppleFMReady || ready.AppleFM.ContextWindow != 4096 || !hasTool(ready) {
		t.Fatalf("ready = %+v tools %+v", ready.AppleFM, ready.Tools)
	}
	timedOut := run(fakeRunResult{err: context.DeadlineExceeded})
	if timedOut.AppleFM.State != models.AppleFMUnknown || timedOut.AppleFM.Available || hasTool(timedOut) {
		t.Fatalf("timeout = %+v tools %+v", timedOut.AppleFM, timedOut.Tools)
	}
}
