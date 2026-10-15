package facts

import (
	"bytes"
	"context"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// Core AI and Core ML bundles are directories recorded whole, with kind
// unknown; the walk does not descend into them.
func TestScanDiskWeightsRecordsModelBundlesAsUnknown(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "home")
	writeFile(t, filepath.Join(home, "models", "Export.aimodel", "weights", "w.bin"), bytes.Repeat([]byte("x"), 64))
	writeFile(t, filepath.Join(home, "models", "OpenELM.mlpackage", "Data", "weight.bin"), bytes.Repeat([]byte("x"), 32))
	writeFile(t, filepath.Join(home, "models", "OpenELM.mlpackage", "Data", "inner.gguf"), append([]byte("GGUF"), bytes.Repeat([]byte("x"), 60)...))
	writeFile(t, filepath.Join(home, "models", "tiny.mlmodelc", "m.bin"), []byte("x"))
	res := ScanDiskWeights(context.Background(), DiskWeightScanConfig{
		Home: home, Volumes: []models.Volume{{Mount: root, Kind: "local"}}, MinSize: 16,
	})
	got := map[string]models.DiskWeight{}
	for _, w := range res.Weights {
		got[w.Name] = w
	}
	if w := got["Export"]; w.Format != "aimodel" || w.Kind != "unknown" || w.Bytes != 64 || w.Runnable() {
		t.Fatalf("aimodel = %+v", w)
	}
	if w := got["OpenELM"]; w.Format != "coreml" || w.Kind != "unknown" || w.Bytes != 96 {
		t.Fatalf("mlpackage = %+v", w)
	}
	if _, ok := got["inner"]; ok {
		t.Fatal("the walk must not descend into a bundle")
	}
	if _, ok := got["tiny"]; ok {
		t.Fatal("a bundle below the size floor is skipped")
	}
}

func TestDiskWeightsScriptRecordsModelBundles(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 not available")
	}
	home := t.TempDir()
	writeFile(t, filepath.Join(home, "models", "OpenELM.mlpackage", "Data", "weight.bin"), bytes.Repeat([]byte("x"), 21<<20))
	_, rest, _ := strings.Cut(DiskWeightsDiscoveryScript, "<<'PY'\n")
	body, _, _ := strings.Cut(rest, "\nPY")
	cmd := exec.Command("python3", "-")
	cmd.Stdin = strings.NewReader(body)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("script: %v\n%s", err, out)
	}
	res := parseDiskWeightsJSON(string(out))
	var found *models.DiskWeight
	for i, w := range res.Weights {
		if w.Name == "OpenELM" {
			found = &res.Weights[i]
		}
	}
	if found == nil || found.Format != "coreml" || found.Kind != "unknown" || found.Bytes != 21<<20 {
		raw, _ := json.Marshal(res)
		t.Fatalf("bundle not recorded as coreml/unknown: %s", raw)
	}
}
