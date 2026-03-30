package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)

const (
	TokenEnvVar = "AXIS_API_TOKEN"
)

// TokenPath returns ~/.axis/token
func TokenPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "token")
}

// LoadOrGenerateToken attempts to load the token from environment or file,
// generating a new one if necessary.
func LoadOrGenerateToken() (string, error) {
	if token := os.Getenv(TokenEnvVar); token != "" {
		return token, nil
	}

	path := TokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return "", fmt.Errorf("creating config directory: %w", err)
	}

	// Use a lock file to prevent race conditions during token generation
	lockPath := path + ".lock"
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return "", fmt.Errorf("creating lock file: %w", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return "", fmt.Errorf("locking token file: %w", err)
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)

	data, err := os.ReadFile(path)
	if err == nil {
		return strings.TrimSpace(string(data)), nil
	}

	if !os.IsNotExist(err) {
		return "", fmt.Errorf("reading token file: %w", err)
	}

	// Generate new token
	token, err := GenerateToken()
	if err != nil {
		return "", err
	}

	if err := SaveToken(token); err != nil {
		return "", err
	}

	return token, nil
}

// GenerateToken creates a secure random token.
func GenerateToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// SaveToken saves the token to ~/.axis/token with 0600 permissions.
func SaveToken(token string) error {
	path := TokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(token), 0600)
}

// IsUnixAddr returns true if the address appears to be a Unix socket path.
func IsUnixAddr(addr string) bool {
	if strings.HasPrefix(addr, "/") || strings.HasPrefix(addr, "./") || strings.HasPrefix(addr, "../") {
		return true
	}
	// Heuristic: if it doesn't have a colon, it's likely a local file path
	return !strings.Contains(addr, ":")
}
