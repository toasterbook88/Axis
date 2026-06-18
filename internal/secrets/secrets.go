// Package secrets is EXPERIMENTAL — safe API key resolution for AXIS.
// It is subordinate to observed state and emits warnings automatically.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// ErrNotFound is returned by Resolve when neither the environment variable
// nor the file path yields a non-empty value.
var ErrNotFound = errors.New("api key not found: neither env var nor file contained a value")

// Resolve returns an API key by probing envVar then filePath.
//
//   - If envVar is non-empty and the named environment variable is set and
//     non-empty, its value is returned immediately.
//   - If filePath is non-empty and the file exists and contains non-empty
//     content (after trimming whitespace), that content is returned.
//   - Otherwise ErrNotFound is returned.
//
// File read errors (permissions, etc.) are surfaced as wrapped errors so
// the caller can distinguish "not configured" from "configured but broken".
// The error message never contains the key value.
func Resolve(envVar, filePath string) (string, error) {
	// Env var takes priority.
	if envVar != "" {
		if val, exists := os.LookupEnv(envVar); exists {
			if trimmed := strings.TrimSpace(val); trimmed != "" {
				return trimmed, nil
			}
		}
	}

	// File fallback.
	if filePath != "" {
		expanded := expandHome(filePath)
		data, err := os.ReadFile(expanded)
		if err != nil {
			if os.IsNotExist(err) {
				// File not present — treat as "not configured", fall through.
			} else {
				return "", fmt.Errorf("secrets: reading key file %q: %w", filePath, err)
			}
		} else {
			if val := strings.TrimSpace(string(data)); val != "" {
				return val, nil
			}
		}
	}

	return "", ErrNotFound
}

// ResolveOrEmpty calls Resolve and returns an empty string instead of an error
// when the key is simply not configured (ErrNotFound). File read errors are
// still surfaced so misconfigured paths are not silently ignored.
//
// Use this when missing credentials should degrade gracefully (e.g. skipping
// a cloud provider) rather than hard-failing.
func ResolveOrEmpty(envVar, filePath string) (string, error) {
	val, err := Resolve(envVar, filePath)
	if errors.Is(err, ErrNotFound) {
		return "", nil
	}
	return val, err
}

// IsConfigured returns true if Resolve would succeed without error.
// It is a convenience wrapper for callers that only need to check presence.
func IsConfigured(envVar, filePath string) bool {
	_, err := Resolve(envVar, filePath)
	return err == nil
}

// expandHome replaces a leading "~/" with the user's home directory.
// Returns the original path unchanged if expansion fails or is not needed.
func expandHome(path string) string {
	if !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	return home + path[1:]
}
