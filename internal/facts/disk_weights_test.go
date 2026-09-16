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
