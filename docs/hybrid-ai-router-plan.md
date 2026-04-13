# AXIS Hybrid AI Router Implementation Plan

**Document Version**: 1.0  
**Last Updated**: 2026-04-08  
**Status**: Draft - Awaiting Approval  
**Target Release**: v0.8.0

---

## Executive Summary

This plan implements **Phase 1: Hybrid AI Router** (`axis llm`) that intelligently routes inference requests between local engines (Ollama, llama.cpp, MLX) and cloud providers (OpenAI, Anthropic, Groq), with automatic capability detection and cost awareness.

**Core Principle**: Minimal, backward-compatible addition. All existing commands work unchanged. New functionality lives in `axis llm` and optional config sections.

---

## Problem Statement

Users running AI workloads across multiple machines need:
1. Automatic selection between local and cloud inference
2. Cost transparency before execution
3. Fallback when local resources are exhausted
4. Unified interface regardless of provider
5. OpenCode/AI assistant compatibility

---

## Design Philosophy

From AXIS Doctrine:
- **Local-first**: Default to local inference, cloud requires explicit opt-in
- **Explicit before automatic**: Show routing decision before execution
- **Advisory only**: Routing decisions are estimates, not guarantees
- **Minimal dependencies**: No cloud SDKs, use standard HTTP
- **Backward compatible**: No breaking changes to existing commands

---

## Architecture

```
┌─────────────────────────────────────────────┐
│              axis llm "prompt"               │
│         axis llm --cloud "prompt"            │
│           axis llm --dry-run                 │
└────────────────┬────────────────────────────┘
                 │
                 ▼
┌─────────────────────────────────────────────┐
│           internal/llmrouter/                │
│                                              │
│  ┌─────────────┐    ┌───────────────────┐   │
│  │   Engine    │───▶│  Provider Registry│   │
│  │             │    │                   │   │
│  │ - Rules     │    │ - Local: Ollama   │   │
│  │ - Fallbacks │    │         llama.cpp │   │
│  │ - Cost calc │    │         MLX       │   │
│  └─────────────┘    │ - Cloud: OpenAI   │   │
│                     │         Anthropic │   │
│                     │         Groq      │   │
│                     └───────────────────┘   │
└─────────────────────────────────────────────┘
                 │
       ┌─────────┴──────────┐
       ▼                    ▼
┌─────────────┐      ┌─────────────┐
│    Local    │      │    Cloud    │
│ (placement  │      │   (HTTP)    │
│  engine)    │      │             │
└─────────────┘      └─────────────┘
```

---

## Key Design Decisions

### 1. Command Name: `axis llm`

**Rationale**: Avoids conflict with existing `placement.InferRequirements()` and `workload.InferRequirements()` functions. Clear, short, memorable.

**Rejected alternatives**:
- `axis infer` - conflicts with existing inference terminology
- `axis model` - too generic, could mean model management
- `axis ai` - vague

### 2. Local-First Default

**Behavior**:
```bash
axis llm "Summarize this code"           # Only local providers
axis llm --cloud "Summarize this code"   # Include cloud providers
```

**Rationale**: Prevents surprise cloud charges. Aligns with AXIS local-first philosophy.

### 3. No Cloud SDK Dependencies

**Approach**: Standard HTTP + provider-specific request/response structs

**Rationale**: Maintains minimal dependency budget (currently 7 direct deps)

### 4. Tiered Configuration

**Tier 1 - Auto-discovery** (zero config):
- AXIS detects Ollama on localhost:11434
- Detects llama.cpp server if running
- Detects MLX HTTP server

**Tier 2 - Simple cloud setup**:
```yaml
ai_providers:
  openai:
    api_key_env: OPENAI_API_KEY
  anthropic:
    api_key_env: ANTHROPIC_API_KEY
```

**Tier 3 - Advanced** (optional):
```yaml
ai_providers:
  openai:
    api_key_env: OPENAI_API_KEY
    priority: 80
    models:
      - name: gpt-4
        cost_per_1k: 0.03
        aliases: ["smart"]
```

---

