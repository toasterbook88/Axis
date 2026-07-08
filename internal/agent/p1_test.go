package agent

import (
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestSymbolSearchGoAST(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "sample.go", "package main\n\nfunc Foo() {}\n\nfunc (r *Bar) Method() {}\n\ntype Bar struct{ X int }\n\nconst Pi = 3.14\n")
	r := newTestToolRegistry(t)

	out, err := execTool(t, r, "symbol_search", mustJSON(t, map[string]any{"query": "Foo", "path": "."}))
	if err != nil {
		t.Fatalf("symbol_search: %v", err)
	}
	if !strings.Contains(out, "sample.go") || !strings.Contains(out, "func Foo") {
		t.Fatalf("expected Foo function hit, got: %s", out)
	}

	out, _ = execTool(t, r, "symbol_search", mustJSON(t, map[string]any{"query": "Method", "path": "."}))
	if !strings.Contains(out, "method Method") {
		t.Fatalf("expected method hit, got: %s", out)
	}

	out, _ = execTool(t, r, "symbol_search", mustJSON(t, map[string]any{"query": "Bar", "path": "."}))
	if !strings.Contains(out, "type Bar") {
		t.Fatalf("expected type Bar hit, got: %s", out)
	}

	out, _ = execTool(t, r, "symbol_search", mustJSON(t, map[string]any{"query": "Pi", "path": "."}))
	if !strings.Contains(out, "Pi") {
		t.Fatalf("expected Pi hit, got: %s", out)
	}
}

func TestSymbolSearchNoMatch(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "sample.go", "package main\n\nfunc Foo() {}\n")
	r := newTestToolRegistry(t)
	out, _ := execTool(t, r, "symbol_search", mustJSON(t, map[string]any{"query": "Nonexistent", "path": "."}))
	if !strings.Contains(out, "No symbols matching") {
		t.Fatalf("expected no-match message, got: %s", out)
	}
}

func TestWebFetchStripsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<html><head><title>Ignored</title><style>body{}</style></head><body><h1>Title</h1><p>Hello world<script>alert(1)</script></p></body></html>`))
	}))
	defer srv.Close()
	r := newTestToolRegistry(t)
	out, err := execTool(t, r, "web_fetch", mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("web_fetch: %v", err)
	}
	if !strings.Contains(out, "Title") || !strings.Contains(out, "Hello world") {
		t.Fatalf("expected readable text, got: %s", out)
	}
	if strings.Contains(out, "<script>") || strings.Contains(out, "alert(1)") || strings.Contains(out, "Ignored") {
		t.Fatalf("html/script/title not stripped, got: %s", out)
	}
}

func TestWebFetchRejectsNonHTTP(t *testing.T) {
	r := newTestToolRegistry(t)
	_, err := execTool(t, r, "web_fetch", mustJSON(t, map[string]any{"url": "file:///etc/passwd"}))
	if err == nil || !strings.Contains(err.Error(), "http or https") {
		t.Fatalf("expected scheme rejection, got %v", err)
	}
}

func TestWebFetchJSON(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"key":"value","n":42}`))
	}))
	defer srv.Close()
	r := newTestToolRegistry(t)
	out, err := execTool(t, r, "web_fetch", mustJSON(t, map[string]any{"url": srv.URL}))
	if err != nil {
		t.Fatalf("web_fetch json: %v", err)
	}
	if !strings.Contains(out, "value") || !strings.Contains(out, "42") {
		t.Fatalf("expected json body, got: %s", out)
	}
}

func TestUndoLastRestoresPriorContent(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "u.txt", "original\n")
	r := newTestToolRegistry(t)

	_, err := execTool(t, r, "edit_file", mustJSON(t, map[string]any{
		"path": "u.txt", "target_content": "original", "replacement_content": "changed",
	}))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	if got := readFile(t, "u.txt"); got != "changed\n" {
		t.Fatalf("precondition: got %q", got)
	}

	out, err := execTool(t, r, "undo_last", mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("undo_last: %v", err)
	}
	if !strings.Contains(out, "restored") {
		t.Fatalf("expected restored message, got: %s", out)
	}
	if got := readFile(t, "u.txt"); got != "original\n" {
		t.Fatalf("after undo got %q want original\\n", got)
	}
}

func TestUndoLastDeletesNewFile(t *testing.T) {
	chdirToTempDir(t)
	r := newTestToolRegistry(t)

	_, err := execTool(t, r, "write_file", mustJSON(t, map[string]any{"path": "new.txt", "content": "fresh"}))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if _, err := os.Stat("new.txt"); err != nil {
		t.Fatalf("file should exist")
	}
	out, err := execTool(t, r, "undo_last", mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("undo: %v", err)
	}
	if !strings.Contains(out, "removed") {
		t.Fatalf("expected removed message, got: %s", out)
	}
	if _, err := os.Stat("new.txt"); !os.IsNotExist(err) {
		t.Fatalf("file should be gone after undo")
	}
}

func TestUndoLastEmpty(t *testing.T) {
	r := newTestToolRegistry(t)
	out, err := execTool(t, r, "undo_last", mustJSON(t, map[string]any{}))
	if err != nil || !strings.Contains(out, "Nothing to undo") {
		t.Fatalf("expected empty message, got: %q err %v", out, err)
	}
}

func TestReviewChangesInGitRepo(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	chdirToTempDir(t)
	dir, _ := os.Getwd()
	mustRunGit(t, dir, "init")
	mustRunGit(t, dir, "config", "user.email", "t@t")
	mustRunGit(t, dir, "config", "user.name", "t")
	writeFile(t, "base.txt", "v1\n")
	mustRunGit(t, dir, "add", "base.txt")
	mustRunGit(t, dir, "commit", "-m", "base")

	r := newTestToolRegistry(t)
	_, err := execTool(t, r, "edit_file", mustJSON(t, map[string]any{
		"path": "base.txt", "target_content": "v1", "replacement_content": "v2",
	}))
	if err != nil {
		t.Fatalf("edit: %v", err)
	}
	out, err := execTool(t, r, "review_changes", mustJSON(t, map[string]any{}))
	if err != nil {
		t.Fatalf("review_changes: %v", err)
	}
	if !strings.Contains(out, "Dirty") || !strings.Contains(out, "base.txt") {
		t.Fatalf("expected dirty + base.txt in review, got: %s", out)
	}
}

func mustRunGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %s", args, dir, string(out))
	}
}
