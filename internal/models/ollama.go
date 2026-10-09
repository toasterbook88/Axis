package models

// OllamaInfo is collected in addition to the normal ToolInfo for "ollama".
// This is what makes discovery actually useful for placement and task run.
type OllamaInfo struct {
	Installed  bool     `json:"installed" yaml:"installed"`
	Path       string   `json:"path,omitempty" yaml:"path,omitempty"`
	Version    string   `json:"version,omitempty" yaml:"version,omitempty"`
	Running    bool     `json:"running" yaml:"running"`
	Listening  bool     `json:"listening" yaml:"listening"`
	Port       int      `json:"port,omitempty" yaml:"port,omitempty"`
	Models     []string `json:"models,omitempty" yaml:"models,omitempty"`
	GPUOffload string   `json:"gpu_offload,omitempty" yaml:"gpu_offload,omitempty"`
	// DefaultKeepAlive is the process-level Ollama default keep-alive
	// duration string (e.g. "5m", "1h"). Populated from /api/ps on
	// Ollama 0.3.10+; empty when unknown or on older Ollama. The warmth
	// computation in internal/facts/local.go (applyOllamaWarmth) parses
	// this and falls back to 5m when empty.
	DefaultKeepAlive string `json:"default_keep_alive,omitempty" yaml:"default_keep_alive,omitempty"`
	// Catalog is the node's own report of its installed models from the
	// local Ollama API. It is absent when that API did not answer; Models
	// (from `ollama list`) is still reported in that case.
	Catalog []OllamaModelEntry `json:"catalog,omitempty" yaml:"catalog,omitempty"`
	Error   string             `json:"error,omitempty" yaml:"error,omitempty"`
}

// OllamaModelEntry is one installed Ollama model as the node itself observes
// it. Installed is not loaded; loaded models appear in ResidentModels.
type OllamaModelEntry struct {
	Name string `json:"name" yaml:"name"`
	// RemoteHost is set for cloud-proxy entries: the weights are not on this
	// node and inference leaves the cluster.
	RemoteHost  string `json:"remote_host,omitempty" yaml:"remote_host,omitempty"`
	RemoteModel string `json:"remote_model,omitempty" yaml:"remote_model,omitempty"`
	// Capabilities is Ollama's /api/show list (e.g. completion, tools,
	// embedding, vision, thinking). Empty means unknown, not "none".
	Capabilities  []string `json:"capabilities,omitempty" yaml:"capabilities,omitempty"`
	Family        string   `json:"family,omitempty" yaml:"family,omitempty"`
	ParameterSize string   `json:"parameter_size,omitempty" yaml:"parameter_size,omitempty"`
	Quantization  string   `json:"quantization,omitempty" yaml:"quantization,omitempty"`
	SizeBytes     int64    `json:"size_bytes,omitempty" yaml:"size_bytes,omitempty"`
}

// CloneOllamaCatalog deep-copies a catalog, including each entry's
// Capabilities, so a snapshot clone shares no slice with the original.
func CloneOllamaCatalog(in []OllamaModelEntry) []OllamaModelEntry {
	if in == nil {
		return nil
	}
	out := make([]OllamaModelEntry, len(in))
	for i, e := range in {
		e.Capabilities = append([]string(nil), e.Capabilities...)
		out[i] = e
	}
	return out
}
