package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/toasterbook88/axis/internal/persist"
)

const (
	TokenEnvVar = "AXIS_API_TOKEN"
)

// TokenPath returns ~/.axis/token
func TokenPath() string {
	return persist.AxisPath("token")
}

// LoadOrGenerateToken returns AXIS_API_TOKEN when set, else the token in
// ~/.axis/token, generating and saving a new one if the file is missing or
// does not hold a valid token (see parseToken).
func LoadOrGenerateToken() (string, error) {
	if token := os.Getenv(TokenEnvVar); token != "" {
		return token, nil
	}
	return loadOrGenerateTokenFile()
}

// loadOrGenerateTokenFile is the file half of LoadOrGenerateToken: it ignores
// AXIS_API_TOKEN. Generation happens under a lock file so concurrent first
// calls agree on one token.
func loadOrGenerateTokenFile() (string, error) {
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
		if token, parseErr := parseToken(data); parseErr == nil {
			return token, nil
		}
		// Token file is corrupted or empty; regenerate under the lock.
	} else if !os.IsNotExist(err) {
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
	return persist.WritePrivateFileAtomic(path, []byte(token))
}

// IsUnixAddr returns true if the address is a Unix socket path.
// Requires an explicit path prefix (/, ./, ../, unix://) to avoid misidentifying
// bare hostnames or IP addresses.
func IsUnixAddr(addr string) bool {
	return strings.HasPrefix(addr, "/") ||
		strings.HasPrefix(addr, "./") ||
		strings.HasPrefix(addr, "../") ||
		strings.HasPrefix(addr, "unix://")
}

// HttpClientForAddrWithTimeout returns an http.Client and base URL for the given address.
// If addr is a Unix domain socket (checked via IsUnixAddr), it configures a Unix dialer
// with keep-alives disabled to avoid leaking idle connections, and returns "http://localhost"
// as the base URL.
func HttpClientForAddrWithTimeout(addr string, timeout time.Duration) (*http.Client, string) {
	client := &http.Client{
		Timeout: timeout,
	}
	if IsUnixAddr(addr) {
		socketPath := strings.TrimPrefix(addr, "unix://")
		client.Transport = &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", socketPath)
			},
		}
		return client, "http://localhost"
	}
	return client, addr
}
