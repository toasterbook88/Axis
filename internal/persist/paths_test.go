package persist

import (
	"path/filepath"
	"testing"
)

// The AXIS_HOME seam is the isolation mechanism for audit finding C5. If it
// stops being honoured, every store silently resolves to the operator's real
// ~/.axis again and `make test` writes production state.

func TestAxisDirHonoursAxisHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv(AxisHomeEnv, root)

	if got := AxisDir(); got != root {
		t.Fatalf("AxisDir() = %q, want %q", got, root)
	}
	want := filepath.Join(root, "state.json")
	if got := AxisPath("state.json"); got != want {
		t.Fatalf("AxisPath(\"state.json\") = %q, want %q", got, want)
	}
}

func TestAxisDirAxisHomeIsTheDirectoryNotTheParentHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv(AxisHomeEnv, root)

	// AXIS_HOME names ~/.axis's replacement, so no ".axis" element is appended.
	if got := AxisDir(); got == filepath.Join(root, axisDirName) {
		t.Fatalf("AxisDir() appended %q to AXIS_HOME: %q", axisDirName, got)
	}
}

func TestAxisDirFallsBackToHomeWhenAxisHomeUnsetOrBlank(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	want := filepath.Join(home, axisDirName)

	for _, tc := range []struct {
		name  string
		value string
	}{
		{"unset", ""},
		{"whitespace", "   "},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(AxisHomeEnv, tc.value)
			if got := AxisDir(); got != want {
				t.Fatalf("AxisDir() = %q, want %q", got, want)
			}
		})
	}
}
