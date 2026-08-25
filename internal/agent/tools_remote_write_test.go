package agent

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// Regression: content containing the old heredoc delimiter (or any shell
// metacharacters) must round-trip byte-exact through the generated command
// instead of escaping and executing on the remote node.
func TestBuildRemoteWriteCmdRoundTripsHostileContent(t *testing.T) {
	content := "line one\nAXIS_EOF\nrm -rf /tmp/pwned; $(whoami) `id` | tee /tmp/pwned\nlast line\n"
	path := filepath.Join(t.TempDir(), "sub", "probe.txt")

	cmd := buildRemoteWriteCmd(path, content)
	out, err := exec.Command("/bin/sh", "-c", cmd).CombinedOutput()
	if err != nil {
		t.Fatalf("sh -c %q: %v\n%s", cmd, err, out)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != content {
		t.Fatalf("content mismatch:\n got: %q\nwant: %q", got, content)
	}
	if _, err := os.Stat("/tmp/pwned"); err == nil {
		t.Fatal("injected command executed — content escaped the write")
	}
	if !strings.Contains(cmd, "base64") {
		t.Fatal("expected base64 transport in generated command")
	}
}
