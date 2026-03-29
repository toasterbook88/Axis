package turboexec

import (
	"fmt"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

type Plan struct {
	Command string
	Env     []string
	Notes   []string
}

func Prepare(node models.NodeFacts, reqs models.TaskRequirements, command string) Plan {
	plan := Plan{Command: command}
	if node.TurboQuant == nil || !node.TurboQuant.Supported {
		return plan
	}

	status := "detected"
	if node.TurboQuant.Verified {
		status = "verified"
	}

	plan.Env = append(plan.Env,
		"AXIS_TURBOQUANT=1",
		"AXIS_TURBOQUANT_STATUS="+status,
		"AXIS_TURBOQUANT_BACKENDS="+strings.Join(node.TurboQuant.Backends, ","),
		"AXIS_TURBOQUANT_CAPABILITIES="+strings.Join(node.TurboQuant.Capabilities, ","),
	)
	plan.Notes = append(plan.Notes,
		fmt.Sprintf("turboquant %s on selected node", status))

	if reqs.PrefersTurboQuant {
		plan.Env = append(plan.Env, "AXIS_TURBOQUANT_REQUESTED=1")
	}
	if reqs.ContextWindowTokens > 0 {
		plan.Env = append(plan.Env, fmt.Sprintf("AXIS_TURBOQUANT_CONTEXT_TOKENS=%d", reqs.ContextWindowTokens))
	}

	if !node.TurboQuant.Verified || reqs.ContextWindowTokens == 0 {
		return plan
	}

	runtime := runtimeCommand(command)
	switch runtime {
	case "llama-cli", "llama-server":
		if !hasAnyFlag(command, "--ctx-size", "-c") {
			plan.Command += fmt.Sprintf(" --ctx-size %d", reqs.ContextWindowTokens)
			plan.Notes = append(plan.Notes,
				fmt.Sprintf("turboquant injected --ctx-size %d", reqs.ContextWindowTokens))
		}
		if hasCapability(node, "flash-attn-flag") && !hasAnyFlag(command, "--flash-attn", "-fa") {
			plan.Command += " --flash-attn"
			plan.Notes = append(plan.Notes, "turboquant injected --flash-attn")
		}
	}

	return plan
}

func runtimeCommand(command string) string {
	fields := strings.Fields(command)
	for _, field := range fields {
		if strings.Contains(field, "=") && !strings.HasPrefix(field, "-") {
			continue
		}
		return field
	}
	return ""
}

func hasAnyFlag(command string, flags ...string) bool {
	for _, flag := range flags {
		if strings.Contains(command, flag) {
			return true
		}
	}
	return false
}

func hasCapability(node models.NodeFacts, capability string) bool {
	if node.TurboQuant == nil {
		return false
	}
	for _, cap := range node.TurboQuant.Capabilities {
		if cap == capability {
			return true
		}
	}
	return false
}
