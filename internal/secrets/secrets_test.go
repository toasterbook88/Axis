package secrets_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/toasterbook88/axis/internal/secrets"
)

// --- Resolve ---

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
