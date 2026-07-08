package agent

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/git"
)

// --- Checkpoint / undo infrastructure ---
//
// The checkpointer snapshots file content before each mutating tool call so
// the agent can undo its most recent edit and review accumulated changes,
// without relying on git being initialized.

type fileCheckpoint struct {
	path      string
	content   string // prior content; empty if the file did not exist
	existed   bool
	timestamp time.Time
}

type checkpointer struct {
	mu    sync.Mutex
	stack []fileCheckpoint
}

func newCheckpointer() *checkpointer { return &checkpointer{} }

// snapshot records the current content of path (if it exists) before a
// mutation so it can be restored by undo_last.
func (c *checkpointer) snapshot(path string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if data, err := os.ReadFile(path); err == nil {
		c.stack = append(c.stack, fileCheckpoint{
			path: path, content: string(data), existed: true, timestamp: time.Now(),
		})
		return
	}
	// File did not exist — record an empty checkpoint so undo can delete it.
	c.stack = append(c.stack, fileCheckpoint{path: path, existed: false, timestamp: time.Now()})
}

func (c *checkpointer) undoLast() (fileCheckpoint, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.stack) == 0 {
		return fileCheckpoint{}, false
	}
	cp := c.stack[len(c.stack)-1]
	c.stack = c.stack[:len(c.stack)-1]
	return cp, true
}

func (c *checkpointer) snapshotCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.stack)
}

// --- Tool: undo_last ---

func (r *ToolRegistry) registerUndoLast() {
	r.add("undo_last",
		"Undo the most recent file mutation made by write_file/edit_file/multi_edit in this session. Restores the file to its prior content (or deletes it if it was newly created). One level of undo per call.",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			cp, ok := r.checkpoints.undoLast()
			if !ok {
				return "Nothing to undo (no checkpoints recorded).", nil
			}
			if !cp.existed {
				if err := os.Remove(cp.path); err != nil && !os.IsNotExist(err) {
					return "", fmt.Errorf("undo: cannot remove %q: %w", cp.path, err)
				}
				return fmt.Sprintf("Undo: removed newly-created file %s", cp.path), nil
			}
			if err := os.WriteFile(cp.path, []byte(cp.content), 0644); err != nil {
				return "", fmt.Errorf("undo: cannot restore %q: %w", cp.path, err)
			}
			return fmt.Sprintf("Undo: restored %s to its content before the last edit", cp.path), nil
		},
	)
}

// --- Tool: review_changes ---

