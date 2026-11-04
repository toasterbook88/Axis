package transport

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestResolveSSHConfigLogsCommandFailure(t *testing.T) {
	var logs bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prevLog)

	prevRunner := runSSHConfigCommand
	runSSHConfigCommand = func(context.Context, string, int, string) (string, error) {
		return "", errors.New("exit status 255")
	}
	defer func() { runSSHConfigCommand = prevRunner }()

	e := NewSSHExecutor("node-a", 22, "axis", 5)
	got := e.resolveSSHConfigForHost(context.Background(), "node-a")
	if got.Hostname != "" || got.User != "" || len(got.IdentityFiles) != 0 {
		t.Fatalf("fallback config changed: %+v", got)
	}
	out := logs.String()
	for _, want := range []string{`"level":"WARN"`, `"msg":"transport: ssh -G failed; using built-in defaults"`, `"host":"node-a"`, `"error":"exit status 255"`} {
		if !strings.Contains(out, want) {
			t.Fatalf("missing %s in logs:\n%s", want, out)
		}
	}
}

func TestResolveSSHConfigSuccessDoesNotWarn(t *testing.T) {
	var logs bytes.Buffer
	prevLog := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prevLog)

	prevRunner := runSSHConfigCommand
	runSSHConfigCommand = func(context.Context, string, int, string) (string, error) {
		return "hostname node-a.example\nuser axis\nport 22\n", nil
	}
	defer func() { runSSHConfigCommand = prevRunner }()

	e := NewSSHExecutor("node-a", 22, "axis", 5)
	if got := e.resolveSSHConfigForHost(context.Background(), "node-a"); got.Hostname != "node-a.example" {
		t.Fatalf("hostname = %q", got.Hostname)
	}
	if strings.Contains(logs.String(), "ssh -G failed") {
		t.Fatalf("unexpected warning: %s", logs.String())
	}
}
