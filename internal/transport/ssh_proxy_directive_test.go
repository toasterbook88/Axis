package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stubSSHConfigPerHost(t *testing.T, home string, outputs map[string]string) {
	t.Helper()
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	prev := runSSHConfigCommand
	runSSHConfigCommand = func(_ context.Context, host string, _ int, _ string) (string, error) {
		out, ok := outputs[host]
		if !ok {
			return "", fmt.Errorf("unexpected ssh -G for %q", host)
		}
		return out, nil
	}
	t.Cleanup(func() { runSSHConfigCommand = prev })
}

const proxyTestKeys = "identityfile ~/.ssh/test_key\nuserknownhostsfile ~/.ssh/known_hosts\n"

func liveProxyServer(t *testing.T) (*sshTestServer, string) {
	t.Helper()
	clientKey, clientSigner := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)
	server := startSSHTestServer(t, clientSigner.PublicKey(), hostSigner, map[string]sshCommandResponse{
		"echo hi": {stdout: "hi\n"},
	})
	t.Cleanup(server.Close)
	return server, writeSSHClientEnv(t, clientKey, hostSigner, server.Host(), server.Port())
}

func wantDirectiveError(t *testing.T, err error, host, directive string) {
	t.Helper()
	var de *UnsupportedSSHDirectiveError
	if !errors.As(err, &de) {
		t.Fatalf("Connect error = %v, want *UnsupportedSSHDirectiveError", err)
	}
	if de.Host != host || de.Directive != directive {
		t.Fatalf("directive error = %+v, want host %q directive %q", de, host, directive)
	}
	if msg := err.Error(); !strings.Contains(msg, directive) || !strings.Contains(msg, host) {
		t.Fatalf("message %q does not name %s and %s", msg, directive, host)
	}
}

func TestConnectRefusesProxyDirectives(t *testing.T) {
	for _, tc := range []struct{ line, directive string }{
		{"proxyjump jump.example.com", "ProxyJump"},
		{"proxycommand nc %h %p", "ProxyCommand"},
	} {
		t.Run(tc.directive, func(t *testing.T) {
			server, home := liveProxyServer(t)
			stubSSHConfigPerHost(t, home, map[string]string{
				"jumped": proxyTestKeys + "hostname " + server.Host() + "\n" + tc.line + "\n",
			})
			exec := NewSSHExecutor("jumped", server.Port(), "axis", 5)
			defer exec.Close()
			wantDirectiveError(t, exec.Connect(context.Background()), "jumped", tc.directive)
		})
	}
}

func TestConnectProxyJumpNoneStillDials(t *testing.T) {
	server, home := liveProxyServer(t)
	stubSSHConfigPerHost(t, home, map[string]string{
		"direct": proxyTestKeys + "hostname " + server.Host() + "\nproxyjump none\nproxycommand none\n",
	})
	exec := NewSSHExecutor("direct", server.Port(), "axis", 5)
	defer exec.Close()
	if err := exec.Connect(context.Background()); err != nil {
		t.Fatalf("Connect with proxyjump none: %v", err)
	}
}

func TestConnectRefusedHostIsNeverDialed(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	clientKey, _ := generateTestKeyPair(t)
	_, hostSigner := generateTestKeyPair(t)
	home := writeSSHClientEnv(t, clientKey, hostSigner, "127.0.0.1", port)
	stubSSHConfigPerHost(t, home, map[string]string{
		"jumped": proxyTestKeys + "hostname 127.0.0.1\nproxyjump jump.example.com\n",
	})

	exec := NewSSHExecutorWithDialTarget("jumped", port, "axis", 1, "127.0.0.1")
	wantDirectiveError(t, exec.Connect(context.Background()), "jumped", "ProxyJump")

	if err := listener.(*net.TCPListener).SetDeadline(time.Now().Add(100 * time.Millisecond)); err != nil {
		t.Fatalf("set deadline: %v", err)
	}
	if conn, err := listener.Accept(); err == nil {
		conn.Close()
		t.Fatal("a TCP connection reached the refused host")
	}
}

func TestConnectFallsBackPastARefusedHost(t *testing.T) {
	server, home := liveProxyServer(t)
	stubSSHConfigPerHost(t, home, map[string]string{
		"jumped": proxyTestKeys + "hostname " + server.Host() + "\nproxyjump jump.example.com\n",
		"direct": proxyTestKeys + "hostname " + server.Host() + "\n",
	})
	exec := NewSSHExecutorFromDial("jumped", server.Port(), "axis", 5, []string{"direct"})
	defer exec.Close()
	if err := exec.Connect(context.Background()); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	if got := exec.ConnectedHost(); got != "direct" {
		t.Fatalf("ConnectedHost = %q, want direct", got)
	}
}

