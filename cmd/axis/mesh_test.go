package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
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

func TestMeshCommandsPropagateWriterFailures(t *testing.T) {
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

	wantErr := errors.New("writer unavailable")
	for _, command := range []struct {
		name string
		new  func() *cobra.Command
	}{
		{name: "status", new: meshStatusCmd},
		{name: "peers", new: meshPeersCmd},
	} {
		t.Run(command.name, func(t *testing.T) {
			cmd := command.new()
			cmd.SetOut(rejectingOutputWriter{err: wantErr})
			cmd.SetErr(&strings.Builder{})
			if err := cmd.Execute(); !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want writer failure", err)
			}
		})
	}
}

func TestMeshCommandsHonorCanceledContext(t *testing.T) {
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

	for _, command := range []struct {
		name string
		new  func() *cobra.Command
	}{
		{name: "status", new: meshStatusCmd},
		{name: "peers", new: meshPeersCmd},
	} {
		t.Run(command.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			var out strings.Builder
			cmd := command.new()
			cmd.SilenceUsage = true
			cmd.SetContext(ctx)
			cmd.SetOut(&out)
			cmd.SetErr(&strings.Builder{})
			if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
			if out.Len() != 0 {
				t.Fatalf("canceled command wrote output: %q", out.String())
			}
		})
	}
}
