package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestPrintModelInventoryTextSeparatesWeightRAMAndVRAM(t *testing.T) {
	inventory := models.ModelInventory{Instances: []models.ModelInstance{
		{ID: "mi-llama", Model: "llama-model", Engine: "llama.cpp", Node: "node-a", State: models.ModelInstanceResident, WeightSizeMB: 5120},
		{ID: "mi-mlx", Model: "mlx-model", Engine: "mlx", Node: "node-b", State: models.ModelInstanceResident, SizeRAMMB: 3072},
		{ID: "mi-ollama", Model: "ollama-model", Engine: "ollama", Node: "node-c", State: models.ModelInstanceResident, SizeVRAMMB: 4096},
	}}
	var out bytes.Buffer
	if err := printModelInventoryText(&out, inventory); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	for _, want := range []string{"WEIGHTS", "RAM", "VRAM", "5120 MiB", "3072 MiB", "4096 MiB"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output missing %q:\n%s", want, rendered)
		}
	}
}

func TestPrintModelInspectionTextSeparatesWeightRAMAndVRAM(t *testing.T) {
	inspection := models.ModelInspection{Instance: models.ModelInstance{
		ID: "mi-test", Model: "test-model", Engine: "llama.cpp", Node: "node-a", State: models.ModelInstanceResident,
		WeightSizeMB: 5120, SizeRAMMB: 3072,
	}}
	var out bytes.Buffer
	if err := printModelInspectionText(&out, inspection); err != nil {
		t.Fatal(err)
	}
	rendered := out.String()
	for _, want := range []string{"Weights: 5120 MiB", "RAM: 3072 MiB", "VRAM: —"} {
		if !strings.Contains(rendered, want) {
			t.Fatalf("output missing %q:\n%s", want, rendered)
		}
	}
}
