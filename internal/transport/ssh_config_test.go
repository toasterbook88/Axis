package transport

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/pem"
	"net"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func TestParseSSHConfigDump(t *testing.T) {
	resolved := parseSSHConfigDump(`
hostname actual.example.com
user axis
port 2222
hostkeyalias known.example.com
hostkeyalgorithms ssh-ed25519,rsa-sha2-512
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
	if resolved.Hostname != "actual.example.com" {
		t.Fatalf("hostname = %q, want actual.example.com", resolved.Hostname)
	}
	if resolved.User != "axis" {
		t.Fatalf("user = %q, want axis", resolved.User)
	}
	if resolved.Port != 2222 {
		t.Fatalf("port = %d, want 2222", resolved.Port)
	}
	if resolved.HostKeyAlias != "known.example.com" {
		t.Fatalf("hostkeyalias = %q, want known.example.com", resolved.HostKeyAlias)
	}
	if len(resolved.HostKeyAlgorithms) != 2 {
		t.Fatalf("expected 2 host key algorithms, got %v", resolved.HostKeyAlgorithms)
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
	resolved := executor.resolveSSHConfig(context.Background())
	lease, err := executor.sshConfig(resolved, net.JoinHostPort("example.com", "22"))
	if err != nil {
		t.Fatalf("sshConfig: %v", err)
	}
	defer lease.Close()
	cfg := lease.config

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

func TestPreferredHostKeyAlgorithmsHonorExplicitSSHConfig(t *testing.T) {
	resolved := resolvedSSHConfig{
		HostKeyAlgorithms: []string{"ssh-ed25519", "rsa-sha2-512"},
	}
	got := preferredHostKeyAlgorithms(resolved, nil, "example.com:22")
	if len(got) != 2 || got[0] != "ssh-ed25519" || got[1] != "rsa-sha2-512" {
		t.Fatalf("preferredHostKeyAlgorithms = %v, want explicit config order", got)
	}
}

func TestKnownHostKeyAlgorithmsPreferKnownEntries(t *testing.T) {
	home := t.TempDir()
	sshDir := filepath.Join(home, ".ssh")
	if err := os.MkdirAll(sshDir, 0o755); err != nil {
		t.Fatalf("mkdir .ssh: %v", err)
	}

	knownHostsPath := filepath.Join(sshDir, "known_hosts")

	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("ed25519.GenerateKey: %v", err)
	}
	ed25519Pub, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatalf("ssh.NewPublicKey: %v", err)
	}

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("rsa.GenerateKey: %v", err)
	}
	rsaSigner, err := ssh.NewSignerFromKey(rsaKey)
	if err != nil {
		t.Fatalf("ssh.NewSignerFromKey: %v", err)
	}

	lineOne := knownhosts.Line([]string{"example.com:22"}, ed25519Pub)
	lineTwo := knownhosts.Line([]string{"example.com:22"}, rsaSigner.PublicKey())
	if err := os.WriteFile(knownHostsPath, []byte(lineOne+"\n"+lineTwo+"\n"), 0o644); err != nil {
		t.Fatalf("write known_hosts: %v", err)
	}

	got := knownHostKeyAlgorithms([]string{knownHostsPath}, "example.com:22")
	want := []string{
		ssh.KeyAlgoED25519,
		ssh.KeyAlgoRSASHA512,
		ssh.KeyAlgoRSASHA256,
		ssh.KeyAlgoRSA,
	}
	if len(got) != len(want) {
		t.Fatalf("knownHostKeyAlgorithms length = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("knownHostKeyAlgorithms[%d] = %q, want %q (full=%v)", i, got[i], want[i], got)
		}
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
