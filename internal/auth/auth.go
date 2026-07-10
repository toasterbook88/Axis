package auth

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/toasterbook88/axis/internal/persist"
)

const (
	TokenEnvVar = "AXIS_API_TOKEN"
)

// TokenPath returns ~/.axis/token
func TokenPath() string {
	return persist.AxisPath("token")
}

// LoadOrGenerateToken attempts to load the token from environment or file,
// generating a new one if necessary.
func LoadOrGenerateToken() (string, error) {
	if token := os.Getenv(TokenEnvVar); token != "" {
		return token, nil
	}

	path := TokenPath()
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
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
		token := strings.TrimSpace(string(data))
		// Validate: token must be non-empty and the expected 64-char hex length.
		if len(token) == 64 {
			return token, nil
		}
		// Token file is corrupted or empty; regenerate under the lock.
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

// SaveToken saves the token to ~/.axis/token atomically with 0600 permissions.
// Uses a temp file + rename to avoid partial writes on crash.
func SaveToken(token string) error {
	path := TokenPath()
	// Create the directory with restrictive 0700 permissions before delegating
	// the atomic write; WriteFileAtomic's own MkdirAll will not loosen an
	// already-existing directory.
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	return persist.WriteFileAtomic(path, []byte(token), 0600)
}

// IsUnixAddr returns true if the address is a Unix socket path.
// Requires an explicit path prefix (/, ./, ../) to avoid misidentifying
// bare hostnames or IP addresses.
func IsUnixAddr(addr string) bool {
	return strings.HasPrefix(addr, "/") ||
		strings.HasPrefix(addr, "./") ||
		strings.HasPrefix(addr, "../")
}
