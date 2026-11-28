package main

import (
	"bytes"
	"encoding/json"
	"os"
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
