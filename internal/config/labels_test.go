package config

import (
	"testing"
	"strings"
)

func TestValidateLabels_Valid(t *testing.T) {
	n := &NodeConfig{Name: "a", Labels: map[string]string{
		"os":     "linux",
		"egress": "lan-only",
	}}
	if err := n.ValidateLabels(); err != nil {
		t.Fatalf("valid labels must pass: %v", err)
	}
}

func TestValidateLabels_EmptyAndNil(t *testing.T) {
	n := &NodeConfig{Name: "a"}
	if err := n.ValidateLabels(); err != nil {
		t.Fatalf("nil labels must pass: %v", err)
	}
	n.Labels = map[string]string{}
	if err := n.ValidateLabels(); err != nil {
		t.Fatalf("empty labels must pass: %v", err)
	}
}

func TestValidateLabels_RejectsEmpty(t *testing.T) {
	n := &NodeConfig{Name: "a", Labels: map[string]string{"": "v"}}
	err := n.ValidateLabels()
	if err == nil {
		t.Fatal("empty key must be rejected")
	}
	n = &NodeConfig{Name: "a", Labels: map[string]string{"k": ""}}
	if err := n.ValidateLabels(); err == nil {
		t.Fatal("empty value must be rejected")
	}
}

func TestValidateLabels_RejectsSeparators(t *testing.T) {
	// ':' and '=' are reserved separators for the k=v flag syntax.
	for _, bad := range []string{"a:b", "a=b", "a b"} {
		n := &NodeConfig{Name: "a", Labels: map[string]string{bad: "v"}}
		if err := n.ValidateLabels(); err == nil {
			t.Fatalf("key %q must be rejected", bad)
		}
	}
}

func TestValidateLabels_RejectsTooLong(t *testing.T) {
	long := make([]byte, 64)
	for i := range long {
		long[i] = 'a'
	}
	n := &NodeConfig{Name: "a", Labels: map[string]string{string(long): "v"}}
	if err := n.ValidateLabels(); err == nil {
		t.Fatal("key longer than 63 chars must be rejected")
	}
}

func TestValidateLabels_ErrorMessageContext(t *testing.T) {
	n := &NodeConfig{Name: "cranium", Labels: map[string]string{"bad key": "v"}}
	err := n.ValidateLabels()
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "cranium") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("error must name the node and key, got: %v", err)
	}
}

