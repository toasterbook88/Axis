package persist

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestQuarantineCorruptFileRenamesAndPreservesContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")
	contents := "not-json"
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatalf("write corrupt file: %v", err)
	}

	warnErr := QuarantineCorruptFile(path, errors.New("bad json"))
	warning, ok := warnErr.(*RecoveryWarning)
	if !ok {
		t.Fatalf("expected RecoveryWarning, got %T", warnErr)
	}
	if warning.Path != path {
		t.Fatalf("warning path = %q, want %q", warning.Path, path)
	}
	if !strings.HasPrefix(warning.BackupPath, path+".corrupt-") {
		t.Fatalf("backup path = %q, want prefix %q", warning.BackupPath, path+".corrupt-")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("expected original file removed, stat err=%v", err)
	}
	data, err := os.ReadFile(warning.BackupPath)
	if err != nil {
		t.Fatalf("read backup file: %v", err)
	}
	if string(data) != contents {
		t.Fatalf("backup contents = %q, want %q", string(data), contents)
	}
	if !strings.Contains(warning.Error(), "quarantined corrupt file") {
		t.Fatalf("unexpected warning string: %q", warning.Error())
	}
}

func TestQuarantineCorruptFileReturnsRenameError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.json")

	err := QuarantineCorruptFile(path, errors.New("bad json"))
	if err == nil {
		t.Fatal("expected rename error")
	}
	if _, ok := err.(*RecoveryWarning); ok {
		t.Fatalf("expected hard error, got RecoveryWarning: %v", err)
	}
	if !strings.Contains(err.Error(), "rename corrupt file") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestWriteFileAtomicReplacesContents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.json")

	if err := WriteFileAtomic(path, []byte("first"), 0o644); err != nil {
		t.Fatalf("first WriteFileAtomic: %v", err)
	}
	if err := WriteFileAtomic(path, []byte("second"), 0o600); err != nil {
		t.Fatalf("second WriteFileAtomic: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if string(data) != "second" {
		t.Fatalf("contents = %q, want %q", string(data), "second")
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("mode = %o, want %o", info.Mode().Perm(), 0o600)
	}

	matches, err := filepath.Glob(filepath.Join(dir, "state.json.tmp-*"))
	if err != nil {
		t.Fatalf("Glob: %v", err)
	}
	if len(matches) != 0 {
		t.Fatalf("expected no temp files left behind, got %v", matches)
	}
}
