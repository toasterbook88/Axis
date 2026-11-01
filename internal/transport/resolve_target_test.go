package transport

import (
	"context"
	"errors"
	"testing"
)

func stubSSHConfigCommand(t *testing.T, fn func(ctx context.Context, host string, port int, user string) (string, error)) {
	t.Helper()
	prev := runSSHConfigCommand
	runSSHConfigCommand = fn
	t.Cleanup(func() { runSSHConfigCommand = prev })
}

func TestResolveSSHTargetReturnsWhatTransportDials(t *testing.T) {
	var gotHost, gotUser string
	var gotPort int
	stubSSHConfigCommand(t, func(_ context.Context, host string, port int, user string) (string, error) {
		gotHost, gotPort, gotUser = host, port, user
		return "user deploy\nhostname 192.0.2.10\nport 2222\nhostkeyalias build-box\n" +
			"proxyjump jump.example.com\nproxycommand ssh -W %h:%p bastion.example.com\n", nil
	})
	got, err := ResolveSSHTarget(context.Background(), "build", 22, "operator")
	if err != nil {
		t.Fatalf("ResolveSSHTarget: %v", err)
	}
	if gotHost != "build" || gotPort != 22 || gotUser != "operator" {
		t.Fatalf("ssh -G called with %q %d %q", gotHost, gotPort, gotUser)
	}
	want := SSHTarget{Hostname: "192.0.2.10", Port: 2222, User: "deploy", HostKeyAlias: "build-box",
		ProxyJump: "jump.example.com", ProxyCommand: "ssh -W %h:%p bastion.example.com"}
	if got != want {
		t.Fatalf("ResolveSSHTarget = %+v, want %+v", got, want)
	}
}

// ssh -G prints "none" for an unset proxy; that is no proxy.
func TestResolveSSHTargetTreatsNoneAsNoProxy(t *testing.T) {
	stubSSHConfigCommand(t, func(context.Context, string, int, string) (string, error) {
		return "hostname alpha.example.com\nport 22\nproxyjump none\nproxycommand none\n", nil
	})
	got, err := ResolveSSHTarget(context.Background(), "alpha", 22, "")
	if err != nil {
		t.Fatalf("ResolveSSHTarget: %v", err)
	}
	if got.ProxyJump != "" || got.ProxyCommand != "" || got.Hostname != "alpha.example.com" {
		t.Fatalf("ResolveSSHTarget = %+v, want no proxy", got)
	}
}

func TestResolveSSHTargetReturnsTheCommandError(t *testing.T) {
	stubSSHConfigCommand(t, func(context.Context, string, int, string) (string, error) {
		return "", errors.New("ssh: not found")
	})
	if _, err := ResolveSSHTarget(context.Background(), "alpha", 22, ""); err == nil {
		t.Fatal("ResolveSSHTarget succeeded although ssh -G failed")
	}
}
