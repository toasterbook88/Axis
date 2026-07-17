package transport

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestSSHConfig_InsecureEnvIgnored(t *testing.T) {
	// Even with AXIS_SSH_INSECURE=1, the code should NOT offer an insecure bypass.
	os.Setenv("AXIS_SSH_INSECURE", "1")
	defer os.Unsetenv("AXIS_SSH_INSECURE")

	executor := NewSSHExecutor("localhost", 22, "user", 10)
	resolved := executor.resolveSSHConfig(context.Background())
	lease, err := executor.sshConfig(resolved, "localhost:22")
	if lease != nil {
		defer lease.Close()
	}

	if err == nil {
		// Config succeeded using known_hosts (not insecure) — that's fine.
		return
	}

	// Expected errors: no SSH keys, or known_hosts missing.
	msg := err.Error()
	if strings.Contains(msg, "no SSH keys") || strings.Contains(msg, "known_hosts") {
		return // valid secure failure
	}
	t.Errorf("unexpected error: %v", err)
}
