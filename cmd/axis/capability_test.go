package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	prevCollect, prevProbe, prevNodes := capabilityCollectLocal, capabilityProbeHardware, capabilitySnapshotNodes
	capabilityCollectLocal = func(context.Context) (*models.NodeFacts, error) { return axis, axisErr }
	capabilityProbeHardware = func() (hardwareReading, string, error) { return probe, "test probe", probeErr }
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) { return nil, errors.New("no snapshot in tests") }
	t.Cleanup(func() {
		capabilityCollectLocal, capabilityProbeHardware, capabilitySnapshotNodes = prevCollect, prevProbe, prevNodes
	})
}

func useTempAxisHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)
	// Level changes reach the event log asynchronously, under this home. Flush
	// before the home is removed, or the write races the cleanup.
	t.Cleanup(func() { _ = events.FlushEvents(5 * time.Second) })
	// Each entry an hour after the last, so a streak covers real time.
	prev, clock := capability.Clock, time.Date(2026, 11, 16, 12, 0, 0, 0, time.UTC)
	capability.Clock = func() time.Time { clock = clock.Add(time.Hour); return clock }
	t.Cleanup(func() { capability.Clock = prev })
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
	if err != nil || !strings.Contains(out, "counted as fail") || !strings.Contains(out, "counts as one level") || !strings.Contains(out, "demoted: advisory -> unproven") {
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

func TestCapabilityRecordedFailuresAreNeverCritical(t *testing.T) {
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
		"--evidence-kind", "file", "--evidence-ref", "findings/audit.md", "--evidence-sha256", "made-up")
	if err != nil || !strings.Contains(out, "demoted: supervised -> advisory") || !strings.Contains(out, "counts as one level") {
		t.Fatalf("an outside critical failure, even with a hash, must demote one level: %q, %v", out, err)
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
	if out, err = runCapability(t, "list", "--format", " JSON "); err != nil || !strings.HasPrefix(strings.TrimSpace(out), "{") {
		t.Fatalf("list --format \" JSON \" = %q, %v; want JSON", out, err)
	}
	for _, args := range [][]string{{"list"}, {"show", "facts.hardware"}, {"export"}} {
		if _, err := runCapability(t, append(args, "--format", "jsoon")...); err == nil || !strings.Contains(err.Error(), "invalid --format") {
			t.Fatalf("%v --format jsoon = %v; want an invalid-format error", args, err)
		}
	}
}

func TestCapabilityResolveCommand(t *testing.T) {
	home := useTempAxisHome(t)
	if err := events.ResetTestLog(filepath.Join(home, "events.jsonl")); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = events.ResetTestLog("") })
	bad := sampleHardware
	bad.Cores = 4
	fakeHardware(t, sampleFacts(bad), nil, sampleHardware, nil)
	if out, err := runCapability(t, "check", "facts.hardware"); err != nil || !strings.Contains(out, "fail") {
		t.Fatalf("check = %q, %v", out, err)
	}
	if _, err := runCapability(t, "resolve", "facts.hardware", "node:alpha"); err == nil {
		t.Fatal("resolve without --reason must be refused")
	}
	out, err := runCapability(t, "resolve", "facts.hardware", "node:ALPHA", "--reason", "machine retired")
	if err != nil || !strings.Contains(out, "closed the open failure on node:alpha") {
		t.Fatalf("resolve with the name in another case = %q, %v", out, err)
	}
	if err := events.FlushEvents(5 * time.Second); err != nil {
		t.Fatal(err)
	}
	resolved := false
	for _, e := range events.GetRecentEvents(100) {
		resolved = resolved || (e.Name == events.EventCapabilityFailureResolved && e.Payload["capability"] == "facts.hardware")
	}
	if !resolved {
		t.Fatal("resolve must emit capability.failure.resolved")
	}
	if out, err = runCapability(t, "show", "facts.hardware"); err != nil || strings.Contains(out, "failing ") {
		t.Fatalf("show after resolve = %q, %v", out, err)
	}
	if out, err = runCapability(t, "verify"); err != nil || !strings.Contains(out, "journal verified: 2 entries") {
		t.Fatalf("verify = %q, %v", out, err)
	}
}

func TestALocalCheckUsesTheSnapshotNameOfThisMachine(t *testing.T) {
	useTempAxisHome(t)
	facts := sampleFacts(sampleHardware)
	facts.Hostname = "alpha-workstation.example"
	fakeHardware(t, facts, nil, sampleHardware, nil)
	prev := capabilitySnapshotNodes
	t.Cleanup(func() { capabilitySnapshotNodes = prev })
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) {
		return []models.NodeFacts{{Name: "Alpha", Hostname: "alpha-workstation"}}, nil
	}
	if obs := checkFactsHardware(context.Background(), ""); obs.Subject != "node:alpha" {
		t.Fatalf("subject = %q, want the snapshot name node:alpha", obs.Subject)
	}
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) { return nil, errors.New("no daemon") }
	if obs := checkFactsHardware(context.Background(), ""); obs.Subject != "node:alpha-workstation" {
		t.Fatalf("subject without a snapshot = %q, want the hostname", obs.Subject)
	}
}

// A snapshot node whose hostname only shares this machine's first label is
// another machine, so a local check does not take its name.
func TestALocalCheckIgnoresANodeSharingOnlyTheFirstLabel(t *testing.T) {
	useTempAxisHome(t)
	facts := sampleFacts(sampleHardware)
	facts.Hostname = "alpha-workstation.example"
	fakeHardware(t, facts, nil, sampleHardware, nil)
	prev := capabilitySnapshotNodes
	t.Cleanup(func() { capabilitySnapshotNodes = prev })
	capabilitySnapshotNodes = func(context.Context) ([]models.NodeFacts, error) {
		return []models.NodeFacts{{Name: "beta", Hostname: "alpha-workstation.remote.example"}}, nil
	}
	if obs := checkFactsHardware(context.Background(), ""); obs.Subject != "node:alpha-workstation" {
		t.Fatalf("subject = %q, want the hostname, not another machine's name", obs.Subject)
	}
}

func TestCapabilityCheckJSONMatchesTheJournal(t *testing.T) {
	home := useTempAxisHome(t)
	fakeHardware(t, sampleFacts(sampleHardware), nil, sampleHardware, nil)
	out, err := runCapability(t, "check", "facts.hardware", "--format", "json")
	if err != nil {
		t.Fatal(err)
	}
	var result struct {
		Observation capability.Observation `json:"observation"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(home, "capability", "journal.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	var entry capability.JournalEntry
	if err := json.Unmarshal(b, &entry); err != nil {
		t.Fatal(err)
	}
	if result.Observation != *entry.Observation || result.Observation.At.IsZero() || result.Observation.Counted == "" {
		t.Fatalf("check --format json printed %+v; the journal holds %+v", result.Observation, *entry.Observation)
	}
}
