//go:build ignore

package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Lifecycle taxonomy from docs/lifecycle.md
const (
	Stable       = "stable"
	Experimental = "experimental"
	Scaffolded   = "scaffolded"
	Dormant      = "dormant"
	Deprecated   = "deprecated"
	InternalOnly = "internal-only"
)

// packageStates maps internal/... paths to their lifecycle state.
var packageStates = map[string]string{
	"internal/agent":        Experimental,
	"internal/api":          Experimental,
	"internal/auth":         InternalOnly,
	"internal/buildinfo":    InternalOnly,
	"internal/chat":         Experimental,
	"internal/config":       Stable,
	"internal/cortex":       Experimental,
	"internal/daemon":       Stable,
	"internal/discovery":    Stable,
	"internal/execution":    Experimental,
	"internal/events":       Stable,
	"internal/facts":        Stable,
	"internal/failures":     Stable,
	"internal/git":          Stable,
	"internal/knowledge":    Stable,
	"internal/llmrouter":    Experimental,
	"internal/mcp":          Experimental,
	"internal/mesh":         Scaffolded,
	"internal/multipath":    Scaffolded,
	"internal/models":       InternalOnly,
	"internal/persist":      InternalOnly,
	"internal/placement":    Stable,
	"internal/repairs":      Scaffolded,
	"internal/reservation":  Scaffolded,
	"internal/runtimectx":   Stable,
	"internal/safety":       Experimental,
	"internal/scripts":      Stable,
	"internal/secrets":      Experimental,
	"internal/skills":       Experimental,
	"internal/snapshot":     Stable,
	"internal/snapshotview": InternalOnly,
	"internal/state":        Stable,
	"internal/transport":    Stable,
	"internal/turboexec":    InternalOnly,
	"internal/ui":           InternalOnly,
	"internal/versioncmp":   InternalOnly,
	"internal/workload":     Stable,
}

// dormantAllowList maps importer → set of allowed dormant imports.
// Empty because no dormant packages exist yet.
var dormantAllowList = map[string]map[string]bool{}

// stableExperimentalAllowList maps stable package → set of allowed experimental transitive imports.
// These are grandfathered violations pending refactoring to respect the inheritance rules.
var stableExperimentalAllowList = map[string]map[string]bool{
	"internal/daemon": {
		"internal/execution": true,
		"internal/safety":    true,
		"internal/skills":    true,
		"internal/cortex":    true,
	},
	"internal/runtimectx": {
		"internal/skills": true,
		"internal/cortex": true,
	},
	"internal/knowledge": {
		"internal/cortex": true,
	},
	"internal/events": {
		"internal/cortex": true,
	},
}

func main() {
	var violations []string
	var warnings []string

	// 1. Stable packages must not transitively import experimental or dormant.
	for pkg, state := range packageStates {
		if state != Stable {
			continue
		}
		deps, err := listDeps(pkg)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing deps for %s: %v\n", pkg, err)
			os.Exit(1)
		}
		for _, dep := range deps {
			if !strings.HasPrefix(dep, "github.com/toasterbook88/axis/internal/") {
				continue
			}
			depShort := strings.TrimPrefix(dep, "github.com/toasterbook88/axis/")
			depState, ok := packageStates[depShort]
			if !ok {
				violations = append(violations, fmt.Sprintf("stable package %s imports unknown internal package %s (not classified)", pkg, depShort))
				continue
			}
			if depState == Experimental || depState == Dormant {
				if allowed, ok := stableExperimentalAllowList[pkg]; ok && allowed[depShort] {
					warnings = append(warnings, fmt.Sprintf("stable package %s imports %s package %s (allow-listed)", pkg, depState, depShort))
					continue
				}
				violations = append(violations, fmt.Sprintf("stable package %s imports %s package %s", pkg, depState, depShort))
			}
		}
	}

	// 2. Experimental packages must have doc.go mentioning EXPERIMENTAL.
	for pkg, state := range packageStates {
		if state != Experimental {
			continue
		}
		docPath := filepath.Join(pkg, "doc.go")
		if _, err := os.Stat(docPath); err != nil {
			violations = append(violations, fmt.Sprintf("experimental package %s missing doc.go", pkg))
			continue
		}
		data, err := os.ReadFile(docPath)
		if err != nil {
			violations = append(violations, fmt.Sprintf("experimental package %s unreadable doc.go: %v", pkg, err))
			continue
		}
		if !strings.Contains(string(data), "EXPERIMENTAL") {
			violations = append(violations, fmt.Sprintf("experimental package %s doc.go missing EXPERIMENTAL", pkg))
		}
	}

	// 3. Dormant imports must be allow-listed.
	dormantPkgs := []string{}
	for pkg, state := range packageStates {
		if state == Dormant {
			dormantPkgs = append(dormantPkgs, pkg)
		}
	}
	if len(dormantPkgs) > 0 {
		allPkgs, err := listAllPackages()
		if err != nil {
			fmt.Fprintf(os.Stderr, "error listing packages: %v\n", err)
			os.Exit(1)
		}
		for _, importer := range allPkgs {
			if !strings.HasPrefix(importer, "github.com/toasterbook88/axis/internal/") {
				continue
			}
			imports, err := listDirectImports(importer)
			if err != nil {
				fmt.Fprintf(os.Stderr, "error listing imports for %s: %v\n", importer, err)
				os.Exit(1)
			}
			for _, imp := range imports {
				for _, dormant := range dormantPkgs {
					if imp == "github.com/toasterbook88/axis/"+dormant {
						importerShort := strings.TrimPrefix(importer, "github.com/toasterbook88/axis/")
						allowed := false
						if set, ok := dormantAllowList[importerShort]; ok {
							allowed = set[dormant]
						}
						if !allowed {
							violations = append(violations, fmt.Sprintf("dormant package %s imported by %s (not allow-listed)", dormant, importerShort))
						}
					}
				}
			}
		}
	}

	for _, w := range warnings {
		fmt.Fprintf(os.Stderr, "  warning: %s\n", w)
	}

	if len(violations) > 0 {
		fmt.Fprintln(os.Stderr, "lifecycle check failures:")
		for _, v := range violations {
			fmt.Fprintf(os.Stderr, "  - %s\n", v)
		}
		os.Exit(1)
	}

	fmt.Println("lifecycle checks passed")
}

func listDeps(pkg string) ([]string, error) {
	cmd := exec.Command("go", "list", "-deps", "-f", "{{.ImportPath}}", "./"+pkg)
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s: %s", err, exitErr.Stderr)
		}
		return nil, err
	}
	var deps []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			deps = append(deps, line)
		}
	}
	return deps, nil
}

func listAllPackages() ([]string, error) {
	cmd := exec.Command("go", "list", "./...")
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s: %s", err, exitErr.Stderr)
		}
		return nil, err
	}
	var pkgs []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			pkgs = append(pkgs, line)
		}
	}
	return pkgs, nil
}

func listDirectImports(pkg string) ([]string, error) {
	cmd := exec.Command("go", "list", "-f", "{{range .Imports}}{{.}}{{'\\n'}}{{end}}", "./"+strings.TrimPrefix(pkg, "github.com/toasterbook88/axis/"))
	out, err := cmd.Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, fmt.Errorf("%s: %s", err, exitErr.Stderr)
		}
		return nil, err
	}
	var imports []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			imports = append(imports, line)
		}
	}
	return imports, nil
}
