package facts

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func writeFile(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestScanDiskWeightsFindsGGUFSkipsStubDirAndGit(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	modelsDir := filepath.Join(home, "models")
	payload := append([]byte("GGUF"), bytes.Repeat([]byte("w"), 64)...)
	writeFile(t, filepath.Join(modelsDir, "real-model.gguf"), payload)
	if err := os.MkdirAll(filepath.Join(modelsDir, "empty-stub"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(modelsDir, ".git", "ignored.gguf"), payload)
	writeFile(t, filepath.Join(modelsDir, "tiny.txt"), []byte("not a model"))

	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 4,
	})
	if res.Truncated {
		t.Fatalf("scan truncated unexpectedly")
	}
	if len(res.Weights) != 1 {
		t.Fatalf("weights = %#v, want 1 GGUF", res.Weights)
	}
	got := res.Weights[0]
	if got.Format != "gguf" || got.Kind != "model" || got.Name != "real-model" {
		t.Fatalf("got %#v", got)
	}
	if !strings.HasSuffix(got.Path, "real-model.gguf") {
		t.Fatalf("path %q", got.Path)
	}
}

func TestScanDiskWeightsGroupsSafetensorsShards(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(home, "models", "nvfp4-tree")
	writeFile(t, filepath.Join(dir, "model.safetensors.index.json"), []byte(`{"x":1}`))
	writeFile(t, filepath.Join(dir, "model-00001-of-00002.safetensors"), bytes.Repeat([]byte("a"), 32))
	writeFile(t, filepath.Join(dir, "model-00002-of-00002.safetensors"), bytes.Repeat([]byte("b"), 16))

	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 8,
	})
	var st []models.DiskWeight
	for _, w := range res.Weights {
		if w.Format == "safetensors" {
			st = append(st, w)
		}
	}
	if len(st) != 1 {
		t.Fatalf("safetensors weights = %#v, want 1 grouped tree", res.Weights)
	}
	if st[0].Bytes != 48 {
		t.Fatalf("bytes = %d, want 48", st[0].Bytes)
	}
	if st[0].Name != "nvfp4-tree" {
		t.Fatalf("name = %q", st[0].Name)
	}
}

func TestScanDiskWeightsHFHubAndOllamaManifests(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	hub := filepath.Join(home, ".cache", "huggingface", "hub", "models--acme--Widget-9B")
	writeFile(t, filepath.Join(hub, "snapshots", "abc", "model.safetensors"), bytes.Repeat([]byte("s"), 40))
	man := filepath.Join(home, ".ollama", "models", "manifests", "registry.ollama.ai", "library", "widget", "latest")
	writeFile(t, man, []byte(`{"schemaVersion":2}`))

	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 8,
	})
	bySrc := map[string]models.DiskWeight{}
	for _, w := range res.Weights {
		bySrc[w.Source] = w
	}
	hf, ok := bySrc["hf-hub"]
	if !ok || hf.Name != "acme/Widget-9B" || hf.Bytes != 40 {
		t.Fatalf("hf-hub = %#v", res.Weights)
	}
	om, ok := bySrc["ollama-manifest"]
	if !ok || om.Name != "widget:latest" || om.Format != "ollama" {
		t.Fatalf("ollama-manifest = %#v", res.Weights)
	}
}

func TestScanDiskWeightsSkipsTinyHFHubPointers(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	hub := filepath.Join(home, ".cache", "huggingface", "hub", "models--acme--Pointer")
	writeFile(t, filepath.Join(hub, "snapshots", "abc", "model.safetensors"), []byte("pointer"))
	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 20 << 20,
	})
	for _, w := range res.Weights {
		if w.Source == "hf-hub" {
			t.Fatalf("LFS pointer must not be a weight: %#v", w)
		}
	}
}

func TestScanDiskWeightsSkipsNetworkVolumes(t *testing.T) {
	root := t.TempDir()
	payload := append([]byte("GGUF"), bytes.Repeat([]byte("w"), 32)...)
	writeFile(t, filepath.Join(root, "share.gguf"), payload)
	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Volumes: []models.Volume{{Mount: root, Kind: "network"}},
		MinSize: 4,
	})
	if len(res.Weights) != 0 {
		t.Fatalf("network volume must not be scanned, got %#v", res.Weights)
	}
}

