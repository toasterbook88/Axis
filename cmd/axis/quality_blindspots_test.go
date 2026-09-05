package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/ui"
)

// Characterization rows for the three PR B blindspots:
//   - runMCPClientInteractive (was 0% cover)
//   - printFactsText (was 16.3%)
//   - formatToolResultSummary (was 13.0%)
//
// Test-only: no refactors. Each row locks today's observable output.

// --- runMCPClientInteractive ---

func stubMCPConfigForInteractive(t *testing.T) {
	previous := loadMCPClientConfig
	t.Cleanup(func() { loadMCPClientConfig = previous })
	loadMCPClientConfig = func(string) (*config.Config, error) {
		return &config.Config{MCPServers: map[string]config.MCPServerConfig{}}, nil
	}
}

func TestCharMCPInteractiveQuit(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	err := runMCPClientInteractive(context.Background(), strings.NewReader("quit\n"), &out)
	if err != nil {
		t.Fatalf("quit: %v", err)
	}
	outStr := out.String()
	if !strings.Contains(outStr, "AXIS MCP Client Interactive REPL") {
		t.Errorf("banner missing, got %q", outStr)
	}
	if !strings.Contains(outStr, "Commands: tools, resources, prompts") {
		t.Errorf("command list missing, got %q", outStr)
	}
	if !strings.Contains(outStr, "Bye.") {
		t.Errorf("quit output missing, got %q", outStr)
	}
}

func TestCharMCPInteractiveExitAliases(t *testing.T) {
	stubMCPConfigForInteractive(t)
	for _, input := range []string{"exit\n", "q\n"} {
		var out bytes.Buffer
		err := runMCPClientInteractive(context.Background(), strings.NewReader(input), &out)
		if err != nil {
			t.Fatalf("%q: %v", input, err)
		}
		if !strings.Contains(out.String(), "Bye.") {
			t.Errorf("%q: expected Bye., got %q", input, out.String())
		}
	}
}

func TestCharMCPInteractiveHelp(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	err := runMCPClientInteractive(context.Background(), strings.NewReader("help\nquit\n"), &out)
	if err != nil {
		t.Fatalf("help: %v", err)
	}
	outStr := out.String()
	for _, fragment := range []string{
		"List connected servers", "List tools", "List resources",
		"Call a tool", "Read a resource", "Search tools", "Exit REPL",
	} {
		if !strings.Contains(outStr, fragment) {
			t.Errorf("help output missing %q", fragment)
		}
	}
}

func TestCharMCPInteractiveEmptyAndUnknown(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	// Blank line is skipped silently; unknown command gets the captured message.
	err := runMCPClientInteractive(context.Background(), strings.NewReader("\n\nbogus\nquit\n"), &out)
	if err != nil {
		t.Fatalf("unknown cmd: %v", err)
	}
	if !strings.Contains(out.String(), "Unknown command: bogus") {
		t.Errorf("unknown command output, got %q", out.String())
	}
}

func TestCharMCPInteractiveListEmptyRegistry(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	// Empty MCPServers -> registry connects nothing -> list prints nothing
	// but the prompt/banner still flow. Lock that it does not error.
	err := runMCPClientInteractive(context.Background(), strings.NewReader("list\nquit\n"), &out)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if !strings.Contains(out.String(), "mcp> ") {
		t.Errorf("prompt missing, got %q", out.String())
	}
}

func TestCharMCPInteractiveEOF(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	// Input ends without quit: scanner EOF terminates cleanly with nil error.
	err := runMCPClientInteractive(context.Background(), strings.NewReader(""), &out)
	if err != nil {
		t.Fatalf("EOF: %v", err)
	}
	if !strings.Contains(out.String(), "AXIS MCP Client Interactive REPL") {
		t.Errorf("EOF run must still print banner, got %q", out.String())
	}
}

