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
}

const (
	AppleFMReady       = "ready"
	AppleFMCold        = "cold"
	AppleFMUnavailable = "unavailable"
	AppleFMUnknown     = "unknown"
)
