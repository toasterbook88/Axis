package modellife

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestBuildEvictShellScriptGeneratesSupervisorCommands(t *testing.T) {
	targets := []EvictTarget{
		{
			InstanceID:     "mi-test-1",
			Port:           8082,
			PID:            4242,
			SupervisorType: "systemd-user",
			SupervisorUnit: "bonsai2-27b.service",
		},
		{
			InstanceID:     "mi-test-2",
			Port:           8081,
			PID:            4243,
			SupervisorType: "systemd-system",
			SupervisorUnit: "coder7b.service",
		},
	}

	script := BuildEvictShellScript(targets, EvictModeStop)
	if !strings.Contains(script, "systemctl --user stop 'bonsai2-27b.service'") {
		t.Errorf("expected systemctl --user stop, got: %s", script)
	}
	if !strings.Contains(script, "systemctl stop 'coder7b.service'") {
		t.Errorf("expected systemctl stop, got: %s", script)
	}
	if !strings.Contains(script, "kill -KILL 4242") || !strings.Contains(script, "kill -KILL 4243") {
		t.Errorf("expected kill -KILL commands, got: %s", script)
	}
	if !strings.Contains(script, "nvidia-smi") {
		t.Errorf("expected nvidia-smi wait loop, got: %s", script)
	}
	if !strings.Contains(script, EvictMarkerOk) {
		t.Errorf("expected EvictMarkerOk, got: %s", script)
	}
}

func TestBuildEvictShellScriptFreezeMode(t *testing.T) {
	targets := []EvictTarget{
		{
			InstanceID:     "mi-test-1",
			Port:           8082,
			PID:            4242,
			SupervisorType: "systemd-user",
			SupervisorUnit: "bonsai2-27b.service",
		},
	}

	script := BuildEvictShellScript(targets, EvictModeFreeze)
	if !strings.Contains(script, "systemctl --user freeze 'bonsai2-27b.service'") {
		t.Errorf("expected systemctl --user freeze, got: %s", script)
	}
	if strings.Contains(script, "kill -KILL") {
		t.Errorf("freeze mode must not issue kill -KILL, got: %s", script)
	}
}

func TestBuildEvictShellScriptForceMasksRuntimeThenKills(t *testing.T) {
	targets := []EvictTarget{
		{
			InstanceID:     "mi-test-1",
			Port:           8082,
			PID:            4242,
			SupervisorType: "systemd-user",
			SupervisorUnit: "bonsai2-27b.service",
		},
		{
			InstanceID:     "mi-test-2",
			Port:           8081,
			PID:            4243,
			SupervisorType: "systemd-system",
			SupervisorUnit: "coder7b.service",
		},
	}

	script := BuildEvictShellScript(targets, EvictModeForce)
	userMask := "systemctl --user mask --runtime 'bonsai2-27b.service'"
	systemMask := "systemctl mask --runtime 'coder7b.service'"
	if !strings.Contains(script, userMask) {
		t.Errorf("expected user runtime mask, got: %s", script)
	}
	if !strings.Contains(script, systemMask) {
		t.Errorf("expected system runtime mask, got: %s", script)
	}
	if strings.Contains(script, "systemctl --user mask --runtime --user") || strings.Contains(script, "mask --runtime --user") {
		t.Errorf("system mask must not include --user, got: %s", script)
	}
	if !strings.Contains(script, "kill -KILL 4242") || strings.Index(script, userMask) > strings.Index(script, "kill -KILL 4242") {
		t.Errorf("user mask must precede kill -KILL, got: %s", script)
	}
	if !strings.Contains(script, "kill -KILL 4243") || strings.Index(script, systemMask) > strings.Index(script, "kill -KILL 4243") {
		t.Errorf("system mask must precede kill -KILL, got: %s", script)
	}

	stopScript := BuildEvictShellScript(targets, EvictModeStop)
	if strings.Contains(stopScript, "mask") {
		t.Errorf("stop mode must not mask, got: %s", stopScript)
	}

	freezeScript := BuildEvictShellScript(targets[:1], EvictModeFreeze)
	if !strings.Contains(freezeScript, "systemctl --user freeze 'bonsai2-27b.service'") || strings.Contains(freezeScript, "kill -KILL") {
		t.Errorf("freeze mode changed, got: %s", freezeScript)
	}

	resume := BuildResumeShellScript(EvictionReceipt{
		Mode: string(EvictModeForce),
		EvictedInstances: []EvictedInstanceReceipt{
			{SupervisorType: "systemd-user", SupervisorUnit: "bonsai2-27b.service"},
			{SupervisorType: "systemd-system", SupervisorUnit: "coder7b.service"},
		},
	})
	userUnmask := "systemctl --user unmask --runtime 'bonsai2-27b.service'"
	systemUnmask := "systemctl unmask --runtime 'coder7b.service'"
	if !strings.Contains(resume, userUnmask) || strings.Index(resume, userUnmask) > strings.Index(resume, "systemctl --user start 'bonsai2-27b.service'") {
		t.Errorf("user unmask must precede start, got: %s", resume)
	}
	if !strings.Contains(resume, systemUnmask) || strings.Index(resume, systemUnmask) > strings.Index(resume, "systemctl start 'coder7b.service'") {
		t.Errorf("system unmask must precede start, got: %s", resume)
	}
}

