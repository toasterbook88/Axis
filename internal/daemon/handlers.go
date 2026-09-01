package daemon

type ToolDef struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	InputSchema map[string]any `json:"input_schema"`
}

type ToolsResponse struct {
	Tools []ToolDef `json:"tools"`
}

func HealthPayload(meta *Metadata) map[string]any {
	payload := map[string]any{
		"status":  "ok",
		"name":    "axis",
		"version": Version,
	}
	if meta == nil {
		return payload
	}

	payload["cache_ready"] = meta.Ready
	if meta.PublicationID != "" {
		payload["publication_id"] = meta.PublicationID
	}
	payload["cache_stale"] = meta.Stale
	payload["cache_age_sec"] = meta.CacheAgeSec
	payload["refresh_count"] = meta.RefreshCount
	if meta.LastRefreshTrigger != "" {
		payload["last_refresh_trigger"] = meta.LastRefreshTrigger
	}
	if meta.LastRefreshMs > 0 {
		payload["last_refresh_duration_ms"] = meta.LastRefreshMs
	}
	if !meta.LastConfigEventAt.IsZero() {
		payload["last_config_event_at"] = meta.LastConfigEventAt
	}
	if len(meta.StaleNodes) > 0 {
		payload["stale_nodes"] = meta.StaleNodes
	}
	if !meta.CollectedAt.IsZero() {
		payload["cache_collected_at"] = meta.CollectedAt
	}
	if meta.LastError != "" {
		payload["cache_last_error"] = meta.LastError
	}
	if meta.Freshness != nil {
		payload["discovery_freshness"] = meta.Freshness
	}
	return payload
}

// ToolDefinitions is the single canonical source for the AXIS tool schemas
// advertised by the production HTTP API and MCP server. Do not copy these
// definitions into another package.
func ToolDefinitions() []ToolDef {
	return []ToolDef{
		{
			Name:        "axis_execute",
			Description: "Execute a task on the live AXIS cluster using placement, learned skills/scripts, live RAM pressure, and the safety blocker. Explicit mode and explicit operator confirmation are required: use mode=script for matched scripts/skills only or mode=exec for explicit raw commands, and set confirm=YES to authorize execution.",
			InputSchema: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"description": map[string]any{"type": "string", "description": "Natural language task description or raw command"},
					"mode":        map[string]any{"type": "string", "description": "Execution mode: script or exec"},
					"confirm":     map[string]any{"type": "string", "description": "Must be YES to authorize execution"},
				},
				"required": []string{"description", "mode", "confirm"},
			},
		},
		{
			Name:        "axis_knowledge",
			Description: "Return live cluster state, Ollama status, learned skills, and recent placement state.",
			InputSchema: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
	}
}
