package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/events"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

func runCapability(t *testing.T, args ...string) (string, error) {
	t.Helper()
	cmd := capabilityCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs(args)
	err := cmd.Execute()
	return out.String(), err
}

var sampleHardware = hardwareReading{OS: "linux", Arch: "amd64", Cores: 8, RAMTotalMB: 16000, DiskTotalGB: 900}

func sampleFacts(r hardwareReading) *models.NodeFacts {
	return &models.NodeFacts{Name: "alpha", Hostname: "alpha.example", OS: r.OS, Arch: r.Arch,
		Resources: &models.Resources{CPUCores: r.Cores, RAMTotalMB: r.RAMTotalMB, DiskTotalGB: r.DiskTotalGB}}
}

// fakeHardware replaces the fact collector and the probe for one test.
func fakeHardware(t *testing.T, axis *models.NodeFacts, axisErr error, probe hardwareReading, probeErr error) {
	t.Helper()
	prevCollect, prevProbe := capabilityCollectLocal, capabilityProbeHardware
	capabilityCollectLocal = func(context.Context) (*models.NodeFacts, error) { return axis, axisErr }
	capabilityProbeHardware = func() (hardwareReading, string, error) { return probe, "test probe", probeErr }
	t.Cleanup(func() { capabilityCollectLocal, capabilityProbeHardware = prevCollect, prevProbe })
}

func useTempAxisHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)
	// Level changes reach the event log asynchronously, under this home. Flush
	// before the home is removed, or the write races the cleanup.
	t.Cleanup(func() { _ = events.FlushEvents(5 * time.Second) })
	return home
}

func TestCapabilityPromotionComesOnlyFromAxisCheckers(t *testing.T) {
	useTempAxisHome(t)
	fakeHardware(t, sampleFacts(sampleHardware), nil, sampleHardware, nil)
	for i := 1; i < capability.DefaultPromoteAfter; i++ {
		if out, err := runCapability(t, "check", "facts.hardware"); err != nil || strings.Contains(out, "promoted") {
			t.Fatalf("check %d: %q %v", i, out, err)
		}
	}
	out, err := runCapability(t, "check", "facts.hardware")
	if err != nil || !strings.Contains(out, "facts.hardware on node:alpha: pass") || !strings.Contains(out, "promoted: unproven -> advisory") {
		t.Fatalf("the promoting check printed %q, %v", out, err)
	}
	out, err = runCapability(t, "list", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var rep capability.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("list --format json is not JSON: %v\n%s", err, out)
	}
	if rep.JournalEntries != capability.DefaultPromoteAfter || len(rep.Capabilities) != 1 {
		t.Fatalf("report = %+v", rep)
	}
	e := rep.Capabilities[0]
	if e.Level != capability.LevelAdvisory || e.Actor != capability.DefaultActor || e.Totals.Pass != capability.DefaultPromoteAfter || !e.AtCeiling {
		t.Fatalf("entry = %+v", e)
	}
}

func TestCapabilityRecordedPassesNeverCount(t *testing.T) {
	useTempAxisHome(t)
	for i := 0; i < capability.DefaultPromoteAfter+5; i++ {
		out, err := runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "pass",
			"--evidence-kind", "live-probe", "--evidence-ref", "not-executed")
		if err != nil || !strings.Contains(out, "counted as inconclusive") || !strings.Contains(out, "never counts") {
			t.Fatalf("record pass %d printed %q, %v", i+1, out, err)
		}
	}
	out, err := runCapability(t, "show", "facts.hardware")
	if err != nil || !strings.Contains(out, "level      unproven") || !strings.Contains(out, "0 pass, 0 fail, 25 inconclusive") {
		t.Fatalf("show = %q, %v", out, err)
	}
	if _, err := runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "pass", "--actor", "agent:codex"); err == nil {
		t.Fatal("record must not accept --actor")
	}
}

