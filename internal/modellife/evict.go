package modellife

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
)

type EvictMode string

const (
	EvictModeStop   EvictMode = "stop"
	EvictModeFreeze EvictMode = "freeze"
	EvictModeForce  EvictMode = "force"

	EvictMarkerOk = "__AXIS_EVICT_OK__"
)

// EvictResult is what the remote evict script actually observed.
// ReclaimedVRAMMB is a node-wide memory.used delta and is meaningful only
// when VRAMMeasured is true. Freeze reports no reclamation.
type EvictResult struct {
	VRAMMeasured    bool
	ReclaimedVRAMMB int64
	Freeze          bool
}

// ParseEvictOutput reads the evict script marker. A bare marker is not success.
func ParseEvictOutput(out string) (EvictResult, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, EvictMarkerOk+":") {
			continue
		}
		rest := strings.TrimPrefix(line, EvictMarkerOk+":")
		switch {
		case rest == "freeze":
			return EvictResult{Freeze: true}, nil
		case rest == "unmeasured":
			return EvictResult{}, nil
		case strings.HasPrefix(rest, "measured:"):
			delta, err := strconv.ParseInt(strings.TrimPrefix(rest, "measured:"), 10, 64)
			if err != nil {
				return EvictResult{}, fmt.Errorf("parse evict vram delta: %w", err)
			}
			return EvictResult{VRAMMeasured: true, ReclaimedVRAMMB: delta}, nil
		}
	}
	return EvictResult{}, fmt.Errorf("evict did not report an observed result")
}

type EvictTarget struct {
	InstanceID        string `json:"instance_id"`
	GenerationID      string `json:"generation_id,omitempty"`
	Model             string `json:"model"`
	Port              int    `json:"port"`
	PID               int    `json:"pid"`
	Executable        string `json:"executable,omitempty"`
	ProcessOwner      string `json:"process_owner,omitempty"`
	ProcessStartToken string `json:"process_start_token,omitempty"`
	SupervisorType    string `json:"supervisor_type,omitempty"`
	SupervisorUnit    string `json:"supervisor_unit,omitempty"`
	GPUIndices        []int  `json:"gpu_indices,omitempty"`
	WeightSizeMB      int64  `json:"weight_size_mb,omitempty"`
	SizeVRAMMB        int64  `json:"size_vram_mb,omitempty"`
}

type EvictedInstanceReceipt struct {
	InstanceID     string `json:"instance_id"`
	Model          string `json:"model"`
	Port           int    `json:"port"`
	PID            int    `json:"pid"`
	GPUIndices     []int  `json:"gpu_indices,omitempty"`
	SupervisorType string `json:"supervisor_type,omitempty"`
	SupervisorUnit string `json:"supervisor_unit,omitempty"`
	VRAMFreedMB    int64  `json:"vram_freed_mb,omitempty"`
}

type EvictionReceipt struct {
	Schema           string                      `json:"schema"` // "axis.eviction-receipt/v1"
	ID               string                      `json:"id"`     // "mo-<uuid>"
	Node             string                      `json:"node"`
	Action           string                      `json:"action"`         // "evict" | "resume"
	Mode             string                      `json:"mode,omitempty"` // stop | freeze | force
	Status           models.ModelOperationStatus `json:"status"`
	Disposition      string                      `json:"disposition"`
	ReclaimedVRAMMB  int64                       `json:"reclaimed_vram_mb"`
	VRAMObserved     bool                        `json:"vram_observed,omitempty"`
	DurationMS       int64                       `json:"duration_ms"`
	EvictedInstances []EvictedInstanceReceipt    `json:"evicted_instances"`
	ResumeCommand    string                      `json:"resume_command,omitempty"`
	SnapshotSource   string                      `json:"snapshot_source,omitempty"`
	PublicationID    string                      `json:"publication_id,omitempty"`
	StartedAt        time.Time                   `json:"started_at"`
	CompletedAt      time.Time                   `json:"completed_at"`
	Error            string                      `json:"error,omitempty"`
}

func ReceiptDirectory() string {
	return persist.AxisPath("receipts")
}

