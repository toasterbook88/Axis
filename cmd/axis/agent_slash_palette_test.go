package main

import (
	"testing"

	"github.com/toasterbook88/axis/internal/console"
)

func TestSlashPaletteMatchesHandlers(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range console.WiredSlashes {
		if _, ok := slashVerbHandlers[name]; !ok {
			t.Errorf("palette lists %s but it is not wired", name)
		}
		seen[name] = true
	}
	for name := range slashVerbHandlers {
		if !seen[name] {
			t.Errorf("wired slash %s is missing from the palette", name)
		}
	}
}
