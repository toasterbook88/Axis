package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadRepoInstructionsNearestWins(t *testing.T) {
	root := t.TempDir()
	child := filepath.Join(root, "proj")
	if err := os.MkdirAll(child, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("parent rules"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(child, "AGENTS.md"), []byte("child rules"), 0o644); err != nil {
		t.Fatal(err)
	}

	path, content, ok := loadRepoInstructions(child)
	if !ok {
		t.Fatal("expected AGENTS.md")
	}
	if !strings.Contains(path, filepath.Join("proj", "AGENTS.md")) && !strings.HasSuffix(path, "proj/AGENTS.md") {
		// path is absolute; ensure it points at child file
		if filepath.Base(filepath.Dir(path)) != "proj" {
			t.Fatalf("expected child AGENTS.md, got %q", path)
		}
	}
	if content != "child rules" {
		t.Fatalf("content = %q, want child rules", content)
	}
}

func TestLoadRepoInstructionsWalksUp(t *testing.T) {
	root := t.TempDir()
	deep := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "AGENTS.md"), []byte("from-root"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, content, ok := loadRepoInstructions(deep)
	if !ok || content != "from-root" {
		t.Fatalf("ok=%v content=%q", ok, content)
	}
}

func TestLoadRepoInstructionsMissing(t *testing.T) {
	dir := tempRepoRoot(t)
	if _, _, ok := loadRepoInstructions(dir); ok {
		t.Fatal("expected no instructions")
	}
}

func TestLoadRepoInstructionsTruncates(t *testing.T) {
	dir := tempRepoRoot(t)
	// Build content larger than the cap.
	big := strings.Repeat("x", maxRepoInstructionsBytes+100)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(big), 0o644); err != nil {
		t.Fatal(err)
	}
	_, content, ok := loadRepoInstructions(dir)
	if !ok {
		t.Fatal("expected file")
	}
	if !strings.Contains(content, "truncated") {
		t.Fatalf("expected truncation marker, got len=%d", len(content))
	}
	if len(content) > maxRepoInstructionsBytes+200 {
		t.Fatalf("content still too large: %d", len(content))
	}
}

func TestLoadRepoInstructionsTruncatesTrimmedNotLeadingWhitespace(t *testing.T) {
	dir := tempRepoRoot(t)
	// Leading whitespace would dominate a raw-byte truncate; trimmed path keeps real rules.
	body := strings.Repeat(" ", 100) + "REAL-RULE-START " + strings.Repeat("y", maxRepoInstructionsBytes)
	if err := os.WriteFile(filepath.Join(dir, "AGENTS.md"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	_, content, ok := loadRepoInstructions(dir)
	if !ok {
		t.Fatal("expected file")
	}
	if !strings.Contains(content, "REAL-RULE-START") {
		t.Fatalf("expected meaningful content after trim+truncate, got prefix %q", content[:min(80, len(content))])
	}
	if strings.HasPrefix(content, " ") {
		t.Fatal("truncated content should not start with leading whitespace from raw bytes")
	}
}

func TestFormatRepoInstructionsBlock(t *testing.T) {
	s := formatRepoInstructionsBlock("/tmp/x/AGENTS.md", "Be careful with GPUs.")
	if !strings.Contains(s, "Repository instructions") || !strings.Contains(s, "Be careful with GPUs.") {
		t.Fatalf("block = %q", s)
	}
	if formatRepoInstructionsBlock("p", "  ") != "" {
		t.Fatal("empty content should yield empty block")
	}
}

func TestNewAgentInjectsRepoInstructions(t *testing.T) {
	dir := tempRepoRoot(t)
	t.Chdir(dir)
	if err := os.WriteFile("AGENTS.md", []byte("Prefer make test before push."), 0o644); err != nil {
		t.Fatal(err)
	}
	a := New(Config{
		Model:  "test",
		Output: os.Stderr,
	})
	found := false
	for _, m := range a.Conversation().Messages() {
		if strings.Contains(m.Content, "Prefer make test before push.") &&
			strings.Contains(m.Content, "Repository instructions") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("system prompt missing AGENTS.md content; messages=%d", len(a.Conversation().Messages()))
	}
}

// tempRepoRoot creates a hermetic fixture root: a TempDir containing a
// repository sentinel, so the walk-up in loadRepoInstructions terminates
// inside the fixture regardless of where TMPDIR sits on the host.
func tempRepoRoot(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestLoadRepoInstructionsStopsAtSentinel pins the walk boundary: with no
// AGENTS.md inside the repo tree, the walk must stop at the sentinel instead
// of leaking an AGENTS.md planted in a parent directory (e.g. the operator's
// home). This is the regression for the Hermes Pass-19 finding.
func TestLoadRepoInstructionsStopsAtSentinel(t *testing.T) {
	// Plant an AGENTS.md above the fixture tree, outside any sentinel.
	plant := filepath.Join(os.TempDir(), "AGENTS.md")
	_, readErr := os.ReadFile(plant)
	had := readErr == nil
	if had {
		_ = os.Rename(plant, plant+".bak-probe")
		defer func() {
			if had {
				_ = os.Rename(plant+".bak-probe", plant)
			} else {
				_ = os.Remove(plant)
			}
		}()
	}
	if err := os.WriteFile(plant, []byte("# parent leak"), 0o644); err != nil {
		t.Skipf("cannot plant probe file: %v", err)
	}

	dir := tempRepoRoot(t)
	path, content, ok := loadRepoInstructions(dir)
	if ok {
		t.Fatalf("sentinel must stop the walk; got path=%q content=%.40q", path, content)
	}
}
