package modelplan

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// A Core ML bundle with the model's name is not weights a runtime can start.
func TestHasLocalWeightsIgnoresUnknownBundles(t *testing.T) {
	spec := models.ModelSpec{Name: "OpenELM"}
	node := models.NodeFacts{DiskWeights: []models.DiskWeight{{Name: "OpenELM", Path: "/m/OpenELM.mlpackage", Format: "coreml", Kind: "unknown"}}}
	if hasLocalWeights(node, spec) {
		t.Fatal("unknown bundle counted as local weights")
	}
	node.DiskWeights = append(node.DiskWeights, models.DiskWeight{Name: "OpenELM", Path: "/m/OpenELM.gguf", Format: "gguf", Kind: "model"})
	if !hasLocalWeights(node, spec) {
		t.Fatal("gguf weights not counted")
	}
}
