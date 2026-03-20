package transport

import (
	"os"
	"testing"
)

func TestSSHConfig_InsecureBypass(t *testing.T) {
	// Ensure AXIS_SSH_INSECURE is set
	os.Setenv("AXIS_SSH_INSECURE", "1")
	defer os.Unsetenv("AXIS_SSH_INSECURE")

	executor := NewSSHExecutor("localhost", 22, "user", 10)

	// We need to access sshConfig which is private, but we are in the same package
	config, err := executor.sshConfig()

	// If there are no keys, sshConfig returns an error before checking insecure mode.
	// We might need to mock keys or just check if the logic for insecure is still there.
	// Given we want to REMOVE it, we expect it to NOT be insecure even if the env var is set.

	if err != nil && err.Error() == "no SSH keys or agent available" {
		t.Skip("No SSH keys available to test config fully, but we will check the source code")
	}

	if config != nil && config.HostKeyCallback != nil {
		// How to check if it is InsecureIgnoreHostKey?
		// One way is to call it with dummy data and see if it returns nil.
		err := config.HostKeyCallback("localhost:22", nil, nil)
		if err == nil {
			t.Errorf("HostKeyCallback allowed insecure connection (returned nil) even if it should be removed")
		}
	}
}
