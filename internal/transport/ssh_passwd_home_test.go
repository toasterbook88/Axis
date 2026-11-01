package transport

import (
	"context"
	"errors"
	"fmt"
	"os"
	osexec "os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestSSHConfigCommandOmitsFOnlyWhenHomeIsThePasswdHome(t *testing.T) {
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	runsFile := filepath.Join(bin, "runs")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + runsFile + "'\n" +
		"echo 'hostname node.example.com'\n" +
		"case \" $* \" in *\" -F \"*) ;; *) echo 'proxyjump sys.example.com' ;; esac\n"
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	t.Setenv("HOME", home)
	t.Setenv("USER", "axis")

	uid := os.Getuid()
	for _, tc := range []struct {
		name          string
		passwd        string
		wantProxyJump string
	}{
		{"home is the passwd home", fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, home), "sys.example.com"},
		{"home differs", fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, t.TempDir()), ""},
		{"uid not in passwd", fmt.Sprintf("other:x:%d:%d::%s:/bin/sh\n", uid+1, uid+1, home), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			passwd := filepath.Join(t.TempDir(), "passwd")
			if err := os.WriteFile(passwd, []byte(tc.passwd), 0o644); err != nil {
				t.Fatal(err)
			}
			prev := sshPasswdFile
			sshPasswdFile = passwd
			t.Cleanup(func() { sshPasswdFile = prev })
			if err := os.WriteFile(runsFile, nil, 0o644); err != nil {
				t.Fatal(err)
			}
			out, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis")
			if err != nil {
				t.Fatalf("runSSHConfigCommand: %v", err)
			}
			if got := parseSSHConfigDump(out).ProxyJump; got != tc.wantProxyJump {
				t.Fatalf("ProxyJump = %q, want %q", got, tc.wantProxyJump)
			}
			raw, err := os.ReadFile(runsFile)
			if err != nil {
				t.Fatal(err)
			}
			if runs := strings.Count(string(raw), "\n"); runs != 1 {
				t.Fatalf("ssh -G ran %d times, want 1:\n%s", runs, raw)
			}
		})
	}
}

func usePasswdFile(t *testing.T, contents string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "passwd")
	if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	prev := sshPasswdFile
	sshPasswdFile = path
	t.Cleanup(func() { sshPasswdFile = prev })
}

func TestPasswdHome(t *testing.T) {
	uid := os.Getuid()
	usePasswdFile(t, fmt.Sprintf("other:x:%d:%d::/other:/bin/sh\naxis:x:%d:%d::/home/operator:/bin/sh\n", uid+2, uid+2, uid, uid))
	if got, err := passwdHome(uid); err != nil || got != "/home/operator" {
		t.Fatalf("passwdHome = %q, %v; want /home/operator", got, err)
	}
	if _, err := passwdHome(uid + 1); err == nil {
		t.Fatal("missing uid: want an error")
	}
	usePasswdFile(t, fmt.Sprintf("axis:x:%d:%d:::/bin/sh\n", uid, uid))
	if _, err := passwdHome(uid); err == nil {
		t.Fatal("empty home: want an error")
	}
	prev := sshPasswdFile
	sshPasswdFile = filepath.Join(t.TempDir(), "absent")
	t.Cleanup(func() { sshPasswdFile = prev })
	if _, err := passwdHome(uid); err == nil {
		t.Fatal("missing file: want an error")
	}
}

// fakeSSH installs an ssh on PATH that appends each argument list to the
// returned runs file and then runs body with "$F" set to yes when -F is present.
func fakeSSH(t *testing.T, body string) string {
	t.Helper()
	bin := t.TempDir()
	runs := filepath.Join(bin, "runs")
	script := "#!/bin/sh\nprintf '%s\\n' \"$*\" >> '" + runs + "'\n" +
		"case \" $* \" in *\" -F \"*) F=yes ;; *) F=no ;; esac\n" + body
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
	return runs
}

func homeWithSSHConfig(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "config"), []byte("Host *\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	uid := os.Getuid()
	usePasswdFile(t, fmt.Sprintf("axis:x:%d:%d::%s:/bin/sh\n", uid, uid, home))
	return home
}

func runLines(t *testing.T, runs string) []string {
	t.Helper()
	raw, err := os.ReadFile(runs)
	if err != nil {
		t.Fatal(err)
	}
	return strings.Split(strings.TrimSpace(string(raw)), "\n")
}

