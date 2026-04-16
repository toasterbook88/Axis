package versioncmp

import (
	"errors"
	"fmt"
	"strings"

	"golang.org/x/mod/semver"
)

// Compare normalizes operator-facing AXIS release strings and compares them
// according to semantic-version ordering.
func Compare(current, latest string) (int, error) {
	vCurrent, err := normalize(current)
	if err != nil {
		return 0, fmt.Errorf("normalize current version %q: %w", current, err)
	}
	vLatest, err := normalize(latest)
	if err != nil {
		return 0, fmt.Errorf("normalize latest version %q: %w", latest, err)
	}
	return semver.Compare(vCurrent, vLatest), nil
}

func normalize(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", errors.New("empty version")
	}
	if !strings.HasPrefix(trimmed, "v") {
		trimmed = "v" + trimmed
	}

	core := trimmed
	suffix := ""
	if idx := strings.IndexAny(trimmed, "-+"); idx >= 0 {
		core = trimmed[:idx]
		suffix = trimmed[idx:]
	}

	coreParts := strings.Split(strings.TrimPrefix(core, "v"), ".")
	if len(coreParts) > 3 {
		return "", errors.New("too many numeric segments")
	}
	for len(coreParts) < 3 {
		coreParts = append(coreParts, "0")
	}

	normalized := "v" + strings.Join(coreParts, ".") + suffix
	if !semver.IsValid(normalized) {
		return "", fmt.Errorf("invalid semantic version %q", raw)
	}
	return normalized, nil
}