func (r *ToolRegistry) registerReviewChanges() {
	r.add("review_changes",
		"Show a summary of uncommitted changes the agent has made. If the working directory is a git repository, this reports `git status` + a stat summary of the diff vs HEAD. Otherwise it lists the files touched in this session (from checkpoints).",
		json.RawMessage(`{"type":"object","properties":{}}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			state, err := git.GetRepoState(".")
			if err == nil && state.IsRepo {
				var b strings.Builder
				b.WriteString(fmt.Sprintf("Branch: %s\nCommit: %s\n", state.Branch, state.Commit))
				if state.IsDirty {
					b.WriteString(fmt.Sprintf("Status: Dirty (%d files changed)\n\n", state.DirtyCount))
				} else {
					b.WriteString("Status: Clean — no uncommitted changes.\n")
					return b.String(), nil
				}
				if out, err := runGitQuiet("diff", "--stat", "HEAD"); err == nil && strings.TrimSpace(out) != "" {
					b.WriteString("Diff stat vs HEAD:\n")
					b.WriteString(out)
				}
				if out, err := runGitQuiet("status", "--short"); err == nil {
					b.WriteString("\nChanged files:\n")
					b.WriteString(out)
				}
				return b.String(), nil
			}
			// Non-repo fallback: report from the checkpoint stack.
			n := r.checkpoints.snapshotCount()
			if n == 0 {
				return "Not a git repository and no file checkpoints recorded this session.", nil
			}
			r.checkpoints.mu.Lock()
			defer r.checkpoints.mu.Unlock()
			var b strings.Builder
			fmt.Fprintf(&b, "Session edits (%d file mutations, no git repo):\n", n)
			seen := map[string]int{}
			for _, cp := range r.checkpoints.stack {
				seen[cp.path]++
			}
			for p, c := range seen {
				fmt.Fprintf(&b, "  %s (edited %d time(s))\n", p, c)
			}
			return b.String(), nil
		},
	)
}

// runGitQuiet runs a git command in the current directory and returns its
// combined stdout/stderr trimmed. Used only for read-only inspection.
func runGitQuiet(args ...string) (string, error) {
	return runGitInDir(".", args...)
}

func runGitInDir(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	return strings.TrimSpace(string(out)), err
}

// --- Tool: web_fetch ---

type webFetchArgs struct {
	URL      string `json:"url"`
	MaxChars int    `json:"max_chars,omitempty"`
}

const defaultWebFetchMaxChars = 20000

func (r *ToolRegistry) registerWebFetch() {
	r.add("web_fetch",
		"Fetch a URL and return its content as readable plain text. HTML tags, scripts, and styles are stripped; whitespace is collapsed. Useful for reading documentation, GitHub issues/PRs, articles, and API endpoints. Respects a max_chars limit (default 20000).",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"url":{"type":"string","description":"The http(s) URL to fetch"},
				"max_chars":{"type":"integer","description":"Maximum characters to return (default 20000)","default":20000}
			},
			"required":["url"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a webFetchArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for web_fetch: %w", err)
			}
			if a.URL == "" {
				return "", fmt.Errorf("web_fetch requires a non-empty \"url\" argument")
			}
			parsed, err := url.Parse(a.URL)
			if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
				return "", fmt.Errorf("web_fetch requires an http or https URL")
			}
			maxChars := a.MaxChars
			if maxChars <= 0 {
				maxChars = defaultWebFetchMaxChars
			}
			req, err := http.NewRequestWithContext(ctx, "GET", a.URL, nil)
			if err != nil {
				return "", fmt.Errorf("web_fetch: bad request: %w", err)
			}
			req.Header.Set("User-Agent", "axis-agent/1.0 (web_fetch tool)")
			req.Header.Set("Accept", "text/html, application/json, text/plain, */*")
			client := &http.Client{Timeout: 20 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return "", fmt.Errorf("web_fetch: request failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return "", fmt.Errorf("web_fetch: HTTP %d for %s", resp.StatusCode, a.URL)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, int64(maxChars)+1024))
			if err != nil {
				return "", fmt.Errorf("web_fetch: read failed: %w", err)
			}
			ctype := resp.Header.Get("Content-Type")
			text := string(body)
			if strings.Contains(ctype, "text/html") || (strings.Contains(ctype, "text/") && looksLikeHTML(text)) {
				text = htmlToText(text)
			}
			text = collapseWhitespace(text)
			if len(text) > maxChars {
				text = text[:maxChars] + "\n... [truncated]"
			}
			return text, nil
		},
	)
}

// looksLikeHTML detects HTML content even when the content-type is generic.
func looksLikeHTML(s string) bool {
	return strings.Contains(strings.ToLower(s[:min(len(s), 512)]), "<html") ||
		strings.Contains(strings.ToLower(s[:min(len(s), 512)]), "<body") ||
		strings.Contains(strings.ToLower(s[:min(len(s), 512)]), "<div")
}

// htmlToText strips HTML markup to readable text: removes script/style blocks,
// converts block tags to newlines, drops all other tags, and decodes common
// entities. Intentionally simple — not a full HTML parser, good enough for
// documentation/articles.
var scriptStyleRe = regexp.MustCompile(`(?is)<(?:script|style|noscript)\b[^>]*>.*?</(?:script|style|noscript)>`)
var headRe = regexp.MustCompile(`(?is)<head\b[^>]*>.*?</head>`)

func htmlToText(html string) string {
	// Drop script/style blocks entirely.
	html = scriptStyleRe.ReplaceAllString(html, " ")
	// Drop the <head> block (title, meta, link) — not reader content.
	html = headRe.ReplaceAllString(html, " ")
	blockRe := regexp.MustCompile(`(?i)</(p|div|li|tr|h[1-6]|br|section|article|header|footer|ul|ol|table)[^>]*>`)
	html = blockRe.ReplaceAllString(html, "\n")
	// Also treat <br> and <br/> opening tags as newlines.
	brRe := regexp.MustCompile(`(?i)<br\s*/?>`)
	html = brRe.ReplaceAllString(html, "\n")
	// Strip remaining tags.
	tagRe := regexp.MustCompile(`<[^>]+>`)
	html = tagRe.ReplaceAllString(html, " ")
	// Decode common entities.
	html = strings.ReplaceAll(html, "&nbsp;", " ")
	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", `"`)
	html = strings.ReplaceAll(html, "&#39;", "'")
	return html
}

func collapseWhitespace(s string) string {
	// Collapse runs of spaces/tabs, trim trailing spaces per line, collapse 3+ newlines to 2.
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(strings.Join(strings.Fields(l), " "), " ")
	}
	out := strings.Join(lines, "\n")
	blankRe := regexp.MustCompile(`\n{3,}`)
	out = blankRe.ReplaceAllString(out, "\n\n")
	return strings.TrimSpace(out)
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- Tool: web_search ---

type webSearchArgs struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

func (r *ToolRegistry) registerWebSearch() {
	r.add("web_search",
		"Search the web via DuckDuckGo and return the top results (title + URL + snippet). No API key required. Best-effort HTML scraping; returns what it can parse.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"The search query"},
				"max_results":{"type":"integer","description":"Maximum results to return (default 8)","default":8}
			},
			"required":["query"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a webSearchArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for web_search: %w", err)
			}
			if a.Query == "" {
				return "", fmt.Errorf("web_search requires a non-empty \"query\" argument")
			}
			max := a.MaxResults
			if max <= 0 {
				max = 8
			}
			searchURL := "https://lite.duckduckgo.com/lite/?q=" + url.QueryEscape(a.Query)
			req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
			if err != nil {
				return "", fmt.Errorf("web_search: bad request: %w", err)
			}
			req.Header.Set("User-Agent", "axis-agent/1.0 (web_search tool)")
			client := &http.Client{Timeout: 20 * time.Second}
			resp, err := client.Do(req)
			if err != nil {
				return "", fmt.Errorf("web_search: request failed: %w", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 400 {
				return "", fmt.Errorf("web_search: HTTP %d", resp.StatusCode)
			}
			body, err := io.ReadAll(io.LimitReader(resp.Body, 200*1024))
			if err != nil {
				return "", fmt.Errorf("web_search: read failed: %w", err)
			}
			results := parseDuckDuckGoLite(string(body), max)
			if len(results) == 0 {
				return "No results parsed. The search endpoint format may have changed; try web_fetch on a specific URL instead.", nil
			}
			var b strings.Builder
			for i, r := range results {
				fmt.Fprintf(&b, "%d. %s\n   %s\n", i+1, r.title, r.url)
				if r.snippet != "" {
					fmt.Fprintf(&b, "   %s\n", r.snippet)
				}
				b.WriteString("\n")
			}
			return strings.TrimSpace(b.String()), nil
		},
	)
}

type ddgResult struct {
	title, url, snippet string
}

// parseDuckDuckGoLite extracts result links and snippets from the DuckDuckGo
// Lite HTML results page. It is intentionally tolerant: it finds anchor tags
// pointing to external sites and the surrounding text cells.
var ddgLinkRe = regexp.MustCompile(`(?i)<a[^>]+class="result-link"[^>]*href="([^"]+)"[^>]*>(.*?)</a>`)
var ddgSnippetRe = regexp.MustCompile(`(?is)<td[^>]*class="result-snippet"[^>]*>(.*?)</td>`)
var ddgAnyLinkRe = regexp.MustCompile(`(?i)<a[^>]+href="(https?://[^"]+)"[^>]*>(.*?)</a>`)

func parseDuckDuckGoLite(html string, max int) []ddgResult {
	var results []ddgResult
	// Prefer the structured result-link/snippet classes first.
	linkMatches := ddgLinkRe.FindAllStringSubmatch(html, -1)
	snippetMatches := ddgSnippetRe.FindAllStringSubmatch(html, -1)
	if len(linkMatches) > 0 {
		for i, m := range linkMatches {
			if i >= max {
				break
			}
			r := ddgResult{url: htmlEntityDecode(m[1]), title: collapseWhitespace(htmlToText(m[2]))}
			if i < len(snippetMatches) {
				r.snippet = collapseWhitespace(htmlToText(snippetMatches[i][1]))
			}
			results = append(results, r)
		}
		return results
	}
	// Fallback: any external anchor (skip duckduckgo.com internals).
	for _, m := range ddgAnyLinkRe.FindAllStringSubmatch(html, -1) {
		u := htmlEntityDecode(m[1])
		if strings.Contains(u, "duckduckgo.com") || strings.Contains(u, "duck.com") {
			continue
		}
		title := collapseWhitespace(htmlToText(m[2]))
		if title == "" || title == u {
			continue
		}
		results = append(results, ddgResult{url: u, title: title})
		if len(results) >= max {
			break
		}
	}
	return results
}

func htmlEntityDecode(s string) string {
	s = strings.ReplaceAll(s, "&amp;", "&")
	s = strings.ReplaceAll(s, "&lt;", "<")
	s = strings.ReplaceAll(s, "&gt;", ">")
	s = strings.ReplaceAll(s, "&quot;", `"`)
	s = strings.ReplaceAll(s, "&#39;", "'")
	return s
}

// --- Tool: symbol_search ---

type symbolSearchArgs struct {
	Query string `json:"query"`
	Path  string `json:"path,omitempty"`
}

func (r *ToolRegistry) registerSymbolSearch() {
	r.add("symbol_search",
		"Find symbol definitions (functions, types, constants, vars) by name across the codebase. Go-aware: parses .go files with the AST to locate exact declaration lines. For other languages, matches definition-like lines (func/def/class/type/struct/const/var) containing the query. Returns file:line:kind:name.",
		json.RawMessage(`{
			"type":"object",
			"properties":{
				"query":{"type":"string","description":"Symbol name or substring to search for"},
				"path":{"type":"string","description":"Directory or file to search (defaults to '.')"}
			},
			"required":["query"]
		}`),
		func(ctx context.Context, args json.RawMessage) (string, error) {
			var a symbolSearchArgs
			if err := json.Unmarshal(args, &a); err != nil {
				return "", fmt.Errorf("invalid arguments for symbol_search: %w", err)
			}
			if a.Query == "" {
				return "", fmt.Errorf("symbol_search requires a non-empty \"query\" argument")
			}
			searchPath := a.Path
			if searchPath == "" {
				searchPath = "."
			}
			clean, err := validateToolPath(searchPath)
			if err != nil {
				return "", err
			}
			var hits []string
			maxHits := 60
			_ = filepath.Walk(clean, func(path string, info os.FileInfo, err error) error {
				if err != nil || len(hits) >= maxHits {
					return err
				}
				if info.IsDir() {
					name := info.Name()
					if name != "." && name != ".." && strings.HasPrefix(name, ".") {
						return filepath.SkipDir
					}
					return nil
				}
				if strings.HasSuffix(path, ".go") {
					goHits := searchGoSymbols(path, a.Query)
					hits = append(hits, goHits...)
				} else if isSourceLike(path) {
					hits = append(hits, searchGenericSymbols(path, a.Query)...)
				}
				return nil
			})
			if len(hits) == 0 {
				return fmt.Sprintf("No symbols matching %q found under %s.", a.Query, clean), nil
			}
			if len(hits) > maxHits {
				hits = hits[:maxHits]
			}
			return strings.Join(hits, "\n"), nil
		},
	)
}

// searchGoSymbols parses a .go file and returns declarations whose name
// contains the query (case-sensitive substring).
func searchGoSymbols(path, query string) []string {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return nil
	}
	var hits []string
	rel := displayPath(path)
	ast.Inspect(f, func(n ast.Node) bool {
		switch d := n.(type) {
		case *ast.FuncDecl:
			if d.Name != nil && strings.Contains(d.Name.Name, query) {
				pos := fset.Position(d.Pos())
				kind := "func"
				if d.Recv != nil {
					kind = "method"
				}
				hits = append(hits, fmt.Sprintf("%s:%d: %s %s", rel, pos.Line, kind, d.Name.Name))
			}
		case *ast.TypeSpec:
			if d.Name != nil && strings.Contains(d.Name.Name, query) {
				pos := fset.Position(d.Pos())
				hits = append(hits, fmt.Sprintf("%s:%d: type %s", rel, pos.Line, d.Name.Name))
			}
		case *ast.ValueSpec:
			if d.Names != nil {
				for _, name := range d.Names {
					if strings.Contains(name.Name, query) {
						pos := fset.Position(name.Pos())
						kind := "const"
						if d.Type != nil || len(d.Values) == 0 {
							kind = "var"
						}
						hits = append(hits, fmt.Sprintf("%s:%d: %s %s", rel, pos.Line, kind, name.Name))
					}
				}
			}
		}
		return true
	})
	return hits
}

// searchGenericSymbols matches definition-like lines in non-Go source files.
var genericDefRe = regexp.MustCompile(`(?i)^\s*(func|function|def|class|struct|type|interface|const|var|let|fn|public|private|static|async|export)\s+([A-Za-z0-9_]+)`)

func searchGenericSymbols(path, query string) []string {
	f, err := os.Open(path)
	if err != nil {
		return nil
	}
	defer f.Close()
	rel := displayPath(path)
	var hits []string
	scanner := bufioNewScanner(f, 1<<20)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		m := genericDefRe.FindStringSubmatch(line)
		if m != nil && strings.Contains(m[2], query) {
			hits = append(hits, fmt.Sprintf("%s:%d: %s %s", rel, lineNum, m[1], m[2]))
			if len(hits) >= 5 {
				break
			}
		}
	}
	return hits
}

func isSourceLike(path string) bool {
	ext := strings.ToLower(filepath.Ext(path))
	switch ext {
	case ".py", ".js", ".ts", ".tsx", ".jsx", ".rs", ".java", ".c", ".cpp", ".h",
		".hpp", ".rb", ".swift", ".kt", ".scala", ".sh", ".bash", ".zsh",
		".yaml", ".yml", ".toml", ".json", ".md", ".txt":
		return true
	}
	return false
}

func displayPath(path string) string {
	if rel, err := filepath.Rel(".", path); err == nil {
		return rel
	}
	return path
}

func bufioNewScanner(r io.Reader, maxLine int) *bufio.Scanner {
	s := bufio.NewScanner(r)
	s.Buffer(make([]byte, 0, 64*1024), maxLine)
	return s
}