func TestCharMCPInteractiveBadJSONArgs(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	// call with malformed JSON args: ParseArgs error is printed, loop continues.
	err := runMCPClientInteractive(context.Background(),
		strings.NewReader("call s t {bad-json}\nquit\n"), &out)
	if err != nil {
		t.Fatalf("call bad json: %v", err)
	}
	if !strings.Contains(out.String(), "Error:") {
		t.Errorf("ParseArgs error output missing, got %q", out.String())
	}
}

func TestCharMCPInteractiveCallUsage(t *testing.T) {
	stubMCPConfigForInteractive(t)
	var out bytes.Buffer
	err := runMCPClientInteractive(context.Background(),
		strings.NewReader("call\nread\nget-prompt\nsearch\nquit\n"), &out)
	if err != nil {
		t.Fatalf("usage paths: %v", err)
	}
	outStr := out.String()
	for _, want := range []string{
		"Usage: call <server> <tool> [json-args]",
		"Usage: read <server> <uri>",
		"Usage: get-prompt <server> <prompt> [json-args]",
	} {
		if !strings.Contains(outStr, want) {
			t.Errorf("usage output missing %q", want)
		}
	}
}

// --- printFactsText ---

func newFactsTestCmdShim(out *bytes.Buffer) *cobra.Command {
	cmd := &cobra.Command{Use: "facts"}
	cmd.SetOut(out)
	return cmd
}

func charFactsNode() *models.NodeFacts {
	return &models.NodeFacts{
		Name:        "test-node",
		Hostname:    "test-host",
		OS:          "linux",
		OSVersion:   "24.04",
		Arch:        "amd64",
		Status:      models.StatusComplete,
		CollectedAt: time.Now(),
		Resources: &models.Resources{
			CPUCores:    8,
			CPUModel:    "Test CPU",
			RAMTotalMB:  16000,
			RAMFreeMB:   8000,
			DiskFreeGB:  100,
			DiskTotalGB: 500,
			Pressure:    "low",
			Load1M:      1.5, Load5M: 1.2, Load15M: 0.9,
		},
	}
}

func TestCharPrintFactsTextBasic(t *testing.T) {
	nf := charFactsNode()
	var out bytes.Buffer
	if err := printFactsText(newFactsTestCmdShim(&out), nf, false); err != nil {
		t.Fatalf("printFactsText: %v", err)
	}
	outStr := ui.StripANSIAndControls(out.String())
	for _, want := range []string{
		"NODE FACTS", "test-node", "test-host", "linux 24.04", "amd64",
		"Resources", "cpu:", "8 cores (Test CPU)", "ram total:", "16000 MB",
		"ram free:", "8000 MB", "disk:", "100 GB free / 500 GB total",
		"load:", "1.50 / 1.20 / 0.90", "collected:",
	} {
		if !strings.Contains(outStr, want) {
			t.Errorf("facts output missing %q", want)
		}
	}
}

func TestCharPrintFactsTextMinimalNode(t *testing.T) {
	// nil Resources: only the header block renders, no Resources section.
	nf := &models.NodeFacts{
		Name: "bare", Hostname: "bare-host", OS: "darwin", Arch: "arm64",
		Status:      models.StatusComplete,
		CollectedAt: time.Now(),
	}
	var out bytes.Buffer
	if err := printFactsText(newFactsTestCmdShim(&out), nf, false); err != nil {
		t.Fatalf("minimal: %v", err)
	}
	outStr := ui.StripANSIAndControls(out.String())
	if !strings.Contains(outStr, "bare") || !strings.Contains(outStr, "darwin") || !strings.Contains(outStr, "arm64") {
		t.Errorf("minimal header missing, got %q", outStr)
	}
	if strings.Contains(outStr, "ram total:") {
		t.Errorf("nil Resources must not render resources, got %q", outStr)
	}
}

