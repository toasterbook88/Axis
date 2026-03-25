package transport

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
)

func TestParseSSHConfigDump(t *testing.T) {
	resolved := parseSSHConfigDump(`
identityfile ~/.ssh/custom_key
identityfile /tmp/extra_key
userknownhostsfile ~/.ssh/custom_known_hosts
globalknownhostsfile /etc/ssh/ssh_known_hosts
`)

	if len(resolved.IdentityFiles) != 2 {
		t.Fatalf("expected 2 identity files, got %d", len(resolved.IdentityFiles))
	}
	if len(resolved.UserKnownHostsFiles) != 1 {
		t.Fatalf("expected 1 user known_hosts file, got %d", len(resolved.UserKnownHostsFiles))
	}
	if len(resolved.GlobalKnownHostsFile) != 1 {
		t.Fatalf("expected 1 global known_hosts file, got %d", len(resolved.GlobalKnownHostsFile))
	}
}

func TestSignerPathsPreferResolvedIdentityFiles(t *testing.T) {
	paths := signerPaths("/tmp/home", resolvedSSHConfig{
		IdentityFiles: []string{"~/.ssh/custom_key", "/tmp/home/.ssh/id_ed25519"},
	})

	if len(paths) < 3 {
		t.Fatalf("expected resolved identity and defaults, got %#v", paths)
	}
	if paths[0] != "/tmp/home/.ssh/custom_key" {
		t.Fatalf("expected resolved identity first, got %#v", paths)
	}
}

func TestSSHConfigUsesResolvedIdentityAndKnownHosts(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	keyPath := filepath.Join(sshDir, "custom_key")
	if err := writeTestPrivateKey(keyPath); err != nil {
		t.Fatalf("write private key: %v", err)
	}

	knownHostsPath := filepath.Join(sshDir, "custom_known_hosts")
	if err := writeTestKnownHosts(knownHostsPath, "example.com"); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	prevHome := os.Getenv("HOME")
	if err := os.Setenv("HOME", home); err != nil {
		t.Fatalf("set HOME: %v", err)
	}
	defer os.Setenv("HOME", prevHome)

	prevSSHAuthSock := os.Getenv("SSH_AUTH_SOCK")
	if err := os.Unsetenv("SSH_AUTH_SOCK"); err != nil {
		t.Fatalf("unset SSH_AUTH_SOCK: %v", err)
	}
	defer os.Setenv("SSH_AUTH_SOCK", prevSSHAuthSock)

	prevRunner := runSSHConfigCommand
	runSSHConfigCommand = func(ctx context.Context, host string, port int, user string) (string, error) {
		return "identityfile ~/.ssh/custom_key\nuserknownhostsfile ~/.ssh/custom_known_hosts\n", nil
	}
	defer func() { runSSHConfigCommand = prevRunner }()

	executor := NewSSHExecutor("example.com", 22, "axis", 10)
	cfg, err := executor.sshConfig(context.Background())
	if err != nil {
		t.Fatalf("sshConfig: %v", err)
	}

	if cfg.User != "axis" {
		t.Fatalf("expected ssh user axis, got %q", cfg.User)
	}
	if len(cfg.Auth) == 0 {
		t.Fatal("expected at least one auth method")
	}
	if cfg.HostKeyCallback == nil {
		t.Fatal("expected host key callback")
	}
}

func writeTestPrivateKey(path string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	block := &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(key),
	}
	return os.WriteFile(path, pem.EncodeToMemory(block), 0o600)
}

func writeTestKnownHosts(path, host string) error {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	signer, err := ssh.NewSignerFromKey(key)
	if err != nil {
		return err
	}
	line := host + " " + string(ssh.MarshalAuthorizedKey(signer.PublicKey()))
	return os.WriteFile(path, []byte(line), 0o644)
}
