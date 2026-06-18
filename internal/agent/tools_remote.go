package agent

import (
	"context"
	"encoding/json"
	"fmt"
)

// registerRemoteExecutionTool registers the axis_run_task tool.
func (r *ToolRegistry) registerRemoteExecutionTool() {
	r.add("axis_run_task",
		"Run a task on the best matching node in the AXIS cluster using placement, safety gating, and resource reservation. Optional target_node, memory_request_mb, and expose_ports [local:]remote port forwarding (e.g. 8080:8080) are supported.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"description":{"type":"string","description":"The command or description of the task to run"},
				"mode":{"type":"string","description":"Optional mode: exec (default) or script"},
				"target_node":{"type":"string","description":"Optional target node name. If omitted, the placement ranker selects the best node."},
				"memory_request_mb":{"type":"integer","description":"Optional explicit memory request in MB"},
				"memory_max_mb":{"type":"integer","description":"Optional explicit memory max in MB"},
				"expose_ports":{"type":"string","description":"Optional port forwarding mapping (e.g. 8080:8080 or just 8080)"}
			},
			"required":["description"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a TaskRequest
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for axis_run_task: %w", err)
			}
			if a.Description == "" {
				return "", fmt.Errorf("axis_run_task requires a non-empty \"description\" argument")
			}
			return "", fmt.Errorf("axis_run_task must be dispatched through the agent safety gate")
		},
	)
}
