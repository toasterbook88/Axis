package agent

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

func newTestToolRegistry(t *testing.T) *ToolRegistry {
	t.Helper()
	return NewToolRegistry(NewToolContext(&RuntimeView{}, nil))
}

func execTool(t *testing.T, r *ToolRegistry, name string, args string) (string, error) {
	t.Helper()
	return r.Execute(context.Background(), name, json.RawMessage(args))
}

// chdirToTempDir changes into a fresh temp dir (so validateToolPath's CWD
// restriction is satisfied) and restores the original dir on cleanup.
func chdirToTempDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	orig, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("chdir: %v", err)
	}
	t.Cleanup(func() { _ = os.Chdir(orig) })
	return dir
}

func writeFile(t *testing.T, name, content string) {
	t.Helper()
	if err := os.WriteFile(name, []byte(content), 0644); err != nil {
		t.Fatalf("write %s: %v", name, err)
	}
}

func readFile(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(b)
}

func TestEditFileReplaceAll(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "f.txt", "foo bar foo baz foo")
	r := newTestToolRegistry(t)

	// Default (no replace_all): non-unique → error.
	_, err := execTool(t, r, "edit_file", mustJSON(t, map[string]any{
		"path": "f.txt", "target_content": "foo", "replacement_content": "FOO",
	}))
	if err == nil || !strings.Contains(err.Error(), "not unique") {
		t.Fatalf("expected not-unique error, got %v", err)
	}

	// replace_all=true replaces every occurrence.
	out, err := execTool(t, r, "edit_file", mustJSON(t, map[string]any{
		"path": "f.txt", "target_content": "foo", "replacement_content": "FOO", "replace_all": true,
	}))
	if err != nil {
		t.Fatalf("replace_all failed: %v", err)
	}
	if got := readFile(t, "f.txt"); got != "FOO bar FOO baz FOO" {
		t.Fatalf("got %q", got)
	}
	if !strings.Contains(out, "3 occurrence") {
		t.Fatalf("expected count in output, got %q", out)
	}
}

func TestEditFileUniqueStillWorks(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "u.txt", "alpha\nbeta\ngamma\n")
	r := newTestToolRegistry(t)

	_, err := execTool(t, r, "edit_file", mustJSON(t, map[string]any{
		"path": "u.txt", "target_content": "beta", "replacement_content": "BETA",
	}))
	if err != nil {
		t.Fatalf("unique edit failed: %v", err)
	}
	if got := readFile(t, "u.txt"); got != "alpha\nBETA\ngamma\n" {
		t.Fatalf("got %q", got)
	}
}

func TestMultiEditAppliesAllInOrder(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "m.txt", "one\ntwo\nthree\nfour\n")
	r := newTestToolRegistry(t)

	edits := []map[string]any{
		{"old_string": "one", "new_string": "ONE"},
		{"old_string": "two\nthree", "new_string": "TWO\nTHREE"},
		{"old_string": "four", "new_string": "FOUR"},
	}
	_, err := execTool(t, r, "multi_edit", mustJSON(t, map[string]any{"path": "m.txt", "edits": edits}))
	if err != nil {
		t.Fatalf("multi_edit failed: %v", err)
	}
	want := "ONE\nTWO\nTHREE\nFOUR\n"
	if got := readFile(t, "m.txt"); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestMultiEditStopsOnFirstError(t *testing.T) {
	chdirToTempDir(t)
	original := "alpha\nbeta\n"
	writeFile(t, "e.txt", original)
	r := newTestToolRegistry(t)

	edits := []map[string]any{
		{"old_string": "alpha", "new_string": "ALPHA"},
		{"old_string": "MISSING", "new_string": "x"},
		{"old_string": "beta", "new_string": "BETA"},
	}
	_, err := execTool(t, r, "multi_edit", mustJSON(t, map[string]any{"path": "e.txt", "edits": edits}))
	if err == nil || !strings.Contains(err.Error(), "edit #2") {
		t.Fatalf("expected edit #2 error, got %v", err)
	}
	// File must be unchanged because the failing edit aborted before write.
	if got := readFile(t, "e.txt"); got != original {
		t.Fatalf("file changed after aborted multi_edit: got %q", got)
	}
}

