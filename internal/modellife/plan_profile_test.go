package modellife

import (
	"reflect"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPlanStartDefaultArgvIsExact(t *testing.T) {
	plan, err := PlanStart(storageNode(), "/mnt/models/a.gguf", 8081)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/llama-server", "-m", "/mnt/models/a.gguf", "--port", "8081", "--host", "127.0.0.1"}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("argv=%#v", plan.Argv)
	}
	if plan.Profile.Engine != "llama.cpp" || plan.Profile.PortSource != models.PortSourceExplicit || plan.Profile.BindHost != "127.0.0.1" {
		t.Fatalf("profile=%+v", plan.Profile)
	}
	if plan.Profile.NGPULayers != nil || plan.Profile.ContextTokens != nil {
		t.Fatalf("default profile must omit optional flags: %+v", plan.Profile)
	}
}

func TestPlanStartProfileAppendsOptionalFlagsAfterBase(t *testing.T) {
	node := storageNode()
	node.Resources.CPUCores = 8
	node.Resources.GPUs = []models.GPUInfo{{
		Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
		VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
	}}
	ctx, ngl, batch, ubatch, threads := 4096, 20, 512, 256, 4
	profile := models.ModelRunProfile{
		Schema:        models.ModelRunSchema,
		Node:          node.Name,
		Engine:        "llama.cpp",
		ToolName:      "llama-server",
		ArtifactKind:  "weights-path",
		WeightsPath:   "/mnt/models/a.gguf",
		BindHost:      "127.0.0.1",
		Port:          8081,
		PortSource:    models.PortSourceExplicit,
		ContextTokens: &ctx,
		NGPULayers:    &ngl,
		BatchSize:     &batch,
		UBatchSize:    &ubatch,
		Threads:       &threads,
	}
	plan, err := PlanStartProfile(node, profile)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"/usr/local/bin/llama-server", "-m", "/mnt/models/a.gguf", "--port", "8081", "--host", "127.0.0.1",
		"-c", "4096", "-ngl", "20", "-b", "512", "-ub", "256", "-t", "4",
	}
	if !reflect.DeepEqual(plan.Argv, want) {
		t.Fatalf("argv=%#v", plan.Argv)
	}
	if !reflect.DeepEqual(plan.Argv, mustArgv(t, plan.Profile)) {
		t.Fatal("stored profile does not project the argv that will exec")
	}
}

func TestPlanStartProfileNGPULayersModeAndRefusals(t *testing.T) {
	node := storageNode()
	node.Resources.GPUs = []models.GPUInfo{{
		Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
		VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
	}}
	profile := readyProfile(node)
	profile.NGPULayersMode = "all"
	plan, err := PlanStartProfile(node, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Argv[len(plan.Argv)-2:], []string{"-ngl", "all"}) {
		t.Fatalf("argv=%#v", plan.Argv)
	}

	unmeasured := storageNode()
	unmeasured.Resources.GPUs = []models.GPUInfo{{Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576, Capabilities: []string{"cuda"}}}
	profile = readyProfile(unmeasured)
	profile.NGPULayersMode = "auto"
	if _, err := PlanStartProfile(unmeasured, profile); err == nil || !strings.Contains(err.Error(), "measured free VRAM") {
		t.Fatalf("unmeasured err=%v", err)
	}

	unified := storageNode()
	unified.Resources.MemoryTopology = models.MemoryTopologyUnified
	unified.Resources.GPUs = []models.GPUInfo{{
		Vendor: "nvidia", Model: "RTX 4090", VRAMMB: 24576,
		VRAMFreeMB: 20000, VRAMFreeMeasured: true, Capabilities: []string{"cuda"},
	}}
	profile = readyProfile(unified)
	profile.NGPULayersMode = "auto"
	if _, err := PlanStartProfile(unified, profile); err == nil || !strings.Contains(err.Error(), "measured free VRAM") {
		t.Fatalf("unified err=%v", err)
	}

	cpu := storageNode()
	profile = readyProfile(cpu)
	profile.NGPULayersMode = "auto"
	if _, err := PlanStartProfile(cpu, profile); err == nil || !strings.Contains(err.Error(), "measured free VRAM") {
		t.Fatalf("cpu err=%v", err)
	}
}

