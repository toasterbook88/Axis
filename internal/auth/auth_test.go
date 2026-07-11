package auth

import (
	"encoding/hex"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

func TestLoadOrGenerateToken_Race(t *testing.T) {
	tmpDir := t.TempDir()
	originalHome := os.Getenv("HOME")
	os.Setenv("HOME", tmpDir)
	defer os.Setenv("HOME", originalHome)

	const goroutines = 10
	var wg sync.WaitGroup
	tokens := make([]string, goroutines)

	wg.Add(goroutines)
	for i := 0; i < goroutines; i++ {
		go func(idx int) {
			defer wg.Done()
			token, err := LoadOrGenerateToken()
			if err != nil {
				t.Errorf("failed to load/generate token: %v", err)
				return
			}
			tokens[idx] = token
		}(i)
	}
	wg.Wait()

	// Verify all tokens are the same
	firstToken := tokens[0]
	for i, token := range tokens {
		if token != firstToken {
			t.Errorf("token mismatch at index %d: expected %s, got %s", i, firstToken, token)
		}
	}

	// Verify token file exists and has correct permissions
	path := TokenPath()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("token file not found: %v", err)
	}
	if info.Mode().Perm() != 0600 {
		t.Errorf("expected 0600 permissions, got %o", info.Mode().Perm())
	}
}

func TestLoadOrGenerateToken_RegeneratesInvalidFile(t *testing.T) {
	tests := map[string]string{
		"empty":      "",
		"short":      "abc123",
		"non-hex-64": "zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz",
	}
	for name, contents := range tests {
		t.Run(name, func(t *testing.T) {
			home := t.TempDir()
			t.Setenv("HOME", home)
			path := filepath.Join(home, ".axis", "token")
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatalf("MkdirAll: %v", err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			token, err := LoadOrGenerateToken()
			if err != nil {
				t.Fatalf("LoadOrGenerateToken: %v", err)
			}
			decoded, err := hex.DecodeString(token)
			if err != nil || len(decoded) != 32 {
				t.Fatalf("regenerated token is not 32-byte hex: %q, err=%v", token, err)
			}
			persisted, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			if string(persisted) != token {
				t.Fatalf("persisted token = %q, want %q", persisted, token)
			}
		})
	}
}