## File Structure

```
internal/
├── llmrouter/
│   ├── models.go           # Core types: Provider, RoutingDecision, etc.
│   ├── registry.go         # Provider registry and health checking
│   ├── engine.go           # Routing logic and rules
│   ├── providers/
│   │   ├── local.go        # Local provider interface (Ollama, llama.cpp, MLX)
│   │   ├── openai.go       # OpenAI HTTP client
│   │   ├── anthropic.go    # Anthropic HTTP client
│   │   └── groq.go         # Groq HTTP client
│   ├── cost.go             # Cost tracking and budgeting
│   └── health.go           # Async health checking
├── secrets/
│   └── secrets.go          # Secure API key management (env, files, keychain)
└── config/
    └── config.go           # Extended Config with AIProviders (optional)

cmd/axis/
├── llm.go                  # axis llm command
├── llm_test.go             # Command tests
└── main.go                 # Register llmCmd()
```

---

## Core Types

```go
// internal/llmrouter/models.go

package llmrouter

// ProviderType categorizes inference providers
type ProviderType string

const (
    ProviderLocal  ProviderType = "local"
    ProviderCloud  ProviderType = "cloud"
)

// ModelCapability describes what a model can do
type ModelCapability struct {
    ContextWindow  int
    SupportsJSON   bool
    SupportsVision bool
    MaxTokens      int
}

// ProviderConfig from ~/.axis/nodes.yaml
type ProviderConfig struct {
    Name        string        `yaml:"name"`
    Type        ProviderType  `yaml:"type"`
    Endpoint    string        `yaml:"endpoint,omitempty"`     // For local/custom
    APIKeyEnv   string        `yaml:"api_key_env,omitempty"`  // Env var name
    APIKeyFile  string        `yaml:"api_key_file,omitempty"` // Path to key file
    Models      []ModelConfig `yaml:"models,omitempty"`
    Priority    int           `yaml:"priority,omitempty"`     // 0-100, higher = preferred
    Enabled     bool          `yaml:"enabled"`
}

type ModelConfig struct {
    Name       string   `yaml:"name"`
    Aliases    []string `yaml:"aliases,omitempty"`
    CostPer1K  float64  `yaml:"cost_per_1k,omitempty"`
    Capability ModelCapability
}

// RoutingDecision from router engine
type RoutingDecision struct {
    Provider    string   `json:"provider"`
    Model       string   `json:"model"`
    Endpoint    string   `json:"endpoint,omitempty"`
    Reasoning   []string `json:"reasoning"`
    EstCost     float64  `json:"estimated_cost,omitempty"`
    EstLatency  string   `json:"estimated_latency"`
    IsLocal     bool     `json:"is_local"`
    Fallbacks   []string `json:"fallback_providers,omitempty"`
    Confidence  float64  `json:"confidence"` // 0.0-1.0
}

// Provider interface implemented by all providers
type Provider interface {
    Name() string
    Type() ProviderType
    Health(ctx context.Context) (HealthStatus, error)
    SupportsModel(model string) bool
    Generate(ctx context.Context, prompt string, model string) (GenerateResult, error)
}

type GenerateResult struct {
    Response  string
    TokensIn  int
    TokensOut int
    LatencyMs int
    Cost      float64
    Error     error
}
```

---

## Routing Rules

Rules are evaluated in order, first match wins:

1. **Explicit provider requested**: `--provider openai` → Use that provider
2. **Local model hot**: Requested model loaded in GPU RAM on local node → Route to that node
3. **Local model warm**: Model available locally but not loaded → Route to local with load penalty
4. **Context window too large**: Required context > local capability → Cloud fallback (if --cloud)
5. **No local providers**: No local inference available → Cloud fallback (if --cloud)
6. **Cost preference**: If multiple options, prefer lower cost (when --prefer cost)
7. **Latency preference**: If multiple options, prefer lower latency (when --prefer latency)

**Tie-breakers**: Priority config value → Cost → Latency → Alphanumeric

---

## Command Interface

