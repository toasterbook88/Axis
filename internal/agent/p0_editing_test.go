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
