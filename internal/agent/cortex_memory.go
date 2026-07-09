package agent

// cortexMemoryGuidance returns the system-prompt instructions that make the
// Cortex cluster-memory tools first-class when a Cortex MCP server is
// connected. It is only injected when mcp_cortex_recall / mcp_cortex_remember
// are registered, so agents without Cortex are unaffected.
func cortexMemoryGuidance() string {
	return "Cluster-shared memory (Cortex) is connected. Treat it as a first-class part of working across the cluster:\n" +
		"- Before starting non-trivial work, call `mcp_cortex_recall` with a query describing the task to surface relevant past discoveries, decisions, and gotchas. Don't redo work someone already figured out.\n" +
		"- When you discover something worth keeping — a working approach, a node quirk, a configuration gotcha, a placement lesson — call `mcp_cortex_remember` to persist it for future sessions and other agents.\n" +
		"- Before mutating shared cluster files (anything outside the current working tree that other agents/sessions might touch), call `mcp_cortex_acquire_lock` on the resource, do the work, then call `mcp_cortex_release_lock` when done. This prevents concurrent agents from clobbering each other.\n" +
		"- After significant changes (a file modified, a service restarted, a credential rotated, a task completed), call `mcp_cortex_publish_event` so other live sessions can react.\n" +
		"- Use `mcp_cortex_list_sessions` to see what other agents are active before assuming a resource is free.\n" +
		"Memory is advisory: treat recalled content as prior context to verify, not as authority that overrides a live probe or snapshot.\n"
}
