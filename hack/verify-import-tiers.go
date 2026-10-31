//go:build ignore

// verify-import-tiers checks that every internal/ package imports only
// packages in its own tier or a lower one, using the tier layout in AGENTS.md
// §4 as the single source of truth. Exceptions live in
// hack/import-tiers-allowlist.txt, each with a rationale.
//
// Run from the module root: go run hack/verify-import-tiers.go
// Design: docs/superpowers/specs/2026-10-30-import-direction-check-design.md
package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strings"
)

const (
	agentsFile    = "AGENTS.md"
	allowlistFile = "hack/import-tiers-allowlist.txt"
)

// releaseTargets are the platforms .goreleaser.yml ships; the check uses the
// union of their import edges so a platform-specific file cannot hide an
// upward import.
var releaseTargets = []struct{ goos, goarch string }{
	{"linux", "amd64"}, {"linux", "arm64"}, {"darwin", "amd64"}, {"darwin", "arm64"},
}

type tier struct {
	level int // 0 is Support; 1-5 are Layer N
	name  string
}

func (t tier) String() string {
	if t.level == 0 {
		return "Support"
	}
	return fmt.Sprintf("Layer %d (%s)", t.level, t.name)
}

type edge struct{ src, dst string }

func (e edge) String() string { return e.src + " -> " + e.dst }

var (
	layerHeader  = regexp.MustCompile(`^Layer ([1-5])\s+(.*)$`)
	supportLabel = regexp.MustCompile(`^Support(\s+.*)?$`)
	pkgToken     = regexp.MustCompile(`^internal/([a-z0-9_]+)$`)
	allowEntry   = regexp.MustCompile(`^([a-z0-9_]+) -> ([a-z0-9_]+)(\s+#\s*(.*))?$`)
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "import tiers FAILED:\n%v\n", err)
		os.Exit(1)
	}
}

func run() error {
	agents, err := os.ReadFile(agentsFile)
	if err != nil {
		return err
	}
	tiers, err := parseTiers(string(agents))
	if err != nil {
		return fmt.Errorf("%s §4 tier layout: %w", agentsFile, err)
	}

	var problems []string
	problems = append(problems, inventoryProblems(tiers)...)

	mod, err := goOutput(nil, "list", "-m")
	if err != nil {
		return err
	}
	mod = strings.TrimSpace(mod)

	edges := map[edge]bool{}
	for _, t := range releaseTargets {
		pkgEdges, loadProblems, err := importEdges(mod, t.goos, t.goarch)
		if err != nil {
			return err
		}
		problems = append(problems, loadProblems...)
		for e := range pkgEdges {
			edges[e] = true
		}
	}

	upward := map[edge]bool{}
	untiered := map[string]string{} // package -> one importer, for the message
	for _, e := range sortedEdges(edges) {
		ts, okS := tiers[e.src]
		td, okD := tiers[e.dst]
		switch {
		case !okD:
			// A hidden top-level directory (testdata, _x, .x) has no tier, so an
			// import through it would otherwise skip the check entirely.
			if _, seen := untiered[e.dst]; !seen {
				untiered[e.dst] = e.src
			}
		case !okS:
			if _, seen := untiered[e.src]; !seen {
				untiered[e.src] = ""
			}
		case td.level > ts.level:
			upward[e] = true
		}
	}
	for pkg, importer := range untiered {
		if importer == "" {
			problems = append(problems, fmt.Sprintf("internal/%s imports other internal/ packages but is not in the %s tier layout", pkg, agentsFile))
			continue
		}
		problems = append(problems, fmt.Sprintf("internal/%s is imported by internal/%s but is not in the %s tier layout (add it to §4 or move it under a tiered package)", pkg, importer, agentsFile))
	}

	allowed, allowProblems := readAllowlist(tiers, upward)
	problems = append(problems, allowProblems...)

	for _, e := range sortedEdges(upward) {
		if allowed[e] {
			continue
		}
		problems = append(problems, fmt.Sprintf(
			"internal/%s -> internal/%s: %s may not import %s. Invert the dependency, move internal/%s or internal/%s to the right tier in %s §4, or add \"%s  # <why>\" to %s.",
			e.src, e.dst, tiers[e.src], tiers[e.dst], e.src, e.dst, agentsFile, e, allowlistFile))
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return errors.New(strings.Join(problems, "\n"))
	}
	fmt.Printf("import tiers passed: %d packages, %d internal import edges, %d allowlisted exception(s)\n",
		len(tiers), len(edges), len(allowed))
	return nil
}

