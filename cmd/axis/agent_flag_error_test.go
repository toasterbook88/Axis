package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/agent"
	"github.com/toasterbook88/axis/internal/execution"
	"github.com/toasterbook88/axis/internal/safety"
)

// runRoot runs the real root command through executeRoot, as main does.
func runRoot(t *testing.T, args ...string) (stdout string, err error) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	root := newRootCmd()
	var out, errBuf bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&errBuf)
	err = executeRoot(root, args)
	return out.String(), err
}

func decodeOneDocument(t *testing.T, stdout string) map[string]any {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(stdout))
	var doc map[string]any
	if err := dec.Decode(&doc); err != nil {
		t.Fatalf("stdout is not a JSON document: %v\n%q", err, stdout)
	}
	if dec.More() {
		t.Fatalf("stdout holds more than one document: %q", stdout)
	}
	return doc
}

func TestRootAgentFlagErrorsWriteFailedDocument(t *testing.T) {
	cases := map[string][]string{
		"malformed typed value":  {"agent", "-p", "--format", "json", "--max-turns", "nope", "check"},
		"unknown flag":           {"agent", "-p", "--format", "json", "--bogus-flag", "check"},
		"unknown flag before -p": {"agent", "--bogus-flag", "-p", "--format=json", "check"},
		"shorthand cluster -sp":  {"agent", "-sp", "--format", "json", "--max-turns", "nope", "check"},
		"root flag before agent": {"--no-color", "agent", "--print", "--format", "json", "--bogus", "check"},
		"format in capitals":     {"agent", "-p", "--format", "JSON", "--max-turns", "nope", "check"},
		"format with spaces":     {"agent", "-p", "--format", " json", "--bogus", "check"},
		"unknown shorthand -xp":  {"agent", "-xp", "--format", "json", "check"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, err := runRoot(t, args...)
			if err == nil {
				t.Fatal("want a flag error")
			}
			if code := ExitCode(err); code != ExitErrGeneric {
				t.Fatalf("exit code %d, want %d", code, ExitErrGeneric)
			}
			doc := decodeOneDocument(t, stdout)
			data, _ := doc["data"].(map[string]any)
			if doc["status"] != "failed" || data["outcome"] != "failed" || data["turns"] != float64(0) {
				t.Fatalf("document = %v, want a failed outcome with turns 0", doc)
			}
			if data["max_turns"] != float64(25) {
				t.Fatalf("max_turns = %v, want the default 25", data["max_turns"])
			}
			if msg, _ := data["error"].(string); msg != err.Error() {
				t.Fatalf("document error %q, want %q", msg, err.Error())
			}
		})
	}
}

func TestRootAgentFlagErrorsWithoutJSONWriteNothing(t *testing.T) {
	cases := map[string][]string{
		"text format":         {"agent", "-p", "--max-turns", "nope", "check"},
		"json without -p":     {"agent", "--format", "json", "--bogus", "check"},
		"later format wins":   {"agent", "-p", "--format", "json", "--format", "text", "--bogus", "check"},
		"-p=false":            {"agent", "-p=false", "--format", "json", "--bogus", "check"},
		"-p is a model value": {"agent", "--model", "-p", "--format", "json", "--bogus", "check"},
		"flags after -- only": {"agent", "--bogus", "--", "-p", "--format", "json"},
	}
	for name, args := range cases {
		t.Run(name, func(t *testing.T) {
			stdout, err := runRoot(t, args...)
			if err == nil {
				t.Fatal("want a flag error")
			}
			if stdout != "" {
				t.Fatalf("stdout = %q, want nothing", stdout)
			}
		})
	}
}

// An error RunE returns already writes the document; executeRoot must not
// add a second one.
func TestRootAgentRunEErrorWritesOneDocument(t *testing.T) {
	stdout, err := runRoot(t, "agent", "-p", "--format", "json", "--max-turns", "0", "check")
	if err == nil {
		t.Fatal("want the --max-turns refusal")
	}
	decodeOneDocument(t, stdout)
}