func TestScanDiskWeightsMarksProjector(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	payload := append([]byte("GGUF"), bytes.Repeat([]byte("w"), 32)...)
	writeFile(t, filepath.Join(home, "models", "mmproj-BF16.gguf"), payload)
	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 4,
	})
	if len(res.Weights) != 1 || res.Weights[0].Kind != "projector" {
		t.Fatalf("got %#v", res.Weights)
	}
}

func TestScanDiskWeightsSkipsSafetensorsLFSPointers(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	dir := filepath.Join(home, "models", "lfs-tree")
	ptr := []byte("version https://git-lfs.github.com/spec/v1\noid sha256:abc\nsize 99\n")
	writeFile(t, filepath.Join(dir, "model-00001-of-00001.safetensors"), ptr)
	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 4,
	})
	for _, w := range res.Weights {
		if w.Format == "safetensors" {
			t.Fatalf("LFS pointer tree must not count as safetensors: %#v", w)
		}
	}
}

func TestDiskWeightsDiscoveryScriptKeepsLocalVolumeContract(t *testing.T) {
	s := DiskWeightsDiscoveryScript
	for _, needle := range []string{
		"axis-disk-weights-v1",
		"is_network_source",
		"hit_count",
		"DEADLINE",
		"is_lfs_pointer",
		"skip_device_mount",
	} {
		if !strings.Contains(s, needle) {
			t.Errorf("remote script missing %q", needle)
		}
	}
	if strings.Contains(s, "n = 0") {
		t.Error("remote walk_root must not reset per-mount file cap")
	}
}

func TestScanDiskWeightsRejectsFakeGGUFExtension(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	writeFile(t, filepath.Join(home, "models", "nope.gguf"), bytes.Repeat([]byte("x"), 64))
	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home:    home,
		Volumes: []models.Volume{{Mount: root, Kind: "local"}},
		MinSize: 4,
	})
	if len(res.Weights) != 0 {
		t.Fatalf("non-magic gguf must be skipped, got %#v", res.Weights)
	}
}

func TestScanDiskWeightsTimeoutTruncates(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	time.Sleep(time.Millisecond)
	res := ScanDiskWeights(ctx, DiskWeightScanConfig{
		Home:    t.TempDir(),
		Volumes: []models.Volume{{Mount: t.TempDir(), Kind: "local"}},
		MinSize: 4,
	})
	if !res.Truncated {
		t.Fatal("expected truncated when parent context is already expired")
	}
}

func TestParseDiskWeightsJSONAndTSV(t *testing.T) {
	got := parseDiskWeightsJSON(`{"weights":[{"name":"a","path":"/m/a.gguf","bytes":10,"format":"gguf"}],"truncated":true}`)
	if !got.Truncated || len(got.Weights) != 1 || got.Weights[0].Name != "a" {
		t.Fatalf("%#v", got)
	}
	tsv := "32\t/models/tree/model-00001-of-00002.safetensors\n16\t/models/tree/model-00002-of-00002.safetensors\n"
	grouped := ParseDiskWeightFindTSV(strings.NewReader(tsv), "/models", "find")
	if len(grouped) != 1 || grouped[0].Bytes != 48 || grouped[0].Format != "safetensors" {
		t.Fatalf("%#v", grouped)
	}
}

func TestHFHubName(t *testing.T) {
	if g := hfHubName("models--acme--Widget-9B"); g != "acme/Widget-9B" {
		t.Fatalf("got %q", g)
	}
}

func TestRemoteCollectorCollectsDiskWeights(t *testing.T) {
	m := minimalRemoteExec()
	m[DiskWeightsDiscoveryScript] = fakeRunResult{
		out: `{"weights":[{"name":"qwen","path":"/mnt/models/qwen.gguf","bytes":100,"format":"gguf","source":"find","kind":"model"}],"truncated":true}`,
	}
	exec := &fakeRemoteExecutor{exact: m}
	facts, err := NewRemoteCollector("worker-1", "worker", "worker-1.local", exec).Collect(context.Background())
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if !facts.DiskWeightsTruncated {
		t.Fatal("expected truncated flag from remote JSON")
	}
	if len(facts.DiskWeights) != 1 || facts.DiskWeights[0].Name != "qwen" {
		t.Fatalf("disk weights = %#v", facts.DiskWeights)
	}
}