func TestPlanStartProfileCtxSizeDoesNotCheckVRAM(t *testing.T) {
	node := storageNode()
	ctx := 100000
	profile := readyProfile(node)
	profile.ContextTokens = &ctx
	plan, err := PlanStartProfile(node, profile)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(plan.Argv[len(plan.Argv)-2:], []string{"-c", "100000"}) {
		t.Fatalf("argv=%#v", plan.Argv)
	}
}

func TestPlanStartProfileRefusesPlanDefaultPortAndStaleRefusals(t *testing.T) {
	node := storageNode()
	profile := readyProfile(node)
	profile.PortSource = models.PortSourcePlanDefault
	if _, err := PlanStartProfile(node, profile); err == nil || !strings.Contains(err.Error(), "plan-default") {
		t.Fatalf("port source err=%v", err)
	}

	profile = readyProfile(node)
	profile.Refusals = []string{"weights are not on a named local volume"}
	if _, err := PlanStartProfile(node, profile); err == nil || !strings.Contains(err.Error(), "named local volume") {
		t.Fatalf("refusals err=%v", err)
	}
}

func TestPlanStartProfileRefusesRangeAndBindHost(t *testing.T) {
	node := storageNode()
	node.Resources.CPUCores = 4
	profile := readyProfile(node)
	profile.BindHost = "0.0.0.0"
	if _, err := PlanStartProfile(node, profile); err == nil || !strings.Contains(err.Error(), "127.0.0.1") {
		t.Fatalf("bind err=%v", err)
	}

	threads := 8
	profile = readyProfile(node)
	profile.Threads = &threads
	if _, err := PlanStartProfile(node, profile); err == nil || !strings.Contains(err.Error(), "threads") {
		t.Fatalf("threads err=%v", err)
	}

	zero := 0
	profile = readyProfile(node)
	profile.BatchSize = &zero
	if _, err := PlanStartProfile(node, profile); err == nil || !strings.Contains(err.Error(), "batch") {
		t.Fatalf("batch err=%v", err)
	}
	profile = readyProfile(node)
	profile.UBatchSize = &zero
	if _, err := PlanStartProfile(node, profile); err == nil || !strings.Contains(err.Error(), "ubatch") {
		t.Fatalf("ubatch err=%v", err)
	}

	both := 4
	profile = readyProfile(node)
	profile.NGPULayers = &both
	profile.NGPULayersMode = "all"
	if _, err := PlanStartProfile(node, profile); err == nil {
		t.Fatal("expected refusal when integer and mode are both set")
	}
}

func TestExecArgvRefusesHandMadeArgv(t *testing.T) {
	err := ExecArgvMatchesProfile(StartPlan{
		Argv: []string{"llama-server", "-m", "/mnt/models/a.gguf", "--port", "8081", "--host", "127.0.0.1"},
		Port: 8081,
	})
	if err == nil || !strings.Contains(err.Error(), "profile") {
		t.Fatalf("err=%v", err)
	}
}

func readyProfile(node models.NodeFacts) models.ModelRunProfile {
	return models.ModelRunProfile{
		Schema:       models.ModelRunSchema,
		Node:         node.Name,
		Engine:       "llama.cpp",
		ToolName:     "llama-server",
		ArtifactKind: "weights-path",
		WeightsPath:  "/mnt/models/a.gguf",
		BindHost:     "127.0.0.1",
		Port:         8081,
		PortSource:   models.PortSourceExplicit,
	}
}

func mustArgv(t *testing.T, profile models.ModelRunProfile) []string {
	t.Helper()
	argv, err := ArgvFromProfile(profile)
	if err != nil {
		t.Fatal(err)
	}
	return argv
}