func TestCapabilityFailuresCountFromAnywhere(t *testing.T) {
	useTempAxisHome(t)
	fakeHardware(t, sampleFacts(sampleHardware), nil, sampleHardware, nil)
	for i := 0; i < capability.DefaultPromoteAfter; i++ {
		if _, err := runCapability(t, "check", "facts.hardware"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "fail", "--severity", "critical",
		"--evidence-kind", "peer", "--evidence-ref", "an agent saw it fail")
	if err != nil || !strings.Contains(out, "counted as fail") || !strings.Contains(out, "ordinary failure") || !strings.Contains(out, "demoted: advisory -> unproven") {
		t.Fatalf("an outside failure printed %q, %v", out, err)
	}
}

func TestCapabilityCeilingVerifyAndExport(t *testing.T) {
	home := useTempAxisHome(t)
	out, err := runCapability(t, "ceiling", "facts.hardware", "supervised", "--reason", "operator raised it")
	if err != nil || !strings.Contains(out, "ceiling supervised") {
		t.Fatalf("ceiling = %q, %v", out, err)
	}
	if _, err := runCapability(t, "ceiling", "facts.hardware", "root"); err == nil {
		t.Fatal("an unknown level must be refused")
	}
	if out, err = runCapability(t, "verify"); err != nil || !strings.Contains(out, "journal verified: 1 entries") {
		t.Fatalf("verify = %q, %v", out, err)
	}
	out, err = runCapability(t, "export")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	var rep capability.Report
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("export is not JSON: %v\n%s", err, out)
	}
	if rep.Schema != capability.Schema || rep.JournalEntries != 1 || rep.JournalHead == "" || len(rep.Capabilities) != 1 {
		t.Fatalf("export = %+v", rep)
	}
	e := rep.Capabilities[0]
	if e.Ceiling != capability.LevelSupervised || e.Level != capability.LevelUnproven || e.Totals != (capability.Totals{}) ||
		e.NextLevel != capability.LevelAdvisory || e.PassesToGo != capability.DefaultPromoteAfter || e.LastTransition == nil || e.LastTransition.Kind != capability.TransitionCeiling {
		t.Fatalf("exported entry = %+v", e)
	}

	statePath := filepath.Join(home, "capability", "state.json")
	data, _ := os.ReadFile(statePath)
	_ = os.WriteFile(statePath, bytes.Replace(data, []byte(`"level": "unproven"`), []byte(`"level": "trusted"`), 1), 0o600)
	if out, err = runCapability(t, "verify"); err == nil || !strings.Contains(out, "journal check FAILED") {
		t.Fatalf("verify of an edited state = %q, %v", out, err)
	}
}

