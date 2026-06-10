package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
