package persist

import (
	"os"
	"path/filepath"
)

// axisDirName is the fixed directory name AXIS uses under the user's home
// directory for all persisted config, state, and cache files.
const axisDirName = ".axis"

// AxisDir returns the AXIS home directory (~/.axis).
//
// When the user's home directory cannot be determined, os.UserHomeDir's error
// is intentionally ignored and an empty home is used, yielding a relative
// ".axis" path. This mirrors the historical behaviour of the many call sites
// that constructed this path inline with `home, _ := os.UserHomeDir()`.
func AxisDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, axisDirName)
}

// AxisPath joins elem onto the AXIS home directory (~/.axis). It is the shared
// replacement for the `filepath.Join(home, ".axis", ...)` pattern that was
// previously duplicated across the config, state, skills, reservation, daemon,
// events, auth, api, and execution packages.
func AxisPath(elem ...string) string {
	return filepath.Join(append([]string{AxisDir()}, elem...)...)
}
