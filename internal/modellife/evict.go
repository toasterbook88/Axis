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
	Action           string                      `json:"action"` // "evict" | "resume"
	Status           models.ModelOperationStatus `json:"status"`
	Disposition      string                      `json:"disposition"`
	ReclaimedVRAMMB  int64                       `json:"reclaimed_vram_mb"`
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

// BuildEvictShellScript generates a shell script that stops supervisors, kills processes,
// and awaits driver VRAM reclamation.
func BuildEvictShellScript(targets []EvictTarget, mode EvictMode) string {
	var sb strings.Builder
	var pids []string

	for _, t := range targets {
		if t.PID > 0 {
			pids = append(pids, strconv.Itoa(t.PID))
		}

		unit := strings.TrimSpace(t.SupervisorUnit)
		if unit != "" {
			if mode == EvictModeFreeze {
				if t.SupervisorType == "systemd-user" {
					sb.WriteString(fmt.Sprintf("systemctl --user freeze %s 2>/dev/null || true; ", shellQuote(unit)))
				} else if t.SupervisorType == "systemd-system" {
					sb.WriteString(fmt.Sprintf("systemctl freeze %s 2>/dev/null || true; ", shellQuote(unit)))
				}
			} else {
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
	}

	if len(pids) > 0 && mode != EvictModeFreeze {
		pidList := strings.Join(pids, " ")
		sb.WriteString(fmt.Sprintf(
			"if command -v nvidia-smi >/dev/null 2>&1; then "+
				"for _i in $(seq 1 30); do "+
				"_active=0; "+
				"for _p in %s; do "+
				"if nvidia-smi --query-compute-apps=pid --format=csv,noheader 2>/dev/null | grep -q \"^$_p$\"; then "+
				"_active=1; break; "+
				"fi; "+
				"done; "+
				"if [ \"$_active\" -eq 0 ]; then break; fi; "+
				"sleep 0.1; "+
				"done; "+
				"fi; ",
			pidList,
		))
	}

	sb.WriteString("echo '" + EvictMarkerOk + "'")
	return sb.String()
}

// BuildResumeShellScript generates a shell script that starts supervisor units or unfreezes services.
func BuildResumeShellScript(receipt EvictionReceipt) string {
	var sb strings.Builder
	for _, inst := range receipt.EvictedInstances {
		unit := strings.TrimSpace(inst.SupervisorUnit)
		if unit != "" {
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