func TestConnectRefusalSurvivesALaterDialFailure(t *testing.T) {
	server, home := liveProxyServer(t)
	stubSSHConfigPerHost(t, home, map[string]string{
		"jumped": proxyTestKeys + "hostname " + server.Host() + "\nproxyjump jump.example.com\n",
		"dead":   proxyTestKeys + "hostname 127.0.0.1\nport 1\n",
	})
	exec := NewSSHExecutorFromDial("jumped", server.Port(), "axis", 2, []string{"dead"})
	err := exec.Connect(context.Background())
	wantDirectiveError(t, err, "jumped", "ProxyJump")
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("Connect error = %v, want the fallback's dial error too", err)
	}
	if strings.Index(err.Error(), "ssh dial") > strings.Index(err.Error(), "ProxyJump") {
		t.Fatalf("dial error should come first: %q", err)
	}
}

func TestUnsupportedSSHDirectiveMessage(t *testing.T) {
	msg := (&UnsupportedSSHDirectiveError{Host: "node-a", Directive: "ProxyCommand"}).Error()
	for _, want := range []string{"node-a", "ProxyCommand", "did not dial", "reach directly", "ProxyCommand none"} {
		if !strings.Contains(msg, want) {
			t.Fatalf("message %q lacks %q", msg, want)
		}
	}
}

func TestConnectWildcardProxyRuleAndItsOverride(t *testing.T) {
	server, home := liveProxyServer(t)
	stubSSHConfigPerHost(t, home, map[string]string{
		"node-a": proxyTestKeys + "hostname " + server.Host() + "\nproxyjump jump.example.com\n",
		"node-b": proxyTestKeys + "hostname " + server.Host() + "\n",
	})
	refused := NewSSHExecutor("node-a", server.Port(), "axis", 5)
	wantDirectiveError(t, refused.Connect(context.Background()), "node-a", "ProxyJump")
	overridden := NewSSHExecutor("node-b", server.Port(), "axis", 5)
	defer overridden.Close()
	if err := overridden.Connect(context.Background()); err != nil {
		t.Fatalf("host with proxyjump none (omitted by ssh -G): %v", err)
	}
}

func TestConnectDialFailureSurvivesALaterRefusal(t *testing.T) {
	server, home := liveProxyServer(t)
	stubSSHConfigPerHost(t, home, map[string]string{
		"dead":   proxyTestKeys + "hostname 127.0.0.1\nport 1\n",
		"jumped": proxyTestKeys + "hostname " + server.Host() + "\nproxyjump jump.example.com\n",
	})
	exec := NewSSHExecutorFromDial("dead", server.Port(), "axis", 2, []string{"jumped"})
	err := exec.Connect(context.Background())
	wantDirectiveError(t, err, "jumped", "ProxyJump")
	var opErr *net.OpError
	if !errors.As(err, &opErr) {
		t.Fatalf("Connect error = %v, want the primary's dial error too", err)
	}
	if strings.Index(err.Error(), "ssh dial") > strings.Index(err.Error(), "ProxyJump") {
		t.Fatalf("dial error should come first: %q", err)
	}
}

func TestConnectDialsWithDefaultsWhenSSHConfigFails(t *testing.T) {
	server, home := liveProxyServer(t)
	key, err := os.ReadFile(filepath.Join(home, ".ssh", "test_key"))
	if err != nil {
		t.Fatalf("read key: %v", err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_rsa"), key, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("SSH_AUTH_SOCK", "")
	prev := runSSHConfigCommand
	runSSHConfigCommand = func(context.Context, string, int, string) (string, error) {
		return "", errors.New("ssh -G: bad configuration option")
	}
	t.Cleanup(func() { runSSHConfigCommand = prev })

	exec := NewSSHExecutor(server.Host(), server.Port(), "axis", 5)
	defer exec.Close()
	if err := exec.Connect(context.Background()); err != nil {
		t.Fatalf("Connect with a failing ssh -G: %v", err)
	}
}

func TestConnectKeepsTheMostRecentDialError(t *testing.T) {
	server, home := liveProxyServer(t)
	stubSSHConfigPerHost(t, home, map[string]string{
		"dead-1": proxyTestKeys + "hostname 127.0.0.1\nport 1\n",
		"dead-3": proxyTestKeys + "hostname 127.0.0.1\nport 3\n",
		"jumped": proxyTestKeys + "hostname " + server.Host() + "\nproxyjump jump.example.com\n",
	})
	exec := NewSSHExecutorFromDial("dead-1", server.Port(), "axis", 2, []string{"jumped", "dead-3"})
	err := exec.Connect(context.Background())
	wantDirectiveError(t, err, "jumped", "ProxyJump")
	if msg := err.Error(); !strings.Contains(msg, "127.0.0.1:3:") || strings.Contains(msg, "127.0.0.1:1:") {
		t.Fatalf("want only the most recent dial error (port 3): %q", msg)
	}
}
