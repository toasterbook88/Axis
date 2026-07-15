package agent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
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
	dir := t.TempDir()
	if _, _, ok := loadRepoInstructions(dir); ok {
		t.Fatal("expected no instructions")
	}
}

func TestLoadRepoInstructionsTruncates(t *testing.T) {
	dir := t.TempDir()
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
	dir := t.TempDir()
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

func TestTruncateUTF8PrefixDoesNotSplitRune(t *testing.T) {
	// "世" is 3 bytes in UTF-8. Cap mid-rune must not produce invalid UTF-8.
	s := "ab" + "世界" + "cd"
	// len("ab世") == 2+3 = 5; cut at 4 would bisect 世 if naive.
	got := truncateUTF8Prefix(s, 4)
	if !utf8.ValidString(got) {
		t.Fatalf("invalid UTF-8 after truncate: %q bytes=%v", got, []byte(got))
	}
	if got != "ab" {
		t.Fatalf("got %q, want ab (dropped incomplete 世)", got)
	}
	if truncateUTF8Prefix("ascii-only", 100) != "ascii-only" {
		t.Fatal("short string should pass through")
	}
	if truncateUTF8Prefix("x", 0) != "" {
		t.Fatal("zero budget should yield empty")
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
	dir := t.TempDir()
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