func TestCharPrintFactsTextAddresses(t *testing.T) {
	nf := charFactsNode()
	nf.Addresses = []models.NetworkAddress{
		{Address: "192.0.2.10", Kind: "ipv4", Interface: "eth0"},
		{Address: "fe80::1", Kind: "ipv6", Scope: "link-local"},
		{Address: "100.64.0.5", Kind: "ipv4", SpeedClass: "tailscale"},
		{Address: "2001:db8::5", Kind: "ipv6", Scope: "global"},
	}
	var out bytes.Buffer
	if err := printFactsText(newFactsTestCmdShim(&out), nf, false); err != nil {
		t.Fatalf("addresses non-verbose: %v", err)
	}
	nonVerbose := ui.StripANSIAndControls(out.String())
	if !strings.Contains(nonVerbose, "192.0.2.10") {
		t.Error("ipv4 always shown")
	}
	if !strings.Contains(nonVerbose, "100.64.0.5") {
		t.Error("tailscale class always shown")
	}
	if !strings.Contains(nonVerbose, "more addresses") {
		t.Errorf("hidden-count hint missing in non-verbose, got %q", nonVerbose)
	}
	if strings.Contains(nonVerbose, "fe80::1") {
		t.Error("link-local must be hidden in non-verbose")
	}

	// verbose shows link-local too
	out.Reset()
	if err := printFactsText(newFactsTestCmdShim(&out), nf, true); err != nil {
		t.Fatalf("addresses verbose: %v", err)
	}
	verbose := ui.StripANSIAndControls(out.String())
	if !strings.Contains(verbose, "fe80::1") {
		t.Errorf("verbose must show link-local, got %q", verbose)
	}
	if !strings.Contains(verbose, "2001:db8::5") {
		t.Errorf("verbose must show global ipv6, got %q", verbose)
	}
}

func TestCharPrintFactsTextOllamaAndTools(t *testing.T) {
	nf := charFactsNode()
	nf.Tools = []models.ToolInfo{{Name: "axis", Version: "0.17.0"}, {Name: "git"}}
	nf.Ollama = &models.OllamaInfo{Installed: true, Running: true, Models: []string{"qwen3:1.7b", "gemma3:1b"}}
	var out bytes.Buffer
	if err := printFactsText(newFactsTestCmdShim(&out), nf, false); err != nil {
		t.Fatalf("ollama+tools: %v", err)
	}
	outStr := ui.StripANSIAndControls(out.String())
	if !strings.Contains(outStr, "Tools") || !strings.Contains(outStr, "axis 0.17.0") {
		t.Errorf("tools section, got %q", outStr)
	}
	if !strings.Contains(outStr, "Ollama") || !strings.Contains(outStr, "running:") || !strings.Contains(outStr, "true") {
		t.Errorf("ollama section, got %q", outStr)
	}
	if !strings.Contains(outStr, "qwen3:1.7b, gemma3:1b") {
		t.Errorf("ollama models list, got %q", outStr)
	}
}

func TestCharPrintFactsTextGPUAndThermal(t *testing.T) {
	nf := charFactsNode()
	nf.Resources.GPUs = []models.GPUInfo{
		{Vendor: "nvidia", Model: "NVIDIA GeForce RTX 5060", VRAMMB: 8074, Capabilities: []string{"cuda"}},
	}
	nf.Resources.ThermalState = "nominal"
	nf.Resources.BatteryPercent = ptrInt(87)
	var out bytes.Buffer
	if err := printFactsText(newFactsTestCmdShim(&out), nf, false); err != nil {
		t.Fatalf("gpu/thermal: %v", err)
	}
	outStr := ui.StripANSIAndControls(out.String())
	if !strings.Contains(outStr, "RTX 5060") || !strings.Contains(outStr, "8074 MB VRAM") {
		t.Errorf("gpu row, got %q", outStr)
	}
	if !strings.Contains(outStr, "[cuda]") {
		t.Errorf("gpu capabilities, got %q", outStr)
	}
	if !strings.Contains(outStr, "nominal") {
		t.Errorf("thermal row, got %q", outStr)
	}
	if !strings.Contains(outStr, "battery:") || !strings.Contains(outStr, "87%") {
		t.Errorf("battery row, got %q", outStr)
	}
}

func ptrInt(i int) *int { return &i }
