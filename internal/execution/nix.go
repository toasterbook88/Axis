package execution

import (
	"fmt"
	"sort"
	"strings"

	"al.essio.dev/pkg/shellescape"
	"github.com/toasterbook88/axis/internal/models"
)

type NixPlan struct {
	Enabled  bool
	Command  string
	Packages []string
	Env      []string
	Notes    []string
}

var nixToolPackages = map[string]string{
	"cargo":   "nixpkgs#cargo",
	"gcc":     "nixpkgs#gcc",
	"git":     "nixpkgs#git",
	"go":      "nixpkgs#go",
	"jq":      "nixpkgs#jq",
	"node":    "nixpkgs#nodejs",
	"python3": "nixpkgs#python3",
}

func PlanNixWrapper(node models.NodeFacts, reqs models.TaskRequirements, command string) NixPlan {
	if !hasObservedTool(node, "nix") {
		return NixPlan{}
	}
	if strings.TrimSpace(command) == "" {
		return NixPlan{}
	}
	if node.Resources != nil && strings.EqualFold(node.Resources.Pressure, "high") {
		return NixPlan{}
	}

	toolSet := make(map[string]struct{})
	for _, tool := range reqs.RequiredTools {
		tool = strings.ToLower(strings.TrimSpace(tool))
		if tool != "" {
			toolSet[tool] = struct{}{}
		}
	}
	if len(toolSet) == 0 {
		if entry := commandEntrypoint(command); entry != "" {
			toolSet[entry] = struct{}{}
		}
	}
	if len(toolSet) == 0 {
		return NixPlan{}
	}

	packages := make([]string, 0, len(toolSet))
	for tool := range toolSet {
		pkg, ok := nixToolPackages[tool]
		if !ok {
			return NixPlan{}
		}
		packages = append(packages, pkg)
	}
	sort.Strings(packages)

	if len(packages) == 0 {
		return NixPlan{}
	}
	if node.Resources != nil && strings.EqualFold(node.Resources.Pressure, "medium") && len(packages) > 2 {
		return NixPlan{}
	}

	plan := NixPlan{
		Enabled:  true,
		Command:  wrapWithNixShell(command, packages),
		Packages: packages,
		Env: []string{
			"AXIS_NIX_WRAPPER=1",
			"AXIS_NIX_PACKAGES=" + strings.Join(packages, ","),
		},
		Notes: []string{
			fmt.Sprintf("nix ephemeral wrapper packages: %s", strings.Join(packages, ", ")),
		},
	}

	if reqs.PrefersTurboQuant && node.TurboQuant != nil && node.TurboQuant.Verified {
		backend := firstTurboBackend(node)
		if backend == "" {
			backend = "native"
		}
		plan.Env = append(plan.Env, "AXIS_NIX_NATIVE_BACKEND="+backend)
		plan.Notes = append(plan.Notes,
			fmt.Sprintf("verified turboquant backend preserved natively (%s); nix wrapper limited to helper tools", backend))
	}
	if node.Resources != nil && node.Resources.MemoryTopology == models.MemoryTopologyUnified && prefersBackend(reqs.PreferredBackends, "mlx") {
		plan.Notes = append(plan.Notes, "unified-memory node kept on native backend; nix wrapper adds only helper tools")
	}

	return plan
}

func wrapWithNixShell(command string, packages []string) string {
	parts := []string{
		"nix",
		"--extra-experimental-features",
		shellescape.Quote("nix-command flakes"),
		"shell",
	}
	for _, pkg := range packages {
		parts = append(parts, shellescape.Quote(pkg))
	}
	parts = append(parts,
		"--command",
		"bash",
		"-lc",
		shellescape.Quote(command),
	)
	return strings.Join(parts, " ")
}

func hasObservedTool(node models.NodeFacts, name string) bool {
	for _, tool := range node.Tools {
		if strings.EqualFold(tool.Name, name) {
			return true
		}
	}
	return false
}

func commandEntrypoint(command string) string {
	for _, field := range strings.Fields(command) {
		if strings.Contains(field, "=") && !strings.HasPrefix(field, "-") {
			continue
		}
		return strings.ToLower(field)
	}
	return ""
}

func firstTurboBackend(node models.NodeFacts) string {
	if node.TurboQuant == nil || len(node.TurboQuant.Backends) == 0 {
		return ""
	}
	return node.TurboQuant.Backends[0]
}

func prefersBackend(backends []string, target string) bool {
	for _, backend := range backends {
		if strings.EqualFold(backend, target) {
			return true
		}
	}
	return false
}
