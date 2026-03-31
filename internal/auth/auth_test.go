package auth

import (
	"os"
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
