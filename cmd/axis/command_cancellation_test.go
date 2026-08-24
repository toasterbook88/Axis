package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

func TestOperatorCommandsHonorCanceledContext(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T)
		cmd   func() commandWithArgs
	}{
		{
			name: "facts",
			setup: func(t *testing.T) {
				previous := collectLocalFacts
				collectLocalFacts = func(ctx context.Context, _ string) (*models.NodeFacts, error) {
					return nil, requireCanceledContext(t, ctx)
				}
				t.Cleanup(func() { collectLocalFacts = previous })
			},
			cmd: func() commandWithArgs { return commandWithArgs{command: factsCmd()} },
		},
		{
			name: "placement explain",
			setup: func(t *testing.T) {
				t.Cleanup(stubTaskLiveLoader(t, func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return nil, "", requireCanceledContext(t, ctx)
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: placementExplainCmd(), args: []string{"intent"}}
			},
		},
		{
			name: "summary",
			setup: func(t *testing.T) {
				t.Cleanup(stubStatusRuntimeLoader(t, func(ctx context.Context) (*runtimectx.Context, error) {
					return nil, requireCanceledContext(t, ctx)
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: summaryCmd(), args: []string{"--cached=false"}}
			},
		},
		{
			name: "status",
			setup: func(t *testing.T) {
				t.Cleanup(stubStatusLiveLoader(t, func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return nil, "", requireCanceledContext(t, ctx)
				}))
			},
			cmd: func() commandWithArgs { return commandWithArgs{command: statusCmd()} },
		},
		{
			name: "task place",
			setup: func(t *testing.T) {
				t.Cleanup(stubTaskLiveLoader(t, func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return nil, "", requireCanceledContext(t, ctx)
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: taskPlaceCmd(), args: []string{"intent"}}
			},
		},
		{
			name: "task context",
			setup: func(t *testing.T) {
				t.Cleanup(stubTaskLiveLoader(t, func(ctx context.Context) (*models.ClusterSnapshot, string, error) {
					return nil, "", requireCanceledContext(t, ctx)
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: taskContextCmd(), args: []string{"intent"}}
			},
		},
		{
			name: "task run",
			setup: func(t *testing.T) {
				previous := loadTaskRunRuntime
				loadTaskRunRuntime = func(ctx context.Context) (*runtimectx.Context, error) {
					return nil, requireCanceledContext(t, ctx)
				}
				t.Cleanup(func() { loadTaskRunRuntime = previous })
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: taskRunCmd(), args: []string{"--exec", "true"}}
			},
		},
		{
			name: "doctor",
			setup: func(t *testing.T) {
				previous := doctorProbeInstall
				doctorProbeInstall = func() DoctorCheck {
					t.Fatal("canceled doctor reached install probe")
					return DoctorCheck{}
				}
				t.Cleanup(func() { doctorProbeInstall = previous })
			},
			cmd: func() commandWithArgs { return commandWithArgs{command: doctorCmd()} },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			invocation := tt.cmd()
			ctx, cancel := context.WithCancel(context.Background())
			cancel()
			invocation.command.SetContext(ctx)
			invocation.command.SetOut(&bytes.Buffer{})
			invocation.command.SetErr(&bytes.Buffer{})
			invocation.command.SetArgs(invocation.args)
			if err := invocation.command.Execute(); !errors.Is(err, context.Canceled) {
				t.Fatalf("error = %v, want context cancellation", err)
			}
		})
	}
}

func TestReservationsCommandDoesNotFallbackAfterCancellation(t *testing.T) {
	t.Setenv("AXIS_HOME", t.TempDir())
	var requests int
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests++
	}))
	defer server.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	cmd := reservationsCmd()
	cmd.SetContext(ctx)
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--cache-addr", server.URL})
	if err := cmd.Execute(); !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context cancellation", err)
	}
	if requests != 0 {
		t.Fatalf("canceled reservations command made %d request(s)", requests)
	}
}

func requireCanceledContext(t *testing.T, ctx context.Context) error {
	t.Helper()
	if !errors.Is(ctx.Err(), context.Canceled) {
		t.Fatalf("context error = %v, want cancellation", ctx.Err())
	}
	return ctx.Err()
}
