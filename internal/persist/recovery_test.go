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
