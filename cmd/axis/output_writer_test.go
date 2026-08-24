package main

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/runtimectx"
)

type rejectingOutputWriter struct {
	err error
}

type commandWithArgs struct {
	command *cobra.Command
	args    []string
}

func (w rejectingOutputWriter) Write([]byte) (int, error) {
	return 0, w.err
}

func TestPrintOutputPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	for _, format := range []string{"json", "yaml"} {
		t.Run(format, func(t *testing.T) {
			err := printOutput(rejectingOutputWriter{err: wantErr}, map[string]string{"status": "ok"}, format)
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want writer failure", err)
			}
		})
	}
}

func TestFactsTextPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	t.Cleanup(stubCollectLocalFacts(t, func(context.Context, string) (*models.NodeFacts, error) {
		return &models.NodeFacts{Name: "test-host", Status: models.StatusComplete}, nil
	}))

	cmd := factsCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestVersionCommandPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	cmd := versionCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestScriptsListPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	cmd := scriptsListCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestSummaryCommandPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	t.Cleanup(stubStatusRuntimeLoader(t, func(context.Context) (*runtimectx.Context, error) {
		return &runtimectx.Context{Snapshot: &models.ClusterSnapshot{}}, nil
	}))

	cmd := summaryCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"--cached=false"})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestStatusCommandPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	t.Cleanup(stubStatusLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{}, "live", nil
	}))

	cmd := statusCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestPlacementCommandPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	t.Cleanup(stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{}, "live", nil
	}))

	cmd := placementExplainCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"intent"})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestTaskPlaceCommandPropagatesWriterFailures(t *testing.T) {
	wantErr := errors.New("writer unavailable")
	t.Cleanup(stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{}, "live", nil
	}))

	cmd := taskPlaceCmd()
	cmd.SetOut(rejectingOutputWriter{err: wantErr})
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"intent"})
	if err := cmd.Execute(); !errors.Is(err, wantErr) {
		t.Fatalf("error = %v, want writer failure", err)
	}
}

func TestContextAndSkillsCommandsPropagateWriterFailures(t *testing.T) {
	t.Setenv("AXIS_HOME", t.TempDir())
	wantErr := errors.New("writer unavailable")

	tests := []struct {
		name string
		run  func(rejectingOutputWriter) error
	}{
		{
			name: "context show",
			run: func(w rejectingOutputWriter) error {
				cmd := contextCmd()
				cmd.SetOut(w)
				cmd.SetErr(&strings.Builder{})
				cmd.SetArgs([]string{"show"})
				return cmd.Execute()
			},
		},
		{
			name: "skills",
			run: func(w rejectingOutputWriter) error {
				cmd := skillsCmd()
				cmd.SetOut(w)
				cmd.SetErr(&strings.Builder{})
				return cmd.Execute()
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.run(rejectingOutputWriter{err: wantErr})
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want writer failure", err)
			}
		})
	}
}

func TestTaskContextUsesConfiguredOutputWriter(t *testing.T) {
	t.Setenv("AXIS_HOME", t.TempDir())
	restore := stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
		return &models.ClusterSnapshot{
			Nodes: []models.NodeFacts{nodeComplete("alpha", 4096, "low", "git")},
			Summary: models.ClusterSummary{
				TotalNodes:     1,
				TotalFreeRAMMB: 4096,
			},
		}, "live", nil
	})
	t.Cleanup(restore)

	var out strings.Builder
	cmd := taskContextCmd()
	cmd.SetOut(&out)
	cmd.SetErr(&strings.Builder{})
	cmd.SetArgs([]string{"analyze a git repo"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if got := out.String(); !strings.Contains(got, "AXIS CLUSTER CONTEXT") || !strings.Contains(got, "Best node: alpha") {
		t.Fatalf("configured output missing context block: %q", got)
	}
}

func TestReadCommandsUseConfiguredErrorWriter(t *testing.T) {
	wantErr := errors.New("snapshot unavailable")
	tests := []struct {
		name  string
		setup func(*testing.T)
		cmd   func() commandWithArgs
	}{
		{
			name: "placement",
			setup: func(t *testing.T) {
				t.Cleanup(stubTaskLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
					return nil, "", wantErr
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: placementExplainCmd(), args: []string{"intent"}}
			},
		},
		{
			name: "status",
			setup: func(t *testing.T) {
				t.Cleanup(stubStatusLiveLoader(t, func(context.Context) (*models.ClusterSnapshot, string, error) {
					return nil, "", wantErr
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: statusCmd()}
			},
		},
		{
			name: "summary",
			setup: func(t *testing.T) {
				t.Cleanup(stubStatusRuntimeLoader(t, func(context.Context) (*runtimectx.Context, error) {
					return nil, wantErr
				}))
			},
			cmd: func() commandWithArgs {
				return commandWithArgs{command: summaryCmd(), args: []string{"--cached=false"}}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.setup(t)
			invocation := tt.cmd()
			var errOut strings.Builder
			invocation.command.SetOut(&strings.Builder{})
			invocation.command.SetErr(&errOut)
			invocation.command.SetArgs(invocation.args)

			err := invocation.command.Execute()
			if !errors.Is(err, wantErr) {
				t.Fatalf("error = %v, want snapshot failure", err)
			}
			if got := errOut.String(); !strings.Contains(got, wantErr.Error()) {
				t.Fatalf("configured stderr missing diagnostic: %q", got)
			}
		})
	}
}
