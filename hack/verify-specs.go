//go:build ignore

// verify-specs checks that every feature spec in docs/superpowers/specs
// records its reviews the way AGENTS.md §8 requires: one block of fixed
// fields per review, and a status on every finding and question.
//
// Run from the module root: go run hack/verify-specs.go
// Design: docs/superpowers/specs/2026-10-31-verify-specs-design.md
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

const specsDir = "docs/superpowers/specs"

// grandfathered specs predate the §8 block format (the first two) or carry
// the maintainer's 2026-10-31 §8 waiver (#585's). Named, not dated, so a
// backdated file name cannot escape the check.
var grandfathered = map[string]bool{
	"2026-10-09-model-discovery-design.md":        true,
	"2026-10-19-axis-start-screen-design.md":      true,
	"2026-10-30-import-direction-check-design.md": true,
}

var requiredSections = []string{"Problem", "Non-goals", "Design", "Alternatives", "Research", "Review"}

var requiredFields = []string{"Kind", "Reviewer", "Model", "Base", "Head", "Date", "Verdict"}

var (
	specName    = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}-[a-z0-9][a-z0-9-]*-design\.md$`)
	fieldLine   = regexp.MustCompile(`^(Kind|Reviewer|Model|Base|Head|Date|Verdict):(.*)$`)
	findingLine = regexp.MustCompile(`^(\d+)\. `)
	rawFinding  = regexp.MustCompile(`^F\d`)
	hexRef      = regexp.MustCompile(`^[0-9a-f]{7,40}$`)
	isoDate     = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)
	severity    = regexp.MustCompile(`(?i)^(blocker|major|minor):\s*`)
	clauseBreak = regexp.MustCompile(`[.;:?] +`)
)

var verdicts = map[string]bool{"approve": true, "approve-with-changes": true, "revise": true}

type problem struct {
	file string
	line int
	rule string
	msg  string
}

type item struct {
	line     int
	question bool
	text     string
}

type block struct {
	line    int
	kind    string
	verdict string
	items   []item
}

type checker struct {
	file     string
	problems []problem
}

func (c *checker) fail(line int, rule, format string, args ...any) {
	c.problems = append(c.problems, problem{c.file, line, rule, fmt.Sprintf(format, args...)})
}

func main() {
	entries, err := os.ReadDir(specsDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "verify-specs: %v\n", err)
		os.Exit(1)
	}
	var problems []problem
	checked, skipped := 0, 0
	for _, e := range entries {
		path := filepath.Join(specsDir, e.Name())
		switch {
		case e.IsDir():
			problems = append(problems, problem{path, 0, "naming", "subdirectory in the specs directory; specs live directly in " + specsDir})
			continue
		case !specName.MatchString(e.Name()):
			problems = append(problems, problem{path, 0, "naming", "file name must be YYYY-MM-DD-<topic>-design.md"})
			continue
		case grandfathered[e.Name()]:
			skipped++
			continue
		}
		data, err := os.ReadFile(path)
		if err != nil {
			problems = append(problems, problem{path, 0, "read", err.Error()})
			continue
		}
		c := &checker{file: path}
		c.check(strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n"))
		problems = append(problems, c.problems...)
		checked++
	}
	if len(problems) > 0 {
		sort.SliceStable(problems, func(i, j int) bool {
			if problems[i].file != problems[j].file {
				return problems[i].file < problems[j].file
			}
			return problems[i].line < problems[j].line
		})
		for _, p := range problems {
			fmt.Fprintf(os.Stderr, "%s:%d: %s: %s\n", p.file, p.line, p.rule, p.msg)
		}
		fmt.Fprintf(os.Stderr, "verify-specs: %d problem(s); see AGENTS.md §8 and %s/2026-10-31-verify-specs-design.md\n", len(problems), specsDir)
		os.Exit(1)
	}
	fmt.Printf("specs passed: %d checked, %d grandfathered\n", checked, skipped)
}

// check runs every rule on one spec. lines[i] is line i+1 of the file.
func (c *checker) check(lines []string) {
	lines, fences := c.blankFences(lines)

	review := -1
	present := map[string]bool{}
	for i, l := range lines {
		if name, ok := strings.CutPrefix(strings.TrimRight(l, " \t"), "## "); ok {
			present[name] = true
			if name == "Review" {
				if review < 0 {
					review = i
				} else {
					c.fail(i+1, "rule 1", "second ## Review section")
				}
			}
		}
	}
	for _, s := range requiredSections {
		if !present[s] {
			c.fail(0, "rule 1", "missing section ## %s", s)
		}
	}
	if review < 0 {
		return
	}
	end := len(lines)
	for i := review + 1; i < len(lines); i++ {
		if strings.HasPrefix(lines[i], "## ") {
			end = i
			break
		}
	}
	for _, f := range fences {
		if f > review && f < end {
			c.fail(f+1, "rule 2", "code fence in ## Review; show example records in another section")
		}
	}
	blocks := c.parseReview(lines, review+1, end)
	c.checkOrder(blocks, review+1)
	for _, b := range blocks {
		for _, it := range b.items {
			c.checkStatus(it)
		}
	}
}

// blankFences returns lines with fenced code blocks blanked, so an example
// record inside a fence is not read as a record, and the index of each
// opening fence line.
func (c *checker) blankFences(lines []string) ([]string, []int) {
	out := make([]string, len(lines))
	var fences []int
	open, openChar, openLen := -1, byte(0), 0
	for i, l := range lines {
		if ch, n, rest := fence(l); n > 0 {
			switch {
			case open < 0:
				open, openChar, openLen = i, ch, n
				fences = append(fences, i)
				continue
			case ch == openChar && n >= openLen && strings.TrimSpace(rest) == "":
				open = -1
				continue
			}
		}
		if open < 0 {
			out[i] = l
		}
	}
	if open >= 0 {
		c.fail(open+1, "fence", "code fence opened here is never closed")
	}
	return out, fences
}

// fence reports a CommonMark code fence line: up to three spaces, then three
// or more backticks or tildes. A backtick fence's info string has no
// backticks (```x``` is an inline span). A fence closes only on the same
// character, at least as many of it, and nothing after.
func fence(l string) (ch byte, n int, rest string) {
	t := strings.TrimLeft(l, " ")
	if len(l)-len(t) > 3 || len(t) < 3 || (t[0] != '`' && t[0] != '~') {
		return 0, 0, ""
	}
	for n < len(t) && t[n] == t[0] {
		n++
	}
	if n < 3 || t[0] == '`' && strings.Contains(t[n:], "`") {
		return 0, 0, ""
	}
	return t[0], n, t[n:]
}

