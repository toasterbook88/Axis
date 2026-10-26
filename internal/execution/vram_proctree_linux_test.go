//go:build linux

package execution

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
)

func TestParentPIDFromStat(t *testing.T) {
	ppid, ok := parentPIDFromStat("123 (we ird) (name)) S 45 123 123 0")
	if !ok || ppid != 45 {
		t.Fatalf("parentPIDFromStat = %d, %v; want 45, true", ppid, ok)
	}
	if _, ok := parentPIDFromStat("garbage"); ok {
		t.Fatal("expected ok=false for malformed stat")
	}
}

func TestProcessTreeFromProc(t *testing.T) {
	dir := t.TempDir()
	write := func(pid, ppid int, comm string) {
		p := filepath.Join(dir, strconv.Itoa(pid))
		if err := os.MkdirAll(p, 0o700); err != nil {
			t.Fatal(err)
		}
		stat := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 0 0"
		if err := os.WriteFile(filepath.Join(p, "stat"), []byte(stat), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(10, 1, "bash")
	write(11, 10, "python (worker)")
	write(12, 11, "cuda helper")
	write(20, 1, "ollama")
	tree := processTreeFromProc(dir, 10)
	for _, pid := range []int{10, 11, 12} {
		if !tree[pid] {
			t.Errorf("pid %d missing from task tree %v", pid, tree)
		}
	}
	if tree[20] {
		t.Errorf("unrelated pid 20 attributed to the task tree")
	}
}
