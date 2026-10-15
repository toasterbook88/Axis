package models

// AppleFoundationModelsInfo records whether the local Apple on-device model
// path is available and runtime-verified through the FoundationModels framework.
type AppleFoundationModelsInfo struct {
	Available bool   `json:"available" yaml:"available"`
	Verified  bool   `json:"verified,omitempty" yaml:"verified,omitempty"`
	Version   string `json:"version,omitempty" yaml:"version,omitempty"`
	Error     string `json:"error,omitempty" yaml:"error,omitempty"`

	// State is ready (availability .available), cold (.modelNotReady),
	// unavailable (Apple Intelligence not enabled / device not eligible),
	// or unknown (probe timed out or gave no facts). Empty on older payloads.
	State string `json:"state,omitempty" yaml:"state,omitempty"`
	// Reason is Apple's UnavailableReason when State is cold or unavailable.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// ContextWindow is SystemLanguageModel.contextSize: tokens per session,
	// input plus output.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// Model is SystemLanguageModel.variant.displayName (macOS 27+).
	Model string `json:"model,omitempty" yaml:"model,omitempty"`
	// Capabilities lists LanguageModelCapabilities present (macOS 27+).
	Capabilities []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	// Provenance maps a field name to the API or probe that produced it,
	// matching ResidentModel.Provenance.
	Provenance map[string]string `json:"provenance,omitempty" yaml:"provenance,omitempty"`
	// PCC is Private Cloud Compute (macOS 27+), read without generating.
	PCC *ApplePCCInfo `json:"private_cloud_compute,omitempty" yaml:"private_cloud_compute,omitempty"`
}

// ApplePCCInfo is PrivateCloudComputeLanguageModel state. Inference runs on
// Apple's servers, so it leaves the cluster; nothing selects it automatically.
type ApplePCCInfo struct {
	// State is ready (availability .available) or unavailable.
	State string `json:"state" yaml:"state"`
	// Reason is Apple's UnavailableReason when State is unavailable.
	Reason        string   `json:"reason,omitempty" yaml:"reason,omitempty"`
	ContextWindow int      `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	Capabilities  []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	// Quota is the account's PCC quota as quotaUsage reported it.
	QuotaLimitReached     bool   `json:"quota_limit_reached,omitempty" yaml:"quota_limit_reached,omitempty"`
	QuotaApproachingLimit bool   `json:"quota_approaching_limit,omitempty" yaml:"quota_approaching_limit,omitempty"`
	QuotaReset            string `json:"quota_reset,omitempty" yaml:"quota_reset,omitempty"`
}

const (
	AppleFMReady       = "ready"
	AppleFMCold        = "cold"
	AppleFMUnavailable = "unavailable"
	AppleFMUnknown     = "unknown"
)
