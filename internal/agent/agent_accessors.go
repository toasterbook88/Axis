package agent

import (
	"github.com/toasterbook88/axis/internal/chat"
	"github.com/toasterbook88/axis/internal/mcpclient"
)

// Conversation returns the underlying conversation for inspection/testing.
func (a *Agent) Conversation() *chat.Conversation {
	return a.conv
}

// ContextTokens returns the estimated size of the last request sent: the
// conversation, tool definitions and turn context. Before the first request
// it is the conversation estimate. It reads a cached value and never builds
// a request, so the console's UI loop can call it.
func (a *Agent) ContextTokens() int {
	if a == nil || a.conv == nil {
		return 0
	}
	if e := a.lastRequest(); e > 0 {
		return e
	}
	return a.conv.EstimateTokens()
}

// MaxTokens returns the usable request budget: the smallest of --max-tokens,
// the served window and a window learned from an overflow error, minus the
// reply reserve.
func (a *Agent) MaxTokens() int {
	_, u := a.budget()
	return u
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
	a.bud = budgetState{targetWindow: max(0, contextWindow), gen: a.bud.gen + 1, warned: a.bud.warned}
}

// Backend returns the active LLM backend.
func (a *Agent) Backend() ChatBackend {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return a.client
}

// backendState returns the backend, its classification and its budget state
// from one read, so a model round or a subagent never pairs one backend with
// another's generation or learned window.
func (a *Agent) backendState() (ChatBackend, BackendSecurityClass, budgetState) {
	a.clientMu.RLock()
	defer a.clientMu.RUnlock()
	return a.client, a.securityClass, a.bud
}

// Model returns the current active model name.
func (a *Agent) Model() string {
	a.modelMu.RLock()
	defer a.modelMu.RUnlock()
	return a.model
}
