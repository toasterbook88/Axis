package main

import "github.com/toasterbook88/axis/internal/agent"

// agentSafetyLabel is the "Safety Gate" text in the session banner. An
// explicit autonomy mode wins over the --auto-approve wording.
func agentSafetyLabel(autoApprove bool, autonomy string) string {
	switch agent.AutonomyMode(autonomy) {
	case agent.AutonomyCluster:
		return "Autonomy: cluster (cluster reads only)"
	case agent.AutonomyEdit:
		return "Autonomy: edit (auto-approve edits)"
	case agent.AutonomyFull:
		return "Autonomy: full (auto-approve all but safety-blocked and unlisted MCP)"
	}
	if autoApprove {
		return "Auto-Approve safe (<70)"
	}
	return "Strict Operator Approval"
}