func SaveEvictionReceipt(receipt EvictionReceipt) (string, error) {
	if strings.TrimSpace(receipt.ID) == "" {
		receipt.ID = "mo-" + strings.ReplaceAll(uuid.New().String(), "-", "")[:8]
	}
	if strings.TrimSpace(receipt.Schema) == "" {
		receipt.Schema = "axis.eviction-receipt/v1"
	}
	dir := ReceiptDirectory()
	path := filepath.Join(dir, fmt.Sprintf("evict-%s.json", receipt.ID))
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return "", err
	}
	if err := persist.WritePrivateFileAtomic(path, data); err != nil {
		return "", err
	}
	return path, nil
}

func LoadEvictionReceipt(receiptID string) (*EvictionReceipt, error) {
	receiptID = strings.TrimSpace(receiptID)
	receiptID = strings.TrimPrefix(receiptID, "evict-")
	receiptID = strings.TrimSuffix(receiptID, ".json")
	path := filepath.Join(ReceiptDirectory(), fmt.Sprintf("evict-%s.json", receiptID))
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var receipt EvictionReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, err
	}
	return &receipt, nil
}

// BuildEvictShellScript generates a shell script that checks process identity,
// stops or freezes supervisors, and emits a success marker only after the
// targeted processes are gone. Stop and force include the observed change in
// summed nvidia-smi memory.used, in MiB.
func BuildEvictShellScript(targets []EvictTarget, mode EvictMode) string {
	var sb strings.Builder
	var pids []string
	for _, t := range targets {
		if t.PID > 0 {
			pids = append(pids, strconv.Itoa(t.PID))
		}
		sb.WriteString(evictIdentityGuard(t))
	}
	if mode != EvictModeFreeze {
		sb.WriteString("if command -v nvidia-smi >/dev/null 2>&1; then _axis_vram_pre=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}'); fi; ")
	}
	for _, t := range targets {
		sb.WriteString(evictSupervisorAction(t, mode))
	}
	if mode == EvictModeFreeze {
		sb.WriteString("echo '" + EvictMarkerOk + ":freeze'")
		return sb.String()
	}
	sb.WriteString(evictWaitAndMarker(pids))
	return sb.String()
}

func evictIdentityGuard(t EvictTarget) string {
	if t.PID <= 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString(fmt.Sprintf(
		"_axis_cmd=$(ps -p %d -o comm= 2>/dev/null | awk '{$1=$1; print}' || echo \"\"); "+
			"_axis_cmd=${_axis_cmd##*/}; "+
			"if test -n \"$_axis_cmd\" && test \"$_axis_cmd\" != llama-server; then "+
			"echo \"refusing pid %d: $_axis_cmd is not llama-server\" >&2; exit 1; fi; ",
		t.PID, t.PID,
	))
	if strings.TrimSpace(t.ProcessStartToken) != "" {
		sb.WriteString(fmt.Sprintf(
			"_axis_start=$(ps -p %d -o lstart= 2>/dev/null | awk '{$1=$1; print}' || echo \"\"); "+
				"if test -n \"$_axis_cmd\" && test \"$_axis_start\" != %s; then echo 'axis model generation mismatch: target pid or start time changed' >&2; exit 1; fi; ",
			t.PID,
			shellQuote(t.ProcessStartToken),
		))
	}
	return sb.String()
}

func evictSupervisorAction(t EvictTarget, mode EvictMode) string {
	var sb strings.Builder
	unit := strings.TrimSpace(t.SupervisorUnit)
	if unit != "" {
		switch mode {
		case EvictModeFreeze:
			if t.SupervisorType == "systemd-user" {
				sb.WriteString(fmt.Sprintf("systemctl --user freeze %s 2>/dev/null || true; ", shellQuote(unit)))
			} else if t.SupervisorType == "systemd-system" {
				sb.WriteString(fmt.Sprintf("systemctl freeze %s 2>/dev/null || true; ", shellQuote(unit)))
			}
		case EvictModeForce:
			// Runtime mask dies on reboot. A persistent mask would still
			// block the unit after the emergency is over.
			if t.SupervisorType == "systemd-user" {
				sb.WriteString(fmt.Sprintf("systemctl --user mask --runtime %s 2>/dev/null || true; ", shellQuote(unit)))
			} else if t.SupervisorType == "systemd-system" {
				sb.WriteString(fmt.Sprintf("systemctl mask --runtime %s 2>/dev/null || true; ", shellQuote(unit)))
			}
		default:
			if t.SupervisorType == "systemd-user" {
				sb.WriteString(fmt.Sprintf("systemctl --user stop %s 2>/dev/null || true; ", shellQuote(unit)))
			} else if t.SupervisorType == "systemd-system" {
				sb.WriteString(fmt.Sprintf("systemctl stop %s 2>/dev/null || true; ", shellQuote(unit)))
			}
		}
	}
	if mode != EvictModeFreeze && t.PID > 0 {
		sb.WriteString(fmt.Sprintf("kill -KILL %d 2>/dev/null || true; ", t.PID))
	}
	return sb.String()
}

