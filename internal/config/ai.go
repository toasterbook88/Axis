package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/toasterbook88/axis/internal/persist"
	"gopkg.in/yaml.v3"
)

// Supported AI backend kinds for ~/.axis/ai.yaml.
const (
	AIBackendOllama           = "ollama"
	AIBackendOpenAICompatible = "openai-compatible"
	AIBackendLlamaCPP         = "llamacpp"
	AIBackendMLX              = "mlx"
)

// AIBackendConfig describes one inference backend endpoint.
// Topology is operator-local; AXIS never ships real cluster addresses.
type AIBackendConfig struct {
	// Name is the registry key referenced by roles.prefer.
	Name string `json:"name" yaml:"name"`

	// Kind is one of: ollama, openai-compatible, llamacpp, mlx.
	Kind string `json:"kind" yaml:"kind"`

	// BaseURL is the HTTP base for health probes and client calls.
	// Examples: http://127.0.0.1:11434  or  http://127.0.0.1:4000/v1
	BaseURL string `json:"base_url" yaml:"base_url"`

	// Node optionally ties this backend to a nodes.yaml node name (placement hints).
	Node string `json:"node,omitempty" yaml:"node,omitempty"`

	// Enabled defaults to true when omitted in YAML (see Normalize).
	Enabled *bool `json:"enabled,omitempty" yaml:"enabled,omitempty"`

	// APIKeyEnv is an optional env var name for Authorization on openai-compatible hubs.
	APIKeyEnv string `json:"api_key_env,omitempty" yaml:"api_key_env,omitempty"`

	// APIKeyFile is an optional file whose contents are the API key.
	APIKeyFile string `json:"api_key_file,omitempty" yaml:"api_key_file,omitempty"`
}

// IsEnabled reports whether the backend participates in routing.
// Nil Enabled means true (present and usable by default).
func (b AIBackendConfig) IsEnabled() bool {
	if b.Enabled == nil {
		return true
	}
	return *b.Enabled
}

// AIRoleConfig maps a logical role to preferred backends and a model id.
type AIRoleConfig struct {
	// Prefer is an ordered list of backend names (first healthy wins).
	Prefer []string `json:"prefer" yaml:"prefer"`

	// Model is the model tag or hub model id to request.
	Model string `json:"model" yaml:"model"`

	// RequireArch is an optional GOARCH-style hint (e.g. arm64, amd64).
	RequireArch string `json:"require_arch,omitempty" yaml:"require_arch,omitempty"`
}

// AIConfig is the top-level structure for ~/.axis/ai.yaml.
type AIConfig struct {
	Backends []AIBackendConfig       `json:"backends" yaml:"backends"`
	Roles    map[string]AIRoleConfig `json:"roles" yaml:"roles"`
}

// DefaultAIConfigPath returns ~/.axis/ai.yaml.
func DefaultAIConfigPath() string {
	return persist.AxisPath("ai.yaml")
}

// LoadAI reads, normalizes, and validates an AI config file.
func LoadAI(path string) (*AIConfig, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading AI config %s: %w", path, err)
	}
	var cfg AIConfig
	if err := decodeStrictAI(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing AI config: %w", err)
	}
	cfg.Normalize()
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	return &cfg, nil
}

// LoadAIOrEmpty loads path when it exists; missing file yields an empty config.
func LoadAIOrEmpty(path string) (*AIConfig, error) {
	if path == "" {
		path = DefaultAIConfigPath()
	}
	if _, err := os.Stat(path); err != nil {
		if os.IsNotExist(err) {
			cfg := &AIConfig{Roles: map[string]AIRoleConfig{}}
			return cfg, nil
		}
		return nil, err
	}
	return LoadAI(path)
}

func decodeStrictAI(data []byte, cfg *AIConfig) error {
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != nil {
		if err == io.EOF {
			return nil
		}
		return err
	}
	return fmt.Errorf("multiple YAML documents are not supported")
}