func TestCapabilityTransitionsReachTheEventLog(t *testing.T) {
	home := useTempAxisHome(t)
	if err := events.ResetTestLog(filepath.Join(home, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.ResetTestLog("") })
	fakeHardware(t, sampleFacts(sampleHardware), nil, sampleHardware, nil)
	for i := 0; i < capability.DefaultPromoteAfter; i++ {
		if _, err := runCapability(t, "check", "facts.hardware"); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := runCapability(t, "ceiling", "facts.hardware", "unproven"); err != nil {
		t.Fatal(err)
	}
	if err := events.FlushEvents(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	seen := map[string]map[string]any{}
	for _, e := range events.GetRecentEvents(500) {
		seen[e.Name] = e.Payload
	}
	if p := seen[events.EventCapabilityPromoted]; p == nil || p["to"] != "advisory" || p["capability"] != "facts.hardware" {
		t.Fatalf("promotion event = %v", p)
	}
	if p := seen[events.EventCapabilityCeilingChanged]; p == nil || p["to"] != "unproven" {
		t.Fatalf("ceiling event = %v", p)
	}
	if p := seen[events.EventCapabilityDemoted]; p == nil || p["from"] != "advisory" || p["to"] != "unproven" {
		t.Fatalf("demotion event = %v", p)
	}
}

func TestCapabilityShowAndBadInput(t *testing.T) {
	useTempAxisHome(t)
	if _, err := runCapability(t, "show", "facts.hardware"); err == nil {
		t.Fatal("show must fail for a capability with no record")
	}
	if _, err := runCapability(t, "record", "--capability", "Bad Name", "--outcome", "fail"); err == nil {
		t.Fatal("a bad capability name must be refused")
	}
	if _, err := runCapability(t, "record", "--capability", "facts.hardware"); err == nil {
		t.Fatal("record without --outcome must be refused")
	}
	if _, err := runCapability(t, "check", "health.everything"); err == nil {
		t.Fatal("check must refuse a capability with no built-in checker")
	}
}

func TestCapabilityRecordWithAHashedFileCanBeCritical(t *testing.T) {
	useTempAxisHome(t)
	fakeHardware(t, sampleFacts(sampleHardware), nil, sampleHardware, nil)
	if _, err := runCapability(t, "ceiling", "facts.hardware", "supervised"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2*capability.DefaultPromoteAfter; i++ {
		if _, err := runCapability(t, "check", "facts.hardware"); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "fail", "--severity", "critical",
		"--evidence-kind", "file", "--evidence-ref", "findings/audit.md", "--evidence-sha256", "be92cb38e3b69b75")
	if err != nil || !strings.Contains(out, "demoted: supervised -> unproven") || strings.Contains(out, "ordinary failure") {
		t.Fatalf("a critical failure backed by a hashed file must demote to unproven: %q, %v", out, err)
	}
	out, err = runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "fail", "--severity", "critical",
		"--evidence-kind", "file", "--evidence-ref", "findings/audit.md")
	if err != nil || !strings.Contains(out, "ordinary failure") {
		t.Fatalf("a file with no --evidence-sha256 is not grounded: %q, %v", out, err)
	}
}

func TestCapabilityStructuredOutput(t *testing.T) {
	useTempAxisHome(t)
	for _, format := range []string{"json", "yaml"} {
		out, err := runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "fail",
			"--evidence-kind", "peer", "--evidence-ref", "x", "--format", format)
		if err != nil || !strings.Contains(out, "record") || !strings.Contains(out, "facts.hardware") {
			t.Fatalf("record --format %s = %q, %v", format, out, err)
		}
	}
	out, err := runCapability(t, "record", "--capability", "facts.hardware", "--outcome", "pass", "--format", "json")
	var recorded struct {
		Record     capability.Record      `json:"record"`
		Transition *capability.Transition `json:"transition"`
	}
	if err != nil || json.Unmarshal([]byte(out), &recorded) != nil || recorded.Record.Totals != (capability.Totals{Fail: 2, Inconclusive: 1}) || recorded.Transition != nil {
		t.Fatalf("record --format json = %q, %v", out, err)
	}
	out, err = runCapability(t, "ceiling", "facts.hardware", "trusted", "--format", "json")
	var ceiling struct {
		Record      capability.Record       `json:"record"`
		Transitions []capability.Transition `json:"transitions"`
	}
	if err != nil || json.Unmarshal([]byte(out), &ceiling) != nil || ceiling.Record.Ceiling != capability.LevelTrusted ||
		len(ceiling.Transitions) != 1 || ceiling.Transitions[0].Kind != capability.TransitionCeiling {
		t.Fatalf("ceiling --format json = %q, %v", out, err)
	}
	out, err = runCapability(t, "show", "facts.hardware", "--format", "json")
	var entry capability.Entry
	if err != nil || json.Unmarshal([]byte(out), &entry) != nil || entry.Ceiling != capability.LevelTrusted || entry.Totals.Fail != 2 {
		t.Fatalf("show --format json = %q, %v", out, err)
	}
	if out, err = runCapability(t, "show", "facts.hardware", "--format", "yaml"); err != nil || !strings.Contains(out, "ceiling: trusted") {
		t.Fatalf("show --format yaml = %q, %v", out, err)
	}
	if out, err = runCapability(t, "verify", "--format", "json"); err != nil || !strings.Contains(out, "\"ok\": true") {
		t.Fatalf("verify --format json = %q, %v", out, err)
	}
}
