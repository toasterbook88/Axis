package models

import "testing"

func TestRunnableDiskWeightsDropsUnknownBundles(t *testing.T) {
	ws := []DiskWeight{{Name: "a", Format: "gguf", Kind: "model"}, {Name: "b", Format: "coreml", Kind: "unknown"}, {Name: "c", Format: "safetensors"}}
	got := RunnableDiskWeights(ws)
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "c" {
		t.Fatalf("got %+v", got)
	}
}