```bash
# Basic usage (local only)
axis llm "Summarize this code"

# Include cloud providers
axis llm --cloud "Summarize this code"

# Specific model
axis llm --model llama3.1 "Summarize this code"
axis llm --model openai/gpt-4 "Complex reasoning"

# Preview routing decision without executing
axis llm --dry-run "Summarize this code"

# Prefer cost over latency
axis llm --cloud --prefer cost "Generate marketing copy"

# List available providers and models
axis llm providers

# Test provider health
axis llm providers test

# Show usage and costs
axis llm usage --today
axis llm usage --this-week

# OpenCode-optimized output
axis llm --format opencode "Task description"
```

---

## Security Design

### API Key Storage

**Priority order** (first found wins):
1. Environment variable (e.g., `OPENAI_API_KEY`)
2. File at path (e.g., `~/.axis/secrets/openai.key`)
3. macOS Keychain / Linux secret-service (future)

**Never**:
- Store keys directly in nodes.yaml
- Log keys in debug output
- Include keys in error messages

### Provider Isolation

- Cloud providers marked as `type: "cloud"` in output
- `--local-only` flag for air-gapped environments
- Separate timeout configs for cloud (shorter) vs local (longer)

### Prompt Safety

- Routing decisions based on prompt length, not content
- No user-controlled routing logic
- Sanitize cloud API errors (don't leak full HTTP dumps)

---

## OpenCode Integration

### Automatic Configuration

During `axis init`:
```
$ axis init
... existing setup ...
Configure for OpenCode? [Y/n]: Y
Writing ~/.axis/opencode.json...
Add this to your .opencode settings:
{
  "mcpServers": {
    "axis": {
      "command": "axis",
      "args": ["mcp", "serve"]
    }
  }
}
```

### MCP Tools

New tools for OpenCode:
- `axis_llm` - Route and execute inference
- `axis_llm_available` - List available models
- `axis_llm_estimate` - Preview cost/latency

### Context Injection

```bash
axis context --for-opencode
# Outputs structured context:
# {
#   "providers": [...],
#   "available_models": [...],
#   "suggested_provider": "...",
#   "estimated_costs": {...}
# }
```

---

## Configuration Schema

### ~/.axis/nodes.yaml (Extended)

```yaml
# Existing nodes section (unchanged)
nodes:
  - name: workstation
    hostname: 192.168.1.50
    ssh_user: axis
    role: primary

# NEW: Optional AI providers section
ai_providers:
  # Local providers (auto-detected if not specified)
  ollama-local:
    type: local
    endpoint: http://localhost:11434
    enabled: true
    models:
      - name: llama3.1:8b
        aliases: [llama3, fast]
      - name: qwen2.5:14b
        aliases: [qwen]

  # Cloud providers (require API keys)
  openai:
    type: cloud
    api_key_env: OPENAI_API_KEY
    enabled: true
    priority: 80
    models:
      - name: gpt-4o
        cost_per_1k: 0.005
        aliases: [smart, gpt4]
      - name: gpt-4o-mini
        cost_per_1k: 0.00015
        aliases: [cheap, mini]

  anthropic:
    type: cloud
    api_key_env: ANTHROPIC_API_KEY
    enabled: true
    priority: 70
    models:
      - name: claude-3-sonnet
        cost_per_1k: 0.003

  groq:
    type: cloud
    api_key_env: GROQ_API_KEY
    enabled: true
    priority: 90  # High priority for fast/cheap
    models:
      - name: llama-3.1-70b-versatile
        cost_per_1k: 0.0009
        aliases: [fast-cloud]

# NEW: Optional inference preferences
inference:
  default_mode: local           # local | cloud | auto
  prefer: latency               # latency | cost | quality
  max_cost_per_request: 0.10    # Dollars
  budget_alert_threshold: 10.00 # Alert when daily spend exceeds
```

---

## Backward Compatibility

**Guarantees**:
- ✅ All existing commands unchanged
- ✅ Existing configs work without modification
- ✅ New features are strictly additive
- ✅ `ai_providers` section is optional
- ✅ Without it, `axis llm` defaults to auto-detected local Ollama

**Config validation**:
- Uses `omitempty` tags for new fields
- Maintains `KnownFields(true)` strict parsing
- Graceful degradation: missing AI config → local-only mode

---

## Implementation Phases

### Phase 1: Core Infrastructure (Week 1-2)

**Goals**: Basic routing infrastructure

**Deliverables**:
- [ ] `internal/llmrouter/models.go` - Core types
- [ ] `internal/llmrouter/registry.go` - Provider registry
- [ ] `internal/secrets/secrets.go` - API key management
- [ ] `internal/config/config.go` - Extended Config with AIProviders
- [ ] Unit tests for all above

**Success criteria**:
- Types compile, tests pass
- Config loads with/without AI providers

### Phase 2: Local Providers (Week 3)

**Goals**: Local inference working

**Deliverables**:
- [ ] `internal/llmrouter/providers/local.go`
- [ ] Ollama integration (uses existing chat client)
- [ ] llama.cpp server detection
- [ ] MLX detection
- [ ] Warm model detection (extend existing fact collection)

**Success criteria**:
- `axis llm "hello"` works with local Ollama
- Warm model detection improves routing

### Phase 3: Cloud Providers (Week 4)

**Goals**: Cloud inference working

**Deliverables**:
- [ ] `internal/llmrouter/providers/openai.go`
- [ ] `internal/llmrouter/providers/anthropic.go`
- [ ] `internal/llmrouter/providers/groq.go`
- [ ] Cost tracking to `~/.axis/usage.json`

**Success criteria**:
- `axis llm --cloud "hello"` routes to cloud when local unavailable
- Cost estimates accurate within 20%

### Phase 4: CLI & Polish (Week 5)

**Goals**: Complete command interface

**Deliverables**:
- [ ] `cmd/axis/llm.go` - Main command
- [ ] `axis llm --dry-run`
- [ ] `axis llm providers`
- [ ] `axis llm usage`
- [ ] Integration tests

**Success criteria**:
- All commands work as documented
- Error messages are helpful

### Phase 5: OpenCode Integration (Week 6)

**Goals**: AI assistant compatibility

**Deliverables**:
- [ ] Enhanced `axis init` with OpenCode setup
- [ ] `axis context --for-opencode`
- [ ] MCP tools for OpenCode
- [ ] Documentation and examples

**Success criteria**:
- OpenCode can use AXIS for cluster-aware inference
- Setup is one command

### Phase 6: Release (Week 7-8)

**Goals**: Production ready

**Deliverables**:
- [ ] Performance testing
- [ ] Security review
- [ ] Documentation complete
- [ ] Release notes
- [ ] Demo video/script

**Success criteria**:
- All tests pass
- Documentation matches implementation
- No known security issues

---

## Testing Strategy

### Unit Tests

```go
// Mock providers for testing
type mockProvider struct {
    name string
    healthy bool
    models []string
}

func TestRouter_LocalOnly(t *testing.T) {
    // Given only local providers
    // When routing without --cloud
    // Then select local provider
}

func TestRouter_FallbackToCloud(t *testing.T) {
    // Given no local providers
    // When routing with --cloud
    // Then select cloud provider
}

func TestRouter_WarmModelPreference(t *testing.T) {
    // Given model loaded on node-a
    // When routing request for that model
    // Then select node-a even if other nodes have more RAM
}
```

### Integration Tests

```bash
# Mark with build tag: //go:build integration

# Test against real Ollama (if running)
# Test against mock cloud servers
# Never test against real cloud APIs in CI (cost + rate limits)
```

### Mock Cloud Servers

```go
// internal/llmrouter/testutil/mock_openai.go
// Returns canned responses for testing
// Validates request format
// Simulates rate limits and errors
```

---

## Risks and Mitigations

| Risk | Impact | Mitigation |
|------|--------|------------|
| Surprise cloud charges | High | `--cloud` flag required, cost preview, budget limits |
| API key leakage | Critical | Env vars only, no logging, separate secrets package |
| Dependency bloat | Medium | HTTP only, no cloud SDKs |
| Provider API changes | Medium | Versioned interfaces, minimal feature set |
| Scope creep | Medium | Document out-of-scope features, strict phase gates |
| Testing complexity | Medium | Mocks, integration build tags, no real cloud in CI |

---

## Out of Scope (v0.8.0)

Explicitly NOT included:
- ❌ Model downloading/management (use Ollama)
- ❌ Model conversion/quantization
- ❌ Fine-tuning or training
- ❌ Multi-model ensembles
- ❌ Streaming responses (may add later)
- ❌ Vision/multimodal (text only)
- ❌ Function calling (may add later)
- ❌ Custom AXIS inference engine (Phase 2)

---

## Success Metrics

- [ ] `axis llm` works with zero config (auto-detected Ollama)
- [ ] Cloud routing requires explicit `--cloud` flag
- [ ] Cost estimates within 20% of actual
- [ ] Local routing latency < 100ms overhead
- [ ] All existing tests pass
- [ ] New code coverage > 70%
- [ ] OpenCode integration tested end-to-end

---

## Decision Log

| Date | Decision | Rationale |
|------|----------|-----------|
| 2026-04-08 | Command: `axis llm` | Avoids conflict with `placement.Infer` |
| 2026-04-08 | Local-first default | Prevents surprise charges |
| 2026-04-08 | No cloud SDKs | Maintains minimal dependencies |
| 2026-04-08 | Env var API keys | Security best practice |
| 2026-04-08 | Tiered config | Reduces complexity for simple cases |

---

## Appendix A: Example Usage Scenarios

### Scenario 1: Developer with Ollama
```bash
$ axis llm "Explain this error"
# Routes to local Ollama, zero config
```

### Scenario 2: Mixed local/cloud
```bash
$ axis llm --cloud "Write a novel"  # Large context
# Local: context window too small
# Cloud: OpenAI selected, cost ~$0.05
# Confirm? [Y/n]: Y
```

### Scenario 3: Cost-conscious
```bash
$ axis llm --cloud --prefer cost "Summarize"
# Groq selected ($0.0001) vs OpenAI ($0.005)
```

### Scenario 4: OpenCode integration
```bash
# In OpenCode:
# "@axis run inference on the best available model"
# → Calls axis_llm MCP tool
# → Gets routing decision
# → Shows cost estimate
# → Executes with user confirmation
```

---

## Appendix B: Provider Implementation Notes

### Ollama
- Use existing `internal/chat` client
- Endpoint: `http://localhost:11434`
- API: `/api/generate` and `/api/chat`
- Health: `GET /`

### llama.cpp Server
- Endpoint: `http://localhost:8080` (configurable)
- API: OpenAI-compatible `/v1/chat/completions`
- Health: `GET /health`

### MLX HTTP Server
- Endpoint: `http://localhost:8080` (configurable)
- API: Custom (documentation TBD)
- Health: `GET /`

### OpenAI
- Endpoint: `https://api.openai.com/v1`
- API: `/chat/completions`
- Auth: Bearer token header
- Models: gpt-4o, gpt-4o-mini, gpt-3.5-turbo

### Anthropic
- Endpoint: `https://api.anthropic.com/v1`
- API: `/messages`
- Auth: x-api-key header
- Models: claude-3-opus, claude-3-sonnet, claude-3-haiku

### Groq
- Endpoint: `https://api.groq.com/openai/v1`
- API: OpenAI-compatible
- Auth: Bearer token
- Models: llama-3.1-70b, llama-3.1-8b, mixtral-8x7b

---

## Approval

- [ ] Architecture review complete
- [ ] Security review complete
- [ ] Dependencies approved
- [ ] Timeline approved

**Approved by**: _________________ **Date**: _________________

---

## Related Documents

- [AXIS Doctrine](../doctrine.md)
- [Current State](../current-state.md)
- [Future Roadmap](../future-roadmap.md)
- [Invariants](../invariants.md)
- [White Paper](../white_paper_v1.md)

---

*This document is a living specification. Updates should be logged in the Decision Log section.*