// parseTiers reads the first fenced text block in §4, from the "## 4."
// heading to the next "## " heading.
func parseTiers(doc string) (map[string]tier, error) {
	_, after, ok := strings.Cut(doc, "\n## 4.")
	if !ok {
		return nil, errors.New(`no "## 4." heading`)
	}
	section, _, _ := strings.Cut(after, "\n## ")
	_, block, ok := strings.Cut(section, "```text\n")
	if !ok {
		return nil, errors.New("no ```text block after the §4 heading")
	}
	block, _, ok = strings.Cut(block, "```")
	if !ok {
		return nil, errors.New("unterminated ```text block")
	}

	tiers := map[string]tier{}
	var cur *tier
	for i, line := range strings.Split(block, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var rest string
		switch {
		case line[0] == ' ' || line[0] == '\t':
			if cur == nil {
				return nil, fmt.Errorf("line %d: continuation before any tier header: %q", i+1, line)
			}
			rest = line
		case layerHeader.MatchString(line):
			m := layerHeader.FindStringSubmatch(line)
			name, pkgs := m[2], ""
			if at := strings.Index(m[2], "internal/"); at >= 0 {
				name, pkgs = m[2][:at], m[2][at:]
			}
			name = strings.TrimSpace(name)
			if name == "" {
				return nil, fmt.Errorf("line %d: tier header without a name: %q", i+1, line)
			}
			cur = &tier{level: int(m[1][0] - '0'), name: name}
			rest = pkgs
		case supportLabel.MatchString(line):
			cur = &tier{level: 0, name: "Support"}
			rest = strings.TrimPrefix(line, "Support")
		default:
			return nil, fmt.Errorf("line %d: not a tier header or continuation: %q", i+1, line)
		}
		for _, tok := range strings.Fields(rest) {
			m := pkgToken.FindStringSubmatch(tok)
			if m == nil {
				return nil, fmt.Errorf("line %d: %q is not an internal/<package> entry", i+1, tok)
			}
			if prev, dup := tiers[m[1]]; dup {
				return nil, fmt.Errorf("internal/%s is listed twice (%s and %s)", m[1], prev, *cur)
			}
			tiers[m[1]] = *cur
		}
	}
	if len(tiers) == 0 {
		return nil, errors.New("the tier block lists no packages")
	}
	return tiers, nil
}

// inventoryProblems compares the tier block with the directories under internal/.
func inventoryProblems(tiers map[string]tier) []string {
	var problems []string
	entries, err := os.ReadDir("internal")
	if err != nil {
		return []string{fmt.Sprintf("read internal/: %v", err)}
	}
	present := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") || strings.HasPrefix(e.Name(), "_") || e.Name() == "testdata" {
			continue
		}
		present[e.Name()] = true
		if _, ok := tiers[e.Name()]; !ok {
			problems = append(problems, fmt.Sprintf("internal/%s is not in the %s tier layout (add it to §4 or delete the package)", e.Name(), agentsFile))
		}
	}
	for name := range tiers {
		if !present[name] {
			problems = append(problems, fmt.Sprintf("%s §4 lists internal/%s, which does not exist (remove it from the layout)", agentsFile, name))
		}
	}
	return problems
}

type listedPackage struct {
	ImportPath string
	Imports    []string
	Error      *struct{ Err string }
	DepsErrors []struct{ Err string }
}