func (c *checker) parseReview(lines []string, start, end int) []block {
	var blocks []block
	i := start
	for ; i < end && !strings.HasPrefix(lines[i], "Kind:"); i++ {
		if strings.TrimSpace(lines[i]) != "" {
			c.fail(i+1, "rule 2", "text before the first Kind: block; every review is a block that starts with Kind:")
		}
	}
	for i < end {
		b, next := c.parseBlock(lines, i, end)
		blocks = append(blocks, b)
		i = next
	}
	return blocks
}

// parseBlock reads the block whose Kind: line is lines[start] and returns
// it with the index of the next Kind: line or end.
func (c *checker) parseBlock(lines []string, start, end int) (block, int) {
	b := block{line: start + 1}
	fields := map[string][]string{}
	sawVerdict := false
	i := start
	for ; i < end && strings.TrimSpace(lines[i]) != ""; i++ {
		l := lines[i]
		if i > start && strings.HasPrefix(l, "Kind:") {
			break
		}
		switch {
		case strings.HasPrefix(l, "- "):
			c.fail(i+1, "rule 2", "a header line starting \"- \"; a question goes after a Questions: line")
		case findingLine.MatchString(l):
			c.fail(i+1, "rule 2", "an item in the block header; leave a blank line after the header")
		case rawFinding.MatchString(l):
			c.fail(i+1, "rule 2", "raw reviewer finding; copy findings into numbered items")
		case sawVerdict:
			c.fail(i+1, "rule 2", "line after Verdict: in the block header; Verdict: is the last header line")
		default:
			if m := fieldLine.FindStringSubmatch(l); m != nil {
				fields[m[1]] = append(fields[m[1]], strings.TrimSpace(m[2]))
				sawVerdict = m[1] == "Verdict"
			}
		}
	}
	for _, f := range requiredFields {
		switch v := fields[f]; {
		case len(v) == 0:
			c.fail(b.line, "rule 2", "block has no %s: line", f)
		case len(v) > 1:
			c.fail(b.line, "rule 2", "block has %d %s: lines", len(v), f)
		case v[0] == "":
			c.fail(b.line, "rule 2", "%s: is empty", f)
		}
	}
	first := func(f string) string {
		if v := fields[f]; len(v) == 1 && v[0] != "" {
			return strings.Fields(v[0])[0]
		}
		return ""
	}
	if v := fields["Kind"]; len(v) == 1 && v[0] != "" {
		b.kind = v[0]
		if b.kind != "spec" && b.kind != "diff" {
			c.fail(b.line, "rule 2", "Kind: %q, want spec or diff", b.kind)
		}
	}
	for _, f := range []string{"Base", "Head"} {
		if v := first(f); v != "" && !hexRef.MatchString(v) {
			c.fail(b.line, "rule 2", "%s: %q is not a 7 to 40 character hex commit", f, v)
		}
	}
	if v := fields["Date"]; len(v) == 1 && v[0] != "" {
		if _, err := time.Parse("2006-01-02", v[0]); err != nil || !isoDate.MatchString(v[0]) {
			c.fail(b.line, "rule 2", "Date: %q, want a real YYYY-MM-DD date", v[0])
		}
	}
	if v := first("Verdict"); v != "" {
		b.verdict = v
		if !verdicts[v] {
			c.fail(b.line, "rule 2", "Verdict: %q, want approve, approve-with-changes or revise", v)
		}
	}

	var cur *item
	inQuestions, paragraphStart, next := false, true, 1
	underQuestions, noted := false, false
	for ; i < end && !strings.HasPrefix(lines[i], "Kind:"); i++ {
		l := lines[i]
		if strings.TrimSpace(l) == "" {
			cur, paragraphStart = nil, true
			continue
		}
		indented := l[0] == ' ' || l[0] == '\t'
		if noted && !indented && !strings.HasPrefix(l, "Note:") {
			c.fail(i+1, "rule 2", "line after the closing Note:; notes end a block")
		}
		wasUnderQuestions := underQuestions
		underQuestions = false
		switch {
		case indented && paragraphStart:
			c.fail(i+1, "rule 2", "paragraph does not start with an item, Questions: or Note:")
			cur = nil
		case indented && wasUnderQuestions:
			c.fail(i+1, "rule 2", "indented line under Questions:; a question starts with \"- \"")
		case indented:
			if cur != nil {
				cur.text += " " + strings.TrimSpace(l)
			}
		case findingLine.MatchString(l):
			n, _ := strconv.Atoi(findingLine.FindStringSubmatch(l)[1])
			if n != next {
				c.fail(i+1, "rule 2", "finding %d where %d was expected; a new review needs its own Kind: block", n, next)
			}
			next = n + 1
			b.items = append(b.items, item{line: i + 1, text: findingLine.ReplaceAllString(l, "")})
			cur = &b.items[len(b.items)-1]
		case strings.HasPrefix(l, "- "):
			if !inQuestions {
				c.fail(i+1, "rule 2", "a \"- \" line with no Questions: line before it in the block")
			}
			b.items = append(b.items, item{line: i + 1, question: true, text: strings.TrimPrefix(l, "- ")})
			cur = &b.items[len(b.items)-1]
		case strings.HasPrefix(strings.ToLower(l), "questions:"):
			inQuestions, cur, underQuestions = true, nil, true
		case strings.HasPrefix(l, "Note:"):
			cur, noted = nil, true
		case rawFinding.MatchString(l):
			c.fail(i+1, "rule 2", "raw reviewer finding; copy findings into numbered items")
			cur = nil
		default:
			c.fail(i+1, "rule 2", "line is not an item, Questions: or Note:; a review needs its own Kind: block")
			cur = nil
		}
		paragraphStart = false
	}
	return b, i
}

