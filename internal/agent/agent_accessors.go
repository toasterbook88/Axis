package agent

import (
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// Conversation returns the underlying conversation for inspection/testing.
func (a *Agent) Conversation() *chat.Conversation {
	return a.conv
}

// ContextTokens returns the current estimated conversation tokens.
func (a *Agent) ContextTokens() int {
	if a == nil || a.conv == nil {
		return 0
	}
	return a.conv.EstimateTokens()
}

// MaxTokens returns the session token limit.
func (a *Agent) MaxTokens() int {
	return a.maxTokens
}

// ContextBudget returns the request budget B (the smallest of --max-tokens,
// the served window and a window learned from an overflow error) and the
// usable part U after the reply reserve.
func (a *Agent) ContextBudget() (b, u int) {
	return a.budget()
}

// MCPRegistry returns the connected MCP client registry.
func (a *Agent) MCPRegistry() *mcpclient.Registry {
	return a.mcpRegistry
}

// SetBackend changes the active LLM backend, its trust classification and
// its served context window (0 = unknown). All change under one write lock
// so readers never observe a backend paired with the previous backend's
// classification or budget. The window and estimate factor learned from the
// previous backend are dropped.
func (a *Agent) SetBackend(client ChatBackend, securityClass BackendSecurityClass, contextWindow int) {
	a.clientMu.Lock()
	defer a.clientMu.Unlock()
	a.client = client
	a.securityClass = securityClass
	a.bud = budgetState{targetWindow: max(0, contextWindow), gen: a.bud.gen + 1}
}

// Backend returns the active LLM backend.
func (a *Agent) Backend() ChatBackend {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return a.client
}

// backendAndClass returns the backend and its classification as one
// consistent pair, for callers that need both (subagent construction).
func (a *Agent) backendAndClass() (ChatBackend, BackendSecurityClass) {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return a.client, a.securityClass
}

// Model returns the current active model name.
func (a *Agent) Model() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}
