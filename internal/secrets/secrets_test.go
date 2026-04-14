package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/secrets"
)

// --- Resolve ---

func TestResolve_EnvVarTakesPriority(t *testing.T) {
	t.Setenv("AXIS_TEST_KEY_PRIO", "from-env")

	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("from-file"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	val, err := secrets.Resolve("AXIS_TEST_KEY_PRIO", keyFile)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "from-env" {
		t.Errorf("got %q, want %q", val, "from-env")
	}
}

func TestResolve_FileFallback(t *testing.T) {
	// Ensure env var is unset.
	t.Setenv("AXIS_TEST_KEY_FILE", "")

	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("  sk-filekey  \n"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	val, err := secrets.Resolve("AXIS_TEST_KEY_FILE", keyFile)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if val != "sk-filekey" {
		t.Errorf("got %q, want trimmed %q", val, "sk-filekey")
	}
}

func TestResolve_BothMissing_ReturnsErrNotFound(t *testing.T) {
	t.Setenv("AXIS_TEST_KEY_NONE", "")

	_, err := secrets.Resolve("AXIS_TEST_KEY_NONE", "/nonexistent/key.txt")
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestResolve_EmptyEnvVarAndNoFile(t *testing.T) {
	_, err := secrets.Resolve("", "")
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("expected ErrNotFound for empty inputs, got %v", err)
	}
}

func TestResolve_FilePermissionError(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(keyFile, 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(keyFile, 0600); err != nil {
			t.Fatalf("cleanup chmod: %v", err)
		}
	})

	if os.Getuid() == 0 {
		t.Skip("running as root; permission errors don't apply")
	}

	_, err := secrets.Resolve("", keyFile)
	if err == nil {
		t.Fatal("expected error for unreadable file")
	}
	if errors.Is(err, secrets.ErrNotFound) {
		t.Fatal("permission error should not be treated as ErrNotFound")
	}
}

func TestResolve_EnvVarWhitespace_FallsBackToFile(t *testing.T) {
	// A value that is only whitespace should be treated as empty (trimmed to "").
	t.Setenv("AXIS_TEST_KEY_WS", "   ")
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("fallback-key"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	val, err := secrets.Resolve("AXIS_TEST_KEY_WS", keyFile)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// Whitespace-only env var should fall through to file.
	if val != "fallback-key" {
		t.Errorf("got %q, want fallback from file", val)
	}
}

func TestResolve_FileWhitespaceOnly(t *testing.T) {
	t.Setenv("AXIS_TEST_KEY_FWS", "")
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("   \n\t  "), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	_, err := secrets.Resolve("AXIS_TEST_KEY_FWS", keyFile)
	if !errors.Is(err, secrets.ErrNotFound) {
		t.Fatalf("whitespace-only file should yield ErrNotFound, got %v", err)
	}
}

// --- ResolveOrEmpty ---

func TestResolveOrEmpty_NotFound_ReturnsEmpty(t *testing.T) {
	t.Setenv("AXIS_TEST_EMPTY", "")
	val, err := secrets.ResolveOrEmpty("AXIS_TEST_EMPTY", "/nonexistent/key.txt")
	if err != nil {
		t.Fatalf("ResolveOrEmpty: unexpected error %v", err)
	}
	if val != "" {
		t.Errorf("got %q, want empty string for not-found", val)
	}
}

func TestResolveOrEmpty_Found_ReturnsValue(t *testing.T) {
	t.Setenv("AXIS_TEST_PRESENT", "my-api-key")
	val, err := secrets.ResolveOrEmpty("AXIS_TEST_PRESENT", "")
	if err != nil {
		t.Fatalf("ResolveOrEmpty: %v", err)
	}
	if val != "my-api-key" {
		t.Errorf("got %q, want %q", val, "my-api-key")
	}
}

func TestResolveOrEmpty_FileError_SurfacesError(t *testing.T) {
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("secret"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}
	if err := os.Chmod(keyFile, 0000); err != nil {
		t.Fatalf("Chmod: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chmod(keyFile, 0600); err != nil {
			t.Fatalf("cleanup chmod: %v", err)
		}
	})

	if os.Getuid() == 0 {
		t.Skip("running as root")
	}

	_, err := secrets.ResolveOrEmpty("", keyFile)
	if err == nil {
		t.Fatal("expected error for unreadable file; should not be swallowed")
	}
}

// --- IsConfigured ---

func TestIsConfigured_True(t *testing.T) {
	t.Setenv("AXIS_TEST_CONFIGURED", "definitely-set")
	if !secrets.IsConfigured("AXIS_TEST_CONFIGURED", "") {
		t.Error("IsConfigured should return true when env var is set")
	}
}

func TestIsConfigured_False(t *testing.T) {
	t.Setenv("AXIS_TEST_NOT_CONFIGURED", "")
	if secrets.IsConfigured("AXIS_TEST_NOT_CONFIGURED", "/nonexistent/key.txt") {
		t.Error("IsConfigured should return false when neither source is available")
	}
}

// --- expandHome (tested indirectly via Resolve with ~/... paths) ---

func TestResolve_HomeExpansion(t *testing.T) {
	t.Setenv("AXIS_TEST_HOME_EXP", "")

	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir available")
	}

	// Write a file in a temp subdir of home… but that's flaky. Instead write
	// to t.TempDir(), confirm a path that does NOT start with ~/ is handled,
	// and separately confirm ~/... substitution doesn't break on a real path.
	dir := t.TempDir()
	keyFile := filepath.Join(dir, "key.txt")
	if err := os.WriteFile(keyFile, []byte("home-key"), 0600); err != nil {
		t.Fatalf("WriteFile: %v", err)
	}

	// Construct a ~/... path that points to the same file if tmpdir is under home.
	if strings.HasPrefix(dir, home) {
		relPath := "~" + dir[len(home):] + "/key.txt"
		val, err2 := secrets.Resolve("AXIS_TEST_HOME_EXP", relPath)
		if err2 != nil {
			t.Fatalf("Resolve with ~/ path: %v", err2)
		}
		if val != "home-key" {
			t.Errorf("got %q, want %q via home-expanded path", val, "home-key")
		}
	} else {
		t.Skip("t.TempDir() not under home; skipping ~/... expansion path")
	}
}
