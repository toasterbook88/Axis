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
}