func (c *checker) checkOrder(blocks []block, line int) {
	last := map[string]block{}
	for _, b := range blocks {
		last[b.kind] = b
	}
	spec, ok := last["spec"]
	if !ok {
		c.fail(line, "rule 3", "no Kind: spec block; a feature spec needs an approving spec review")
	} else if spec.verdict == "revise" {
		c.fail(spec.line, "rule 3", "the last spec review is revise; record the re-review")
	}
	if diff, ok := last["diff"]; ok && diff.verdict == "revise" {
		c.fail(diff.line, "rule 3", "the last diff review is revise; record the re-review")
	}
}

// checkStatus requires a status marker at the start of a clause after the
// description, so "Minor: fixed timeouts are too short." has none.
func (c *checker) checkStatus(it item) {
	text := severity.ReplaceAllString(strings.TrimSpace(it.text), "")
	words, colon := []string{"fixed"}, []string{"declined:"}
	want := "fixed or declined:"
	if it.question {
		words, colon = []string{"became"}, []string{"open:", "verified:"}
		want = "became, open: or verified:"
	}
	for _, loc := range clauseBreak.FindAllStringIndex(text, -1) {
		rest := strings.ToLower(text[loc[1]:])
		for _, w := range words {
			if strings.HasPrefix(rest, w) && (len(rest) == len(w) || !isLetter(rest[len(w)])) {
				return
			}
		}
		for _, m := range colon {
			if strings.HasPrefix(rest, m) {
				return
			}
		}
	}
	kind := "finding"
	if it.question {
		kind = "question"
	}
	c.fail(it.line, "rule 4", "%s has no status (%s after its description)", kind, want)
}

func isLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }
