package modellife

import (
	"fmt"
	"strings"
)

// StopTarget is either a legacy port-only request or an evidence-bound process
// generation. A generation target must carry all fields needed to reject PID
// reuse or a replacement process before termination.
type StopTarget struct {
	Port              int
	PID               int
	Executable        string
	ProcessOwner      string
	ProcessStartToken string
	GenerationID      string
}

func (t StopTarget) IsGenerationBound() bool {
	return strings.TrimSpace(t.GenerationID) != ""
}

func (t StopTarget) Validate() error {
	if t.Port < 1 || t.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535")
	}
	if !t.IsGenerationBound() {
		return nil
	}
	if t.PID <= 0 || strings.TrimSpace(t.Executable) == "" || strings.TrimSpace(t.ProcessStartToken) == "" {
		return fmt.Errorf("generation-bound stop requires PID, executable, and process start token")
	}
	return nil
}