// The "none" clears value rests on guarded execution applying the same rule
// set as the agent's safety gate: a command the gate scores 100 must be a deny
// on the guarded run_shell surface too.
func TestHardDenyIsAlsoGuardedDeny(t *testing.T) {
	ev := safety.NewEvaluator(safety.DefaultRuleSet())
	for _, cmd := range []string{"rm -rf /", "mkfs.ext4 /dev/sda1", "sudo reboot"} {
		allowed, _, score := agent.DefaultSafetyGate(nil)(cmd)
		if allowed || score != 100 {
			t.Fatalf("%q: gate allowed=%v score=%d, want a deny at 100", cmd, allowed, score)
		}
		for _, surface := range []string{execution.OwnerSurfaceAgentRunShell, execution.OwnerSurfaceAgentRunOnNode} {
			if d := ev.Evaluate(cmd, surface); d.Verdict != safety.VerdictDeny {
				t.Fatalf("%q on %s: guarded verdict %q, want deny", cmd, surface, d.Verdict)
			}
		}
	}
}

// TestMainWritesDocumentOnFlagError runs main itself in a child process, so
// main's wiring to executeRoot is covered, not just executeRoot.
func TestMainWritesDocumentOnFlagError(t *testing.T) {
	if os.Getenv("AXIS_TEST_RUN_MAIN") == "1" {
		os.Args = append([]string{"axis"}, strings.Fields(os.Getenv("AXIS_TEST_MAIN_ARGS"))...)
		main()
		return
	}
	child := exec.Command(os.Args[0], "-test.run=^TestMainWritesDocumentOnFlagError$")
	child.Env = append(os.Environ(), "AXIS_TEST_RUN_MAIN=1",
		"AXIS_TEST_MAIN_ARGS=agent -p --format json --bogus-flag check", "HOME="+t.TempDir())
	var out bytes.Buffer
	child.Stdout = &out
	err := child.Run()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) || exitErr.ExitCode() != ExitErrGeneric {
		t.Fatalf("child exit = %v, want exit code %d", err, ExitErrGeneric)
	}
	doc := decodeOneDocument(t, out.String())
	if doc["status"] != "failed" {
		t.Fatalf("document = %v, want status failed", doc)
	}
}

func TestStartupPickerNeedsTerminalAtBothEnds(t *testing.T) {
	dir := t.TempDir()
	tty, err := os.Create(dir + "/tty")
	if err != nil {
		t.Fatal(err)
	}
	defer tty.Close()
	plain, err := os.Create(dir + "/plain")
	if err != nil {
		t.Fatal(err)
	}
	defer plain.Close()
	prev := isTerminalFile
	t.Cleanup(func() { isTerminalFile = prev })
	isTerminalFile = func(f *os.File) bool { return f == tty }

	cases := []struct {
		name string
		p    agentStartupBackendParams
		def  bool
		want bool
	}{
		{"terminal stdin and output", agentStartupBackendParams{In: tty, Out: tty}, false, true},
		{"output redirected to a file", agentStartupBackendParams{In: tty, Out: plain}, false, false},
		{"output is a buffer", agentStartupBackendParams{In: tty, Out: &bytes.Buffer{}}, false, false},
		{"stdin redirected", agentStartupBackendParams{In: plain, Out: tty}, false, false},
		{"--model given", agentStartupBackendParams{In: tty, Out: tty, Model: "qwen"}, false, false},
		{"default model configured", agentStartupBackendParams{In: tty, Out: tty}, true, false},
		{"--select with output redirected", agentStartupBackendParams{In: plain, Out: &bytes.Buffer{}, SelectModel: true}, false, true},
	}
	for _, c := range cases {
		if got := startupPickerWanted(c.p, c.def); got != c.want {
			t.Errorf("%s: startupPickerWanted = %v, want %v", c.name, got, c.want)
		}
	}
}

func TestWriterIsTerminalRejectsNonTerminals(t *testing.T) {
	if writerIsTerminal(&bytes.Buffer{}) {
		t.Fatal("a buffer is not a terminal")
	}
	f, err := os.CreateTemp(t.TempDir(), "out")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if writerIsTerminal(f) {
		t.Fatal("a regular file is not a terminal")
	}
}
