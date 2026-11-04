package daemon

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestRefreshFailureIsLogged(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, nil)))
	defer slog.SetDefault(prev)

	d := New(time.Minute, func(context.Context) (*models.ClusterSnapshot, error) {
		return nil, errors.New("collector boom")
	})
	d.SetSnapshotPath("")
	if err := d.Refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	got := logs.String()
	for _, want := range []string{`"msg":"daemon: refresh failed"`, `"level":"WARN"`, `"trigger":"manual"`, `"error":"collector boom"`, `"duration_ms":`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in logs:\n%s", want, got)
		}
	}
}

func TestRefreshSuccessLogsDurationAtDebug(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	var logs bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	defer slog.SetDefault(prev)

	d := New(time.Minute, func(context.Context) (*models.ClusterSnapshot, error) {
		return &models.ClusterSnapshot{Status: models.SnapshotHealthy}, nil
	})
	d.SetSnapshotPath("")
	if err := d.Refresh(context.Background()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	got := logs.String()
	for _, want := range []string{`"msg":"daemon: refresh ok"`, `"level":"DEBUG"`, `"duration_ms":`, `"nodes":0`} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %s in logs:\n%s", want, got)
		}
	}
}
