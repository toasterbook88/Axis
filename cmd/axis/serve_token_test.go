package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/auth"
	"github.com/toasterbook88/axis/internal/persist"
)

const (
	serveTokOld = "3333333333333333333333333333333333333333333333333333333333333333"
	serveTokNew = "4444444444444444444444444444444444444444444444444444444444444444"
)

func TestServeTokenSourceEnvVarIsFixed(t *testing.T) {
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)
	t.Setenv(auth.TokenEnvVar, serveTokOld)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src, err := serveTokenSource(ctx, serveTokOld)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := src.(auth.StaticToken); !ok {
		t.Fatalf("with %s set, source = %T, want auth.StaticToken", auth.TokenEnvVar, src)
	}
	if err := os.WriteFile(filepath.Join(home, "token"), []byte(serveTokNew), 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(700 * time.Millisecond) // longer than one 500 ms poll
	if got := src.Current(); got != serveTokOld {
		t.Fatalf("Current() = %q, want the env token", got)
	}
}

func TestServeTokenSourceReloadsFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv(persist.AxisHomeEnv, home)
	t.Setenv(auth.TokenEnvVar, "")
	path := filepath.Join(home, "token")
	if err := os.WriteFile(path, []byte(serveTokOld), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	src, err := serveTokenSource(ctx, serveTokOld)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path+".tmp", []byte(serveTokNew), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path+".tmp", path); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for src.Current() != serveTokNew {
		if time.Now().After(deadline) {
			t.Fatal("daemon token source did not reload the rewritten file")
		}
		time.Sleep(20 * time.Millisecond)
	}
}
