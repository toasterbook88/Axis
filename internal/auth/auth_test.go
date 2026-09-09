package auth

import (
	"encoding/hex"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
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

func TestIsUnixAddr(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"/var/run/axis.sock", true},
		{"./axis.sock", true},
		{"../axis.sock", true},
		{"unix:///home/user/.axis/axis.sock", true},
		{"unix://./axis.sock", true},
		{"127.0.0.1:8080", false},
		{"localhost:8080", false},
		{"axis.example.com:443", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			if got := IsUnixAddr(tc.addr); got != tc.want {
				t.Fatalf("IsUnixAddr(%q) = %v, want %v", tc.addr, got, tc.want)
			}
		})
	}
}

func TestHttpClientForAddrWithTimeout_UnixSchemeAndDisableKeepAlives(t *testing.T) {
	tmpDir := t.TempDir()
	sockPath := filepath.Join(tmpDir, "test.sock")

	ln, err := net.Listen("unix", sockPath)
	if err != nil {
		t.Fatalf("Listen unix: %v", err)
	}
	defer ln.Close()

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("pong"))
		}),
	}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()

	// Dial with unix:// scheme prefix
	client, baseURL := HttpClientForAddrWithTimeout("unix://"+sockPath, 2*time.Second)
	if baseURL != "http://localhost" {
		t.Fatalf("baseURL = %q, want http://localhost", baseURL)
	}

	tr, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("client.Transport is not *http.Transport: %T", client.Transport)
	}
	if !tr.DisableKeepAlives {
		t.Fatal("expected DisableKeepAlives to be true on ephemeral unix transport")
	}

	resp, err := client.Get(baseURL + "/ping")
	if err != nil {
		t.Fatalf("client.Get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
}