func evictWaitAndMarker(pids []string) string {
	if len(pids) == 0 {
		return "echo '" + EvictMarkerOk + ":unmeasured'"
	}
	pidList := strings.Join(pids, " ")
	return fmt.Sprintf(
		"if command -v nvidia-smi >/dev/null 2>&1; then "+
			"_axis_gone=0; "+
			"for _i in $(seq 1 30); do "+
			"_active=0; "+
			"for _p in %s; do "+
			"if nvidia-smi --query-compute-apps=pid --format=csv,noheader 2>/dev/null | awk '{$1=$1; print}' | grep -q \"^$_p$\"; then "+
			"_active=1; break; "+
			"fi; "+
			"done; "+
			"if [ \"$_active\" -eq 0 ]; then _axis_gone=1; break; fi; "+
			"sleep 0.1; "+
			"done; "+
			"if [ \"$_axis_gone\" != 1 ]; then echo 'evict timed out: targeted pid still resident in nvidia-smi' >&2; exit 1; fi; "+
			"_axis_vram_post=$(nvidia-smi --query-gpu=memory.used --format=csv,noheader,nounits 2>/dev/null | awk '{s+=$1} END {print s+0}'); "+
			"_axis_vram_pre=${_axis_vram_pre:-0}; _axis_vram_post=${_axis_vram_post:-0}; "+
			"_axis_delta=$((_axis_vram_pre - _axis_vram_post)); "+
			"echo '"+EvictMarkerOk+":measured:'\"$_axis_delta\"; "+
			"else "+
			"for _p in %s; do _axis_note=$(kill -0 \"$_p\" 2>&1); _rc=$?; "+
			"if [ \"$_rc\" -eq 0 ]; then echo \"evict timed out: pid $_p still running\" >&2; exit 1; fi; "+
			"case \"$_axis_note\" in *[Pp]ermitted*|*[Pp]ermission*) echo \"evict cannot confirm pid $_p is gone\" >&2; exit 1;; esac; "+
			"done; "+
			"echo '"+EvictMarkerOk+":unmeasured'; fi; ",
		pidList, pidList,
	)
}

// BuildResumeShellScript generates a shell script that starts supervisor units or unfreezes services.
func BuildResumeShellScript(receipt EvictionReceipt) string {
	var sb strings.Builder
	for _, inst := range receipt.EvictedInstances {
		unit := strings.TrimSpace(inst.SupervisorUnit)
		if unit != "" {
			if receipt.Mode == string(EvictModeForce) {
				if inst.SupervisorType == "systemd-user" {
					sb.WriteString(fmt.Sprintf("systemctl --user unmask --runtime %s 2>/dev/null || true; ", shellQuote(unit)))
				} else if inst.SupervisorType == "systemd-system" {
					sb.WriteString(fmt.Sprintf("systemctl unmask --runtime %s 2>/dev/null || true; ", shellQuote(unit)))
				}
			}
			if inst.SupervisorType == "systemd-user" {
				sb.WriteString(fmt.Sprintf("systemctl --user unfreeze %s 2>/dev/null || true; ", shellQuote(unit)))
				sb.WriteString(fmt.Sprintf("systemctl --user start %s 2>/dev/null || true; ", shellQuote(unit)))
			} else if inst.SupervisorType == "systemd-system" {
				sb.WriteString(fmt.Sprintf("systemctl unfreeze %s 2>/dev/null || true; ", shellQuote(unit)))
				sb.WriteString(fmt.Sprintf("systemctl start %s 2>/dev/null || true; ", shellQuote(unit)))
			}
		}
	}
	sb.WriteString("echo '" + EvictMarkerOk + "'")
	return sb.String()
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\\''") + "'"
}
