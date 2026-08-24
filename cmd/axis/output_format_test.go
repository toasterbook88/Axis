package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestStructuredOutputCommandsRejectUnknownFormat(t *testing.T) {
	tests := []struct {
		name string
		cmd  func() *cobra.Command
		args []string
	}{
		{name: "reservations list", cmd: reservationsListCmd},
		{name: "reservations inspect", cmd: reservationsInspectCmd, args: []string{"reservation-id"}},
		{name: "reservations release", cmd: reservationsReleaseCmd, args: []string{"reservation-id"}},
		{name: "reservations doctor", cmd: reservationsDoctorCmd},
		{name: "observations list", cmd: observationsListCmd},
		{name: "observations inspect", cmd: observationsInspectCmd, args: []string{"observation-key"}},
		{name: "facts", cmd: factsCmd},
		{name: "mcp client list", cmd: mcpClientListCmd},
		{name: "mcp client tools", cmd: mcpClientToolsCmd},
		{name: "mcp client resources", cmd: mcpClientResourcesCmd},
		{name: "mcp client prompts", cmd: mcpClientPromptsCmd},
		{name: "placement explain", cmd: placementExplainCmd, args: []string{"intent"}},
		{name: "ai backends", cmd: aiBackendsCmd},
		{name: "ai roles", cmd: aiRolesCmd},
		{name: "ai route", cmd: aiRouteCmd, args: []string{"default"}},
		{name: "task place", cmd: taskPlaceCmd, args: []string{"intent"}},
		{name: "task context", cmd: taskContextCmd, args: []string{"intent"}},
		{name: "status", cmd: statusCmd},
		{name: "profile match", cmd: profileMatchCmd, args: []string{"intent"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmd()
			cmd.SilenceErrors = true
			cmd.SilenceUsage = true
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(append(append([]string{}, tt.args...), "--format", "bogus"))

			err := cmd.Execute()
			if err == nil {
				t.Fatal("expected invalid format error")
			}
			if got := err.Error(); !strings.Contains(got, `invalid --format "bogus"`) {
				t.Fatalf("error = %q", got)
			}
		})
	}
}

func TestOutputFormatValidationNormalizesCase(t *testing.T) {
	cmd := profileMatchCmd()
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"intent", "--format", "JSON"})

	if err := cmd.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	var payload profileMatchOutput
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("uppercase JSON format did not produce JSON: %v\noutput: %s", err, out.String())
	}
}
