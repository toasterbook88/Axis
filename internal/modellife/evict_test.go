package modellife

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
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

func TestBuildEvictShellScriptNoPIDMarksUnmeasured(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		SupervisorType: "systemd-user",
		SupervisorUnit: "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
kill() { echo "kill $*" >> "$log"; return 0; }
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code != 0 {
		t.Fatalf("pid-less evict failed: code=%d stdout=%q log=%q", code, stdout, log)
	}
	if !strings.Contains(stdout, EvictMarkerOk+":unmeasured") {
		t.Fatalf("stdout = %q, want %s:unmeasured", stdout, EvictMarkerOk)
	}
	if strings.Contains(log, "kill") {
		t.Fatalf("pid-less evict must not kill, log=%q", log)
	}
	if !strings.Contains(log, "systemctl --user stop bonsai2-27b.service") {
		t.Fatalf("pid-less evict must still stop the unit, log=%q", log)
	}
}

func TestParseEvictOutput(t *testing.T) {
	measured, err := ParseEvictOutput("noise\n" + EvictMarkerOk + ":measured:800\n")
	if err != nil || !measured.VRAMMeasured || measured.ReclaimedVRAMMB != 800 || measured.Freeze {
		t.Fatalf("measured parse = %+v err=%v", measured, err)
	}
	negative, err := ParseEvictOutput(EvictMarkerOk + ":measured:-12")
	if err != nil || !negative.VRAMMeasured || negative.ReclaimedVRAMMB != -12 {
		t.Fatalf("negative parse = %+v err=%v", negative, err)
	}
	unmeasured, err := ParseEvictOutput(EvictMarkerOk + ":unmeasured")
	if err != nil || unmeasured.VRAMMeasured || unmeasured.ReclaimedVRAMMB != 0 || unmeasured.Freeze {
		t.Fatalf("unmeasured parse = %+v err=%v", unmeasured, err)
	}
	frozen, err := ParseEvictOutput(EvictMarkerOk + ":freeze")
	if err != nil || !frozen.Freeze || frozen.VRAMMeasured || frozen.ReclaimedVRAMMB != 0 {
		t.Fatalf("freeze parse = %+v err=%v", frozen, err)
	}
	if _, err := ParseEvictOutput("no marker"); err == nil {
		t.Fatal("missing marker must fail")
	}
	if _, err := ParseEvictOutput(EvictMarkerOk); err == nil {
		t.Fatal("bare marker must fail")
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

// runEvictScript executes a built evict script in one bash process so function
// stubs override the kill builtin and external ps/systemctl/nvidia-smi.
func runEvictScript(t *testing.T, script, stubs string) (stdout string, log string, exitCode int) {
	t.Helper()
	dir := t.TempDir()
	logPath := filepath.Join(dir, "actions.log")
	prelude := fmt.Sprintf("log=%s\n%s\n", shellQuote(logPath), stubs)
	cmd := exec.Command("bash", "-c", prelude+script)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			t.Fatalf("run evict script: %v\n%s", err, out)
		}
		code = exitErr.ExitCode()
	}
	logged, readErr := os.ReadFile(logPath)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatalf("read action log: %v", readErr)
	}
	return string(out), string(logged), code
}

func TestBuildEvictShellScriptRefusesMismatchedStartTokenBeforeKill(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:               4242,
		ProcessStartToken: "Thu Sep  3 09:00:00 2026",
		SupervisorType:    "systemd-user",
		SupervisorUnit:    "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
ps() {
  echo "ps $*" >> "$log"
  case "$*" in
    *lstart*) printf '%s\n' 'Thu Sep  3 10:00:00 2026' ;;
    *comm*) printf '%s\n' 'llama-server' ;;
  esac
}
kill() { echo "kill $*" >> "$log"; return 0; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code == 0 {
		t.Fatalf("mismatched start token must fail, stdout=%q log=%q", stdout, log)
	}
	if strings.Contains(stdout, EvictMarkerOk) {
		t.Fatalf("mismatched start token must not emit %s, stdout=%q", EvictMarkerOk, stdout)
	}
	if strings.Contains(log, "kill -KILL") || strings.Contains(log, "systemctl") {
		t.Fatalf("mismatched start token must not kill or stop the unit, log=%q", log)
	}
}