func TestBuildResumeShellScript(t *testing.T) {
	receipt := EvictionReceipt{
		ID:   "mo-test",
		Node: "cranium",
		EvictedInstances: []EvictedInstanceReceipt{
			{
				InstanceID:     "mi-test-1",
				SupervisorType: "systemd-user",
				SupervisorUnit: "bonsai2-27b.service",
			},
		},
	}
	script := BuildResumeShellScript(receipt)
	if !strings.Contains(script, "systemctl --user start 'bonsai2-27b.service'") {
		t.Errorf("expected systemctl --user start, got: %s", script)
	}
	if !strings.Contains(script, EvictMarkerOk) {
		t.Errorf("expected EvictMarkerOk, got: %s", script)
	}
}

func TestSaveAndLoadEvictionReceipt(t *testing.T) {
	tmpDir := t.TempDir()
	origHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", origHome)

	receipt := EvictionReceipt{
		ID:              "mo-test-receipt",
		Node:            "cranium",
		Action:          "evict",
		Status:          models.ModelOperationCompleted,
		Disposition:     "evicted",
		ReclaimedVRAMMB: 9842,
		DurationMS:      94,
		StartedAt:       time.Now().UTC(),
		CompletedAt:     time.Now().UTC(),
		EvictedInstances: []EvictedInstanceReceipt{
			{
				InstanceID:     "mi-test-1",
				Model:          "bonsai2-27b",
				Port:           8082,
				PID:            4242,
				GPUIndices:     []int{0},
				SupervisorType: "systemd-user",
				SupervisorUnit: "bonsai2-27b.service",
				VRAMFreedMB:    9842,
			},
		},
	}

	path, err := SaveEvictionReceipt(receipt)
	if err != nil {
		t.Fatalf("SaveEvictionReceipt failed: %v", err)
	}
	if !strings.HasSuffix(path, filepath.Join("receipts", "evict-mo-test-receipt.json")) {
		t.Fatalf("unexpected receipt path: %s", path)
	}

	loaded, err := LoadEvictionReceipt("mo-test-receipt")
	if err != nil {
		t.Fatalf("LoadEvictionReceipt failed: %v", err)
	}
	if loaded.ID != receipt.ID || loaded.ReclaimedVRAMMB != 9842 {
		t.Fatalf("loaded receipt mismatch: %+v", loaded)
	}
	if len(loaded.EvictedInstances) != 1 || loaded.EvictedInstances[0].SupervisorUnit != "bonsai2-27b.service" {
		t.Fatalf("loaded evicted instances mismatch: %+v", loaded.EvictedInstances)
	}
}