// importEdges returns the cross-package production import edges between
// top-level internal/ packages for one platform. -deps is required: the
// ./internal/... pattern skips testdata and _- or .-prefixed directories, but
// production code can still import packages there, and their edges count.
func importEdges(mod, goos, goarch string) (map[edge]bool, []string, error) {
	platform := goos + "/" + goarch
	out, err := goOutput([]string{"GOOS=" + goos, "GOARCH=" + goarch, "CGO_ENABLED=0"}, "list", "-e", "-deps", "-json", "./internal/...")
	if err != nil {
		return nil, nil, err
	}
	prefix := mod + "/internal/"
	edges := map[edge]bool{}
	var problems []string
	listed := 0
	dec := json.NewDecoder(strings.NewReader(out))
	for {
		var p listedPackage
		if err := dec.Decode(&p); err == io.EOF {
			break
		} else if err != nil {
			return nil, nil, fmt.Errorf("decode go list output (%s): %w", platform, err)
		}
		src := topLevel(p.ImportPath, prefix)
		if src == "" {
			continue // standard library or another module, pulled in by -deps
		}
		listed++
		if p.Error != nil {
			problems = append(problems, fmt.Sprintf("%s: %s failed to load: %s", platform, p.ImportPath, p.Error.Err))
		}
		for _, de := range p.DepsErrors {
			problems = append(problems, fmt.Sprintf("%s: %s has a dependency error: %s", platform, p.ImportPath, de.Err))
		}
		for _, imp := range p.Imports {
			if dst := topLevel(imp, prefix); dst != "" && dst != src {
				edges[edge{src, dst}] = true
			}
		}
	}
	if listed == 0 {
		return nil, nil, fmt.Errorf("%s: go list found no packages under %s; run from the module root", platform, prefix)
	}
	return edges, problems, nil
}

func topLevel(importPath, prefix string) string {
	rest, ok := strings.CutPrefix(importPath, prefix)
	if !ok || rest == "" {
		return ""
	}
	top, _, _ := strings.Cut(rest, "/")
	return top
}

// readAllowlist parses the allowlist and reports malformed, unknown,
// duplicate and stale entries.
func readAllowlist(tiers map[string]tier, upward map[edge]bool) (map[edge]bool, []string) {
	allowed := map[edge]bool{}
	f, err := os.Open(allowlistFile)
	if errors.Is(err, os.ErrNotExist) {
		return allowed, nil
	}
	if err != nil {
		return allowed, []string{fmt.Sprintf("open %s: %v", allowlistFile, err)}
	}
	defer f.Close()

	var problems []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		where := fmt.Sprintf("%s:%d", allowlistFile, n)
		m := allowEntry.FindStringSubmatch(line)
		if m == nil {
			problems = append(problems, fmt.Sprintf(`%s: expected "<pkg> -> <pkg>  # <rationale>", got %q`, where, line))
			continue
		}
		e := edge{m[1], m[2]}
		if strings.TrimSpace(m[4]) == "" {
			problems = append(problems, fmt.Sprintf("%s: %s has no rationale (add \"# <why>\")", where, e))
		}
		for _, name := range []string{e.src, e.dst} {
			if _, ok := tiers[name]; !ok {
				problems = append(problems, fmt.Sprintf("%s: internal/%s is not in the %s tier layout", where, name, agentsFile))
			}
		}
		if allowed[e] {
			problems = append(problems, fmt.Sprintf("%s: %s is listed twice", where, e))
		}
		allowed[e] = true
		if !upward[e] {
			problems = append(problems, fmt.Sprintf("%s: %s is no longer an upward import; remove the stale entry", where, e))
		}
	}
	if err := sc.Err(); err != nil {
		problems = append(problems, fmt.Sprintf("read %s: %v", allowlistFile, err))
	}
	return allowed, problems
}

func sortedEdges(set map[edge]bool) []edge {
	out := make([]edge, 0, len(set))
	for e := range set {
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out
}

// goOutput runs the go command with extra environment and returns stdout.
// A nonzero exit is an error that carries stderr. GOWORK is off because in a
// multi-module workspace `go list -m` prints every module, and the joined
// output would match no import path, silently checking nothing.
func goOutput(env []string, args ...string) (string, error) {
	cmd := exec.Command("go", args...)
	cmd.Env = append(append(os.Environ(), "GOWORK=off"), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("go %s (%s): %v\n%s", strings.Join(args, " "), strings.Join(env, " "), err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}
