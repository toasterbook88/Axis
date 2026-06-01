package models

import (
	"strings"
	"testing"
)

func TestGenerateID(t *testing.T) {
	id := GenerateID("test")
	if !strings.HasPrefix(id, "test-") {
		t.Fatalf("expected prefix 'test-', got %q", id)
	}
	parts := strings.SplitN(id, "-", 2)
	if len(parts) != 2 {
		t.Fatalf("expected format prefix-hex, got %q", id)
	}
	if len(parts[1]) != 16 {
		t.Fatalf("expected 16 hex chars, got %d: %q", len(parts[1]), parts[1])
	}
}

func TestGenerateIDUnique(t *testing.T) {
	seen := make(map[string]struct{}, 100)
	for i := 0; i < 100; i++ {
		id := GenerateID("x")
		if _, ok := seen[id]; ok {
			t.Fatalf("duplicate ID generated: %q", id)
		}
		seen[id] = struct{}{}
	}
}