// Normalize trims strings and initializes maps.
func (c *AIConfig) Normalize() {
	if c == nil {
		return
	}
	if c.Roles == nil {
		c.Roles = map[string]AIRoleConfig{}
	}
	for i := range c.Backends {
		b := &c.Backends[i]
		b.Name = strings.TrimSpace(b.Name)
		b.Kind = strings.ToLower(strings.TrimSpace(b.Kind))
		b.BaseURL = strings.TrimRight(strings.TrimSpace(b.BaseURL), "/")
		b.Node = strings.TrimSpace(b.Node)
		b.APIKeyEnv = strings.TrimSpace(b.APIKeyEnv)
		b.APIKeyFile = strings.TrimSpace(b.APIKeyFile)
	}
	normalized := make(map[string]AIRoleConfig, len(c.Roles))
	for name, role := range c.Roles {
		key := strings.TrimSpace(name)
		role.Model = strings.TrimSpace(role.Model)
		role.RequireArch = strings.ToLower(strings.TrimSpace(role.RequireArch))
		prefer := make([]string, 0, len(role.Prefer))
		for _, p := range role.Prefer {
			p = strings.TrimSpace(p)
			if p != "" {
				prefer = append(prefer, p)
			}
		}
		role.Prefer = prefer
		normalized[key] = role
	}
	c.Roles = normalized
}

// Validate checks AI config invariants.
func (c *AIConfig) Validate() error {
	if c == nil {
		return fmt.Errorf("AI config is nil")
	}
	backendNames := make(map[string]bool, len(c.Backends))
	for i, b := range c.Backends {
		if b.Name == "" {
			return fmt.Errorf("AI config: backends[%d] missing name", i)
		}
		lower := strings.ToLower(b.Name)
		if backendNames[lower] {
			return fmt.Errorf("AI config: duplicate backend name %q", b.Name)
		}
		backendNames[lower] = true
		if err := validateAIBackendKind(b.Kind); err != nil {
			return fmt.Errorf("AI config: backend %q: %w", b.Name, err)
		}
		if b.BaseURL == "" {
			return fmt.Errorf("AI config: backend %q missing base_url", b.Name)
		}
		if !strings.HasPrefix(b.BaseURL, "http://") && !strings.HasPrefix(b.BaseURL, "https://") {
			return fmt.Errorf("AI config: backend %q base_url must be http(s)", b.Name)
		}
	}
	for name, role := range c.Roles {
		if name == "" {
			return fmt.Errorf("AI config: empty role name")
		}
		if role.Model == "" {
			return fmt.Errorf("AI config: role %q missing model", name)
		}
		if len(role.Prefer) == 0 {
			return fmt.Errorf("AI config: role %q has empty prefer list", name)
		}
		for _, pref := range role.Prefer {
			if !backendNames[strings.ToLower(pref)] {
				return fmt.Errorf("AI config: role %q prefer references unknown backend %q", name, pref)
			}
		}
	}
	return nil
}

func validateAIBackendKind(kind string) error {
	switch kind {
	case AIBackendOllama, AIBackendOpenAICompatible, AIBackendLlamaCPP, AIBackendMLX:
		return nil
	case "":
		return fmt.Errorf("missing kind")
	default:
		return fmt.Errorf("unsupported kind %q (want ollama|openai-compatible|llamacpp|mlx)", kind)
	}
}

// FindBackend returns a backend by name (case-insensitive).
func (c *AIConfig) FindBackend(name string) (AIBackendConfig, bool) {
	if c == nil {
		return AIBackendConfig{}, false
	}
	for _, b := range c.Backends {
		if strings.EqualFold(b.Name, name) {
			return b, true
		}
	}
	return AIBackendConfig{}, false
}

// BackendByName returns a map of lower(name) → backend for enabled backends only when enabledOnly is true.
func (c *AIConfig) BackendByName(enabledOnly bool) map[string]AIBackendConfig {
	out := map[string]AIBackendConfig{}
	if c == nil {
		return out
	}
	for _, b := range c.Backends {
		if enabledOnly && !b.IsEnabled() {
			continue
		}
		out[strings.ToLower(b.Name)] = b
	}
	return out
}