func TestBuildEvictShellScriptRefusesWrongOwnerBeforeKill(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:            4242,
		SupervisorType: "systemd-user",
		SupervisorUnit: "bonsai2-27b.service",
	}}, EvictModeForce)
	stubs := `
ps() {
  echo "ps $*" >> "$log"
  case "$*" in
    *comm*) printf '%s\n' 'python' ;;
  esac
}
kill() { echo "kill $*" >> "$log"; return 0; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code == 0 {
		t.Fatalf("wrong owner must fail, stdout=%q log=%q", stdout, log)
	}
	if strings.Contains(stdout, EvictMarkerOk) {
		t.Fatalf("wrong owner must not emit %s, stdout=%q", EvictMarkerOk, stdout)
	}
	if strings.Contains(log, "kill -KILL") || strings.Contains(log, "systemctl") {
		t.Fatalf("wrong owner must not mask or kill, log=%q", log)
	}
}

func TestBuildEvictShellScriptAllowsAlreadyExitedPID(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:               4242,
		ProcessStartToken: "Thu Sep  3 09:00:00 2026",
		SupervisorType:    "systemd-user",
		SupervisorUnit:    "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
ps() {
  echo "ps $*" >> "$log"
  return 0
}
kill() { echo "kill $*" >> "$log"; return 0; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code != 0 {
		t.Fatalf("an already exited pid is not a generation mismatch, code=%d stdout=%q log=%q", code, stdout, log)
	}
	if strings.Contains(stdout, "generation mismatch") {
		t.Fatalf("already exited pid must not be reported as a mismatch, stdout=%q", stdout)
	}
	if !strings.Contains(log, "kill -KILL 4242") {
		t.Fatalf("evict continues when the pid is already gone, log=%q", log)
	}
}

func TestBuildEvictShellScriptWithholdsMarkerWhilePIDResident(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:               4242,
		ProcessStartToken: "Thu Sep 3 09:00:00 2026",
		SupervisorType:    "systemd-user",
		SupervisorUnit:    "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
ps() {
  case "$*" in
    *lstart*) printf '%s\n' 'Thu Sep  3 09:00:00 2026' ;;
    *comm*) printf '%s\n' 'llama-server' ;;
  esac
}
kill() { echo "kill $*" >> "$log"; return 0; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
nvidia-smi() { printf '%s\n' '4242'; }
`
	stdout, _, code := runEvictScript(t, script, stubs)
	if code == 0 || strings.Contains(stdout, EvictMarkerOk) {
		t.Fatalf("a pid that stays in nvidia-smi compute-apps must not succeed, code=%d stdout=%q", code, stdout)
	}
}

func TestBuildEvictShellScriptReportsMeasuredVRAMDelta(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:               4242,
		ProcessStartToken: "Thu Sep 3 09:00:00 2026",
		SupervisorType:    "systemd-user",
		SupervisorUnit:    "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
ps() {
  case "$*" in
    *lstart*) printf '%s\n' 'Thu Sep  3 09:00:00 2026' ;;
    *comm*) printf '%s\n' 'llama-server' ;;
  esac
}
kill() { echo "kill $*" >> "$log"; return 0; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
nvidia-smi() {
  echo "nvidia-smi $*" >> "$log"
  case "$*" in
    *compute-apps*) return 0 ;;
    *memory.used*)
      n=0
      if [ -f "${log}.vram" ]; then n=$(cat "${log}.vram"); fi
      n=$((n + 1))
      printf '%s' "$n" > "${log}.vram"
      if [ "$n" -eq 1 ]; then printf '5000\n'; else printf '4200\n'; fi
      ;;
  esac
}
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code != 0 {
		t.Fatalf("measured evict failed: code=%d stdout=%q log=%q", code, stdout, log)
	}
	if !strings.Contains(stdout, EvictMarkerOk+":measured:800") {
		t.Fatalf("stdout = %q, want %s:measured:800", stdout, EvictMarkerOk)
	}
	pre := strings.Index(log, "memory.used")
	killAt := strings.Index(log, "kill -KILL 4242")
	if pre < 0 || killAt < 0 || pre > killAt {
		t.Fatalf("memory.used must be sampled before kill, log=%q", log)
	}
	if strings.Count(log, "memory.used") < 2 {
		t.Fatalf("memory.used must be sampled before and after the stop, log=%q", log)
	}
}