func TestMultiEditReplaceAllWithinBatch(t *testing.T) {
	chdirToTempDir(t)
	writeFile(t, "r.txt", "x x x\n")
	r := newTestToolRegistry(t)

	edits := []map[string]any{
		{"old_string": "x", "new_string": "Y", "replace_all": true},
		{"old_string": "Y Y Y", "new_string": "Z"},
	}
	_, err := execTool(t, r, "multi_edit", mustJSON(t, map[string]any{"path": "r.txt", "edits": edits}))
	if err != nil {
		t.Fatalf("multi_edit failed: %v", err)
	}
	if got := readFile(t, "r.txt"); got != "Z\n" {
		t.Fatalf("got %q", got)
	}
}

func TestTodoLifecycle(t *testing.T) {
	r := newTestToolRegistry(t)

	// init
	out, err := execTool(t, r, "todo", mustJSON(t, map[string]any{
		"op": "init",
		"items": []map[string]any{
			{"content": "write tests", "phase": "Test"},
			{"content": "update docs", "phase": "Docs"},
		},
	}))
	if err != nil {
		t.Fatalf("todo init: %v", err)
	}
	if !strings.Contains(out, "write tests") || !strings.Contains(out, "update docs") {
		t.Fatalf("init output missing items: %s", out)
	}

	// start one
	out, err = execTool(t, r, "todo", mustJSON(t, map[string]any{"op": "start", "task": "write tests"}))
	if err != nil {
		t.Fatalf("todo start: %v", err)
	}
	if !strings.Contains(out, "[~] write tests") {
		t.Fatalf("expected in_progress marker: %s", out)
	}

	// done
	out, err = execTool(t, r, "todo", mustJSON(t, map[string]any{"op": "done", "task": "write tests"}))
	if err != nil {
		t.Fatalf("todo done: %v", err)
	}
	if !strings.Contains(out, "[x] write tests") {
		t.Fatalf("expected done marker: %s", out)
	}

	// append
	out, err = execTool(t, r, "todo", mustJSON(t, map[string]any{
		"op": "append", "phase": "Test",
		"items": []map[string]any{{"content": "add edge case"}},
	}))
	if err != nil {
		t.Fatalf("todo append: %v", err)
	}
	if !strings.Contains(out, "add edge case") {
		t.Fatalf("append missing item: %s", out)
	}

	// drop
	out, err = execTool(t, r, "todo", mustJSON(t, map[string]any{"op": "drop", "task": "update docs"}))
	if err != nil {
		t.Fatalf("todo drop: %v", err)
	}
	if !strings.Contains(out, "[-] update docs") {
		t.Fatalf("expected dropped marker: %s", out)
	}

	// view
	out, err = execTool(t, r, "todo", mustJSON(t, map[string]any{"op": "view"}))
	if err != nil {
		t.Fatalf("todo view: %v", err)
	}
	if !strings.Contains(out, "[x] write tests") || !strings.Contains(out, "[-] update docs") {
		t.Fatalf("view missing state: %s", out)
	}
}

func TestTodoStartUnknownTaskErrors(t *testing.T) {
	r := newTestToolRegistry(t)
	execTool(t, r, "todo", mustJSON(t, map[string]any{
		"op": "init", "items": []map[string]any{{"content": "only task"}},
	}))
	_, err := execTool(t, r, "todo", mustJSON(t, map[string]any{"op": "done", "task": "nonexistent"}))
	if err == nil || !strings.Contains(err.Error(), "not found") {
		t.Fatalf("expected not-found error, got %v", err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return string(b)
}