func TestSSHConfigCommandRetriesWithFWhenTheRunWithoutItFails(t *testing.T) {
	homeWithSSHConfig(t)
	runs := fakeSSH(t, "[ $F = no ] && { echo 'Bad owner or permissions' >&2; exit 255; }\n"+
		"echo 'hostname 127.0.0.1'\necho 'port 1'\necho 'proxyjump jump.example.com'\n")

	out, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis")
	if err != nil {
		t.Fatalf("runSSHConfigCommand: %v", err)
	}
	if got := parseSSHConfigDump(out).ProxyJump; got != "jump.example.com" {
		t.Fatalf("ProxyJump = %q, want the -F run's value", got)
	}
	lines := runLines(t, runs)
	if len(lines) != 2 || strings.Contains(lines[0], "-F") || !strings.Contains(lines[1], "-F") {
		t.Fatalf("ssh runs = %q, want one without -F then one with it", lines)
	}

	if err := os.WriteFile(runs, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	exec := NewSSHExecutor("node.example.com", 22, "axis", 1)
	wantDirectiveError(t, exec.Connect(context.Background()), "node.example.com", "ProxyJump")
}

func TestSSHConfigCommandReturnsTheSecondErrorWhenBothRunsFail(t *testing.T) {
	homeWithSSHConfig(t)
	runs := fakeSSH(t, "echo \"failed with -F=$F\" >&2\nexit 255\n")
	_, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis")
	var exitErr *osexec.ExitError
	if !errors.As(err, &exitErr) || !strings.Contains(string(exitErr.Stderr), "failed with -F=yes") {
		t.Fatalf("err = %v, want the -F run's exit error", err)
	}
	if lines := runLines(t, runs); len(lines) != 2 {
		t.Fatalf("ssh ran %d times, want 2", len(lines))
	}
}

func TestSSHConfigCommandTreatsASymlinkedHomeAsThePasswdHome(t *testing.T) {
	home := homeWithSSHConfig(t)
	link := filepath.Join(t.TempDir(), "home-link")
	if err := os.Symlink(home, link); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", link)
	runs := fakeSSH(t, "echo 'hostname node.example.com'\n")
	if _, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis"); err != nil {
		t.Fatalf("runSSHConfigCommand: %v", err)
	}
	if lines := runLines(t, runs); len(lines) != 1 || strings.Contains(lines[0], "-F") {
		t.Fatalf("ssh runs = %q, want one run without -F", lines)
	}
}

// bwrapSSH puts an ssh on PATH that runs the real ssh in a user namespace with
// the test's passwd file and a self-owned /etc/ssh holding systemConfig.
func bwrapSSH(t *testing.T, systemConfig string) {
	t.Helper()
	bwrap, err := osexec.LookPath("bwrap")
	if err != nil {
		t.Skip("bwrap not installed")
	}
	realSSH, err := osexec.LookPath("ssh")
	if err != nil {
		t.Skip("ssh not installed")
	}
	if err := osexec.Command(bwrap, "--dev-bind", "/", "/", "--unshare-user", "true").Run(); err != nil {
		t.Skipf("unprivileged user namespaces unavailable: %v", err)
	}
	etcSSH := t.TempDir()
	if err := os.WriteFile(filepath.Join(etcSSH, "ssh_config"), []byte(systemConfig), 0o644); err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	script := fmt.Sprintf("#!/bin/sh\nexec %q --dev-bind / / --unshare-user --bind %q /etc/passwd --bind %q /etc/ssh %q \"$@\"\n",
		bwrap, sshPasswdFile, etcSSH, realSSH)
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin)
}

func TestSSHConfigCommandWithRealSSHInASandbox(t *testing.T) {
	t.Run("system-only ProxyJump is seen and refused", func(t *testing.T) {
		homeWithSSHConfig(t)
		bwrapSSH(t, "Host *\n    ProxyJump sys.example.com\n")
		out, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis")
		if err != nil {
			t.Fatalf("runSSHConfigCommand: %v", err)
		}
		if got := parseSSHConfigDump(out).ProxyJump; got != "sys.example.com" {
			t.Fatalf("ProxyJump = %q, want the system file's value", got)
		}
		exec := NewSSHExecutor("node.example.com", 22, "axis", 1)
		wantDirectiveError(t, exec.Connect(context.Background()), "node.example.com", "ProxyJump")
	})
	t.Run("world-writable user config is still refused, not dialed", func(t *testing.T) {
		home := homeWithSSHConfig(t)
		config := filepath.Join(home, ".ssh", "config")
		if err := os.WriteFile(config, []byte("Host *\n    ProxyJump user.example.com\n"), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(config, 0o666); err != nil {
			t.Fatal(err)
		}
		bwrapSSH(t, "Host *\n    ForwardX11 no\n")
		out, err := runSSHConfigCommand(context.Background(), "node.example.com", 22, "axis")
		if err != nil {
			t.Fatalf("runSSHConfigCommand: %v", err)
		}
		if got := parseSSHConfigDump(out).ProxyJump; got != "user.example.com" {
			t.Fatalf("ProxyJump = %q, want the user file's value from the -F retry", got)
		}
		exec := NewSSHExecutor("node.example.com", 22, "axis", 1)
		wantDirectiveError(t, exec.Connect(context.Background()), "node.example.com", "ProxyJump")
	})
}
