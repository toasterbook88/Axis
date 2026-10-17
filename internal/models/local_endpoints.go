package models

// LocalEndpoints is what a sweep of the node's loopback listeners found
// beyond the ports the Ollama, llama.cpp, and MLX probes already own.
// Confirmed endpoints answered a known model-listing dialect and named at
// least one model; the catalog lists those models as listed, never loaded.
// Candidates answered HTTP but named no model; nothing reads them for
// placement or model choice.
type LocalEndpoints struct {
	Confirmed  []LocalEndpoint          `json:"confirmed,omitempty" yaml:"confirmed,omitempty"`
	Candidates []LocalEndpointCandidate `json:"candidates,omitempty" yaml:"candidates,omitempty"`
	// Truncated is true when the port cap or the time budget cut the sweep.
	Truncated bool `json:"truncated,omitempty" yaml:"truncated,omitempty"`
	// Reason explains an empty sweep, e.g. no-listener-tool or no-curl.
	Reason string `json:"reason,omitempty" yaml:"reason,omitempty"`
	// SkippedReserved lists listening ports the sweep never dials because
	// they are known non-HTTP services (sshd, Samba, databases).
	SkippedReserved []int `json:"skipped_reserved,omitempty" yaml:"skipped_reserved,omitempty"`
	// SkippedEphemeral counts OS ephemeral-range listeners left unprobed
	// because no model-server process name was visible.
	SkippedEphemeral int `json:"skipped_ephemeral,omitempty" yaml:"skipped_ephemeral,omitempty"`
	// SkippedNotAllowlisted counts listeners omitted by the default scope.
	SkippedNotAllowlisted int `json:"skipped_not_allowlisted,omitempty" yaml:"skipped_not_allowlisted,omitempty"`
	// SkippedExcluded lists explicitly denied listener ports.
	SkippedExcluded []int `json:"skipped_excluded,omitempty" yaml:"skipped_excluded,omitempty"`
	// CachedSweep indicates that the whole result was reused without dialing.
	CachedSweep bool `json:"cached_sweep,omitempty" yaml:"cached_sweep,omitempty"`
}

// LocalEndpoint is a loopback port that named its models.
type LocalEndpoint struct {
	Port int `json:"port" yaml:"port"`
	// Bind is loopback (reachable only on the node) or any (wildcard).
	Bind string `json:"bind" yaml:"bind"`
	// Dialect is the API that answered: ollama (/api/tags) or openai (/v1/models).
	Dialect string `json:"dialect" yaml:"dialect"`
	// Engine comes from the response (owned_by), else openai-compatible.
	Engine     string               `json:"engine" yaml:"engine"`
	Process    string               `json:"process,omitempty" yaml:"process,omitempty"`
	Models     []LocalEndpointModel `json:"models" yaml:"models"`
	Provenance map[string]string    `json:"provenance,omitempty" yaml:"provenance,omitempty"`
}

// LocalEndpointModel is one model id an endpoint listed.
type LocalEndpointModel struct {
	Name string `json:"name" yaml:"name"`
	// ContextWindow is set only when the response states the served
	// context (vLLM max_model_len); 0 = unknown.
	ContextWindow int `json:"context_window,omitempty" yaml:"context_window,omitempty"`
	// RemoteHost is set when an Ollama listing marks the model as proxied
	// to a cloud host.
	RemoteHost string `json:"remote_host,omitempty" yaml:"remote_host,omitempty"`
}

// LocalEndpointCandidate is a loopback HTTP listener that did not name a
// model: it needs auth, speaks another API, or could not be parsed.
type LocalEndpointCandidate struct {
	Port    int    `json:"port" yaml:"port"`
	Process string `json:"process,omitempty" yaml:"process,omitempty"`
	Reason  string `json:"reason" yaml:"reason"`
	// Cached is true when the result was reused from an earlier sweep of the
	// same socket on this node (at most 24 h old) instead of probed this run.
	Cached bool `json:"cached,omitempty" yaml:"cached,omitempty"`
}
