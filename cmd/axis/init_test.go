package main

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/config"
)

func TestInitCmd(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Mock input for readline:
	// 1. local node name -> "my-local-node"
	// 2. SSH user -> "my-ssh-user"
	// 3. Scan network [Y/n] -> "n"
	// 4. Manually add remote worker [y/N] -> "n"
	input := "my-local-node\nmy-ssh-user\nn\nn\n"
	inBuf := bytes.NewBufferString(input)
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	cmd := initCmd()
	cmd.SetIn(inBuf)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)

	err := cmd.Execute()
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("expected nil error or EOF from readline, got: %v", err)
	}

	cfgPath := filepath.Join(tempHome, ".axis", "nodes.yaml")
	info, err := os.Stat(cfgPath)
	if err != nil {
		t.Fatalf("config file not created: %v", err)
	}

	// Verify permissions are 0600 on Unix
	if os.PathSeparator == '/' {
		mode := info.Mode().Perm()
		if mode != 0600 {
			t.Errorf("expected file permissions 0600, got %O", mode)
		}
	}
}

func TestInitCmdManualRemote(t *testing.T) {
	tempHome := t.TempDir()
	t.Setenv("HOME", tempHome)

	// Mock verification function
	oldVerify := verifySSHConnectionFn
	verifySSHConnectionFn = func(ctx context.Context, host string, port int, user string, timeoutSec int, out io.Writer) bool {
		return true // Mock successful SSH connection
	}
	defer func() { verifySSHConnectionFn = oldVerify }()

	// Mock input for readline:
	// 1. local node name -> "my-local-node"
	// 2. SSH user -> "my-ssh-user"
	// 3. Scan network [Y/n] -> "n"
	// 4. Manually add remote worker [y/N] -> "y"
	// 5. Remote node name -> "remote-worker"
	// 6. Remote host -> "192.168.1.100"
	// 7. SSH port -> "2222"
	// 8. Timeout -> "15"
	// 9. Manually add another remote worker -> "n"
	input := "my-local-node\nmy-ssh-user\nn\ny\nremote-worker\n192.168.1.100\n2222\n15\nn\n"
	inBuf := bytes.NewBufferString(input)
	outBuf := new(bytes.Buffer)
	errBuf := new(bytes.Buffer)

	cmd := initCmd()
	cmd.SetIn(inBuf)
	cmd.SetOut(outBuf)
	cmd.SetErr(errBuf)

	err := cmd.Execute()
	if err != nil && !strings.Contains(err.Error(), "EOF") {
		t.Fatalf("expected nil error or EOF from readline, got: %v", err)
	}

	cfgPath := filepath.Join(tempHome, ".axis", "nodes.yaml")
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("failed to load generated config: %v", err)
	}

	if len(cfg.Nodes) != 2 {
		t.Fatalf("expected 2 nodes, got %d", len(cfg.Nodes))
	}

	local := cfg.Nodes[0]
	if local.Name != "my-local-node" || local.Hostname != "localhost" || local.SSHUser != "my-ssh-user" {
		t.Errorf("unexpected local node configuration: %+v", local)
	}

	remote := cfg.Nodes[1]
	if remote.Name != "remote-worker" || remote.Hostname != "192.168.1.100" || remote.SSHUser != "my-ssh-user" || remote.SSHPort != 2222 || remote.TimeoutSec != 15 {
		t.Errorf("unexpected remote node configuration: %+v", remote)
	}

	if cfg.Discovery == nil || !cfg.Discovery.Enabled || cfg.Discovery.Secret == "" {
		t.Errorf("discovery configuration was not set up properly: %+v", cfg.Discovery)
	}
}
