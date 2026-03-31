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
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, "token.*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()

	ok := false
	defer func() {
		if !ok {
			tmp.Close()
			os.Remove(tmpName)
		}
	}()

	if _, err := tmp.Write([]byte(token)); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmpName, 0600); err != nil {
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	ok = true
	// Enforce permissions on the destination even if it previously existed
	// with broader permissions.
	return os.Chmod(path, 0600)
}

// IsUnixAddr returns true if the address is a Unix socket path.
// Requires an explicit path prefix (/, ./, ../) to avoid misidentifying
// bare hostnames or IP addresses.
func IsUnixAddr(addr string) bool {
	return strings.HasPrefix(addr, "/") ||
		strings.HasPrefix(addr, "./") ||
		strings.HasPrefix(addr, "../")
}
