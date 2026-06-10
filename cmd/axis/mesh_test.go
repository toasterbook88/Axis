package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestMeshStatusCmd(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	cfgPath := filepath.Join(tempHome, ".axis", "nodes.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0755); err != nil {
		t.Fatalf("mkdir config: %v", err)
	}
	content := `nodes:
  - name: local
    hostname: localhost
    ssh_user: axis
`
	if err := os.WriteFile(cfgPath, []byte(content), 0644); err != nil {
		t.Fatalf("write config: %v", err)
	}

	stdout, stderr, err := captureProcessOutput(t, func() error {
		cmd := meshStatusCmd()
		cmd.SetArgs([]string{})
		return cmd.Execute()
	})
	if err != nil {
		t.Fatalf("mesh status Execute: %v", err)
	}
	if stderr != "" {
		t.Fatalf("expected no stderr, got %q", stderr)
	}
	if !strings.Contains(stdout, "Gossip Mesh Discovery: DISABLED") {
		t.Errorf("expected disabled message, got %q", stdout)
	}
}