func TestBuildEvictShellScriptFreezeMarksWithoutKilling(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:               4242,
		ProcessStartToken: "Thu Sep 3 09:00:00 2026",
		SupervisorType:    "systemd-user",
		SupervisorUnit:    "bonsai2-27b.service",
	}}, EvictModeFreeze)
	stubs := `
ps() {
  case "$*" in
    *lstart*) printf '%s\n' 'Thu Sep  3 09:00:00 2026' ;;
    *comm*) printf '%s\n' 'llama-server' ;;
  esac
}
kill() { echo "kill $*" >> "$log"; return 0; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
nvidia-smi() { echo "nvidia-smi $*" >> "$log"; printf '9999\n'; }
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code != 0 {
		t.Fatalf("freeze failed: code=%d stdout=%q log=%q", code, stdout, log)
	}
	if !strings.Contains(stdout, EvictMarkerOk+":freeze") {
		t.Fatalf("stdout = %q, want %s:freeze", stdout, EvictMarkerOk)
	}
	if strings.Contains(log, "kill") || strings.Contains(log, "nvidia-smi") {
		t.Fatalf("freeze must not kill or sample VRAM, log=%q", log)
	}
	if !strings.Contains(log, "systemctl --user freeze bonsai2-27b.service") {
		t.Fatalf("freeze must freeze the unit, log=%q", log)
	}
}

func TestBuildEvictShellScriptUnmeasuredWhenNvidiaSMIAbsent(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:            4242,
		SupervisorType: "systemd-user",
		SupervisorUnit: "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
mkdir -p "${log}.bin"
ln -s "$(command -v awk)" "${log}.bin/awk"
export PATH="${log}.bin"
ps() { printf '%s\n' 'llama-server'; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
kill() {
  echo "kill $*" >> "$log"
  case "$1" in
    -0) return 1 ;;
    *) return 0 ;;
  esac
}
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code != 0 {
		t.Fatalf("unmeasured evict failed: code=%d stdout=%q log=%q", code, stdout, log)
	}
	if !strings.Contains(stdout, EvictMarkerOk+":unmeasured") {
		t.Fatalf("stdout = %q, want %s:unmeasured", stdout, EvictMarkerOk)
	}
	if strings.Contains(stdout, ":measured:") {
		t.Fatalf("absent nvidia-smi must not invent a measurement, stdout=%q", stdout)
	}
	if !strings.Contains(log, "kill -KILL 4242") {
		t.Fatalf("stop still kills when nvidia-smi is absent, log=%q", log)
	}
}

func TestBuildEvictShellScriptUnmeasuredRefusesSurvivingPID(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:            4242,
		SupervisorType: "systemd-user",
		SupervisorUnit: "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
mkdir -p "${log}.bin"
ln -s "$(command -v awk)" "${log}.bin/awk"
export PATH="${log}.bin"
ps() { printf '%s\n' 'llama-server'; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
kill() {
  echo "kill $*" >> "$log"
  return 0
}
`
	stdout, log, code := runEvictScript(t, script, stubs)
	if code == 0 || strings.Contains(stdout, EvictMarkerOk) {
		t.Fatalf("a pid that survives kill -0 must not succeed, code=%d stdout=%q log=%q", code, stdout, log)
	}
}

func TestBuildEvictShellScriptUnmeasuredRefusesPermissionDenied(t *testing.T) {
	script := BuildEvictShellScript([]EvictTarget{{
		PID:            4242,
		SupervisorType: "systemd-user",
		SupervisorUnit: "bonsai2-27b.service",
	}}, EvictModeStop)
	stubs := `
mkdir -p "${log}.bin"
ln -s "$(command -v awk)" "${log}.bin/awk"
export PATH="${log}.bin"
ps() { printf '%s\n' 'llama-server'; }
systemctl() { echo "systemctl $*" >> "$log"; return 0; }
kill() {
  echo "kill $*" >> "$log"
  case "$1" in
    -0) echo 'kill: (4242) - Operation not permitted' >&2; return 1 ;;
    *) return 0 ;;
  esac
}
`
	stdout, _, code := runEvictScript(t, script, stubs)
	if code == 0 || strings.Contains(stdout, EvictMarkerOk) {
		t.Fatalf("kill -0 permission denied must not count as gone, code=%d stdout=%q", code, stdout)
	}
}
