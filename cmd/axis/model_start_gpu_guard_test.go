package main

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

func TestRunModelStartRejectsAnyObservedGPUOccupancy(t *testing.T) {
	for _, tc := range []struct {
		name        string
		occupiedGPU int
		mainGPU     string
	}{
		{name: "pin does not isolate", occupiedGPU: 0, mainGPU: "1"},
		{name: "non-primary occupancy", occupiedGPU: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := twoGPUDiscreteSnap()
			snap.Nodes[0].ResidentModels[0].GPUIndices = []int{tc.occupiedGPU}
			stubModelSnapshot(t, snap)
			stubModelConfig(t, &config.Config{Nodes: []config.NodeConfig{{Name: "storage"}}})
			runner := &fakeModelRunner{}
			previous := defaultModelRunner
			defaultModelRunner = runner
			t.Cleanup(func() { defaultModelRunner = previous })
			cmd := modelStartCmd()
			var output bytes.Buffer
			cmd.SetOut(&output)
			args := []string{"--node", "storage", "--weights", "/mnt/models/b.gguf", "--port", "8082", "--n-gpu-layers", "all", "--format", "json"}
			if tc.mainGPU != "" {
				args = append(args, "--main-gpu", tc.mainGPU)
			}
			cmd.SetArgs(args)
			if err := cmd.Execute(); err == nil || ExitCode(err) != ExitErrCommandFail {
				t.Fatalf("expected occupied-GPU rejection, got %v; started=%v", err, runner.started)
			}
			var receipt models.ModelOperationReceipt
			if err := json.Unmarshal(output.Bytes(), &receipt); err != nil {
				t.Fatalf("decode receipt: %v; output=%s", err, output.String())
			}
			if receipt.Status != models.ModelOperationRejected || receipt.Disposition != "gpu_occupied" {
				t.Fatalf("unexpected receipt: %+v", receipt)
			}
			if receipt.DeviceIndex == nil || *receipt.DeviceIndex != tc.occupiedGPU {
				t.Fatalf("receipt must identify observed occupied GPU %d: %+v", tc.occupiedGPU, receipt)
			}
			if !strings.Contains(receipt.Error, "GPU "+strconv.Itoa(tc.occupiedGPU)+" already occupied") {
				t.Fatalf("missing observed conflict: %q", receipt.Error)
			}
			if receipt.PublicationID != snap.Publication.ID || receipt.SnapshotSource != "daemon-cache" {
				t.Fatalf("missing snapshot provenance: %+v", receipt)
			}
			if len(runner.started) != 0 || len(runner.probed) != 0 {
				t.Fatalf("runner must not execute on rejection: %+v", runner)
			}
		})
	}
}
