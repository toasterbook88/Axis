package persist

import (
	"os"
	"path/filepath"
	"strings"
)

// axisDirName is the fixed directory name AXIS uses under the user's home
// directory for all persisted config, state, and cache files.
const axisDirName = ".axis"

// AxisHomeEnv names the environment variable that overrides the AXIS home
// directory. When set to a non-empty value it replaces ~/.axis wholesale — it
// names the directory itself, not the parent home, so AXIS_HOME=/tmp/x
// resolves state.json to /tmp/x/state.json.
//
// This is the isolation seam for audit finding C5: without it the test suite
// resolves every store to the operator's real ~/.axis. The Makefile test
// targets set it to a disposable directory. HOME is deliberately not used for
// that purpose — GOCACHE, GOPATH, and GOMODCACHE derive from HOME, so
// repointing it discards the build cache on every run.
const AxisHomeEnv = "AXIS_HOME"

// AxisDir returns the AXIS home directory: $AXIS_HOME when set and non-empty,
// otherwise ~/.axis.
//
// When the user's home directory cannot be determined, os.UserHomeDir's error
// is intentionally ignored and an empty home is used, yielding a relative
// ".axis" path. This mirrors the historical behaviour of the many call sites
// that constructed this path inline with `home, _ := os.UserHomeDir()`.
func AxisDir() string {
	if root := strings.TrimSpace(os.Getenv(AxisHomeEnv)); root != "" {
		return root
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, axisDirName)
}

// AxisPath joins elem onto the AXIS home directory. It is the shared
// replacement for the `filepath.Join(home, ".axis", ...)` pattern that was
// previously duplicated across the config, state, skills, reservation, daemon,
// events, auth, api, execution, chat, facts, and CLI packages.
//
// Every AXIS-owned path must resolve through here. Building a ~/.axis path
// from a direct os.UserHomeDir call bypasses the AXIS_HOME seam above and
// re-opens C5 for that store.
func AxisPath(elem ...string) string {
	return filepath.Join(append([]string{AxisDir()}, elem...)...)
}
