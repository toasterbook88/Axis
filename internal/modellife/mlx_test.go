package modellife

import (
	"reflect"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func unifiedMLXNode() models.NodeFacts {
	node := storageNode()
	node.Resources.MemoryTopology = models.MemoryTopologyUnified
	node.Tools = append(node.Tools, models.ToolInfo{Name: "mlx_lm.server", Path: "/usr/local/bin/mlx_lm.server"})
	return node
}

func TestMLXArgvUsesObservedServerBinary(t *testing.T) {
	node := unifiedMLXNode()
	profile := models.ModelRunProfile{
		Schema:   models.ModelRunSchema,
		Node:     node.Name,
		Engine:   "mlx",
		MLXModel: "/mnt/models/qwen",
		BindHost: "127.0.0.1",
		Port:     8080,
	}
	argv, err := MLXArgv(node, profile, false)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/local/bin/mlx_lm.server", "--model", "/mnt/models/qwen", "--port", "8080", "--host", "127.0.0.1"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv=%#v", argv)
	}
}

func TestMLXArgvUsesPythonModuleWhenImportWasObserved(t *testing.T) {
	node := storageNode()
	node.Resources.MemoryTopology = models.MemoryTopologyUnified
	node.Tools = []models.ToolInfo{
		{Name: "mlx_lm", Path: "/usr/local/bin/mlx_lm"},
		{Name: "python3", Path: "/usr/bin/python3"},
	}
	profile := models.ModelRunProfile{
		Schema: models.ModelRunSchema, Engine: models.EngineMLX, MLXModel: "/mnt/models/qwen",
		BindHost: "127.0.0.1", Port: 8080,
	}
	argv, err := MLXArgv(node, profile, true)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"/usr/bin/python3", "-m", "mlx_lm.server", "--model", "/mnt/models/qwen", "--port", "8080", "--host", "127.0.0.1"}
	if !reflect.DeepEqual(argv, want) {
		t.Fatalf("argv=%#v", argv)
	}
	if _, err := MLXArgv(node, profile, false); err == nil || !strings.Contains(err.Error(), "mlx_lm.server") {
		t.Fatalf("mlx_lm console tool err=%v", err)
	}
}

func TestMLXArgvRefusesHubDiscreteAndFileAndOmitsUnsetFlags(t *testing.T) {
	node := unifiedMLXNode()
	base := models.ModelRunProfile{
		Schema: models.ModelRunSchema, Engine: models.EngineMLX, BindHost: "127.0.0.1", Port: 8080,
	}
	hub := base
	hub.MLXModel = "mlx-community/Qwen"
	if _, err := MLXArgv(node, hub, false); err == nil || !strings.Contains(err.Error(), "hub") {
		t.Fatalf("hub err=%v", err)
	}
	file := base
	file.MLXModel = "/mnt/models/a.gguf"
	if _, err := MLXArgv(node, file, false); err == nil || !strings.Contains(err.Error(), "directory") {
		t.Fatalf("file err=%v", err)
	}
	discrete := node
	copied := *node.Resources
	discrete.Resources = &copied
	discrete.Resources.MemoryTopology = ""
	dir := base
	dir.MLXModel = "/mnt/models/qwen"
	if _, err := MLXArgv(discrete, dir, false); err == nil || !strings.Contains(err.Error(), "unified") {
		t.Fatalf("discrete err=%v", err)
	}
	step, cache, bits := 2, int64(4096), 4
	flagged := dir
	flagged.PrefillStepSize = &step
	flagged.PromptCacheBytes = &cache
	flagged.KVBits = &bits
	argv, err := MLXArgv(node, flagged, false)
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(argv, " ")
	if !strings.Contains(got, "--prefill-step-size 2") || !strings.Contains(got, "--prompt-cache-bytes 4096") || !strings.Contains(got, "--kv-bits 4") {
		t.Fatalf("argv=%v", argv)
	}
	plain, err := MLXArgv(node, dir, false)
	if err != nil {
		t.Fatal(err)
	}
	plainGot := strings.Join(plain, " ")
	if strings.Contains(plainGot, "prefill") || strings.Contains(plainGot, "kv-bits") || strings.Contains(plainGot, "prompt-cache") {
		t.Fatalf("unset flags leaked: %v", plain)
	}
}
