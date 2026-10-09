package facts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

type appleFMFactsPayload struct {
	Availability string   `json:"availability"`
	Reason       string   `json:"reason"`
	ContextSize  int      `json:"context_size"`
	Model        string   `json:"model"`
	Capabilities []string `json:"capabilities"`
}

// appleFMFromProbe turns the Apple helper's output into node facts. The
// helper's --facts mode reads SystemLanguageModel availability, contextSize,
// variant, and capabilities without generating. A timeout is unknown, never
// unavailable. Marker outputs (OK, AVAILABLE, UNAVAILABLE:<reason>,
// UNVERIFIED) from the framework-only fallback and older helpers are still
// understood.
func appleFMFromProbe(version, out string, err error) *models.AppleFoundationModelsInfo {
	info := &models.AppleFoundationModelsInfo{Version: version}
	trimmed := strings.TrimSpace(out)

	if err != nil && (errors.Is(err, context.DeadlineExceeded) || strings.Contains(err.Error(), "deadline exceeded")) {
		info.State = models.AppleFMUnknown
		info.Error = "probe timed out; availability unknown"
		return info
	}

	if err == nil && strings.HasPrefix(trimmed, "{") {
		var p appleFMFactsPayload
		if json.Unmarshal([]byte(trimmed), &p) == nil && p.Availability != "" {
			switch p.Availability {
			case "available":
				info.Available, info.Verified, info.State = true, true, models.AppleFMReady
				info.ContextWindow = p.ContextSize
				info.Model = p.Model
				info.Capabilities = p.Capabilities
			case "unavailable":
				setAppleFMUnavailable(info, p.Reason)
			default:
				info.State = models.AppleFMUnknown
				info.Error = "unrecognised availability " + p.Availability
			}
			return info
		}
	}

	switch {
	case err == nil && (trimmed == "OK" || trimmed == "AVAILABLE" || trimmed == "OK\nOK"):
		info.Available, info.Verified, info.State = true, true, models.AppleFMReady
	case err == nil && strings.HasPrefix(trimmed, "UNAVAILABLE:") && !strings.Contains(trimmed, "\n"):
		setAppleFMUnavailable(info, strings.TrimPrefix(trimmed, "UNAVAILABLE:"))
		info.Error = trimmed
	case err == nil && trimmed == "UNVERIFIED":
		info.State = models.AppleFMUnknown
		info.Error = "foundation models framework imported but runtime availability unverified"
	default:
		info.State = models.AppleFMUnknown
		switch {
		case trimmed != "":
			info.Error = trimmed
		case err != nil:
			info.Error = err.Error()
		default:
			info.Error = "apple foundation models probe failed"
		}
	}
	return info
}

// setAppleFMUnavailable records Apple's UnavailableReason. modelNotReady
// means the model is still downloading: cold, not absent.
func setAppleFMUnavailable(info *models.AppleFoundationModelsInfo, reason string) {
	info.Reason = reason
	info.State = models.AppleFMUnavailable
	if reason == "modelNotReady" {
		info.State = models.AppleFMCold
	}
	info.Error = "unavailable: " + reason
}
