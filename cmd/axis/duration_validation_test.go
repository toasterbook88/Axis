package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestCommandsRejectNonPositiveDurationFlags(t *testing.T) {
	tests := []struct {
		name    string
		cmd     func() *cobra.Command
		args    []string
		wantErr string
	}{
		{name: "summary watch", cmd: summaryCmd, args: []string{"--watch-interval", "0s"}, wantErr: "--watch-interval must be greater than zero"},
		{name: "status watch", cmd: statusCmd, args: []string{"--watch-interval", "-1s"}, wantErr: "--watch-interval must be greater than zero"},
		{name: "serve refresh", cmd: serveCmd, args: []string{"--refresh", "0s"}, wantErr: "--refresh must be greater than zero"},
		{name: "daemon refresh", cmd: daemonStartCmd, args: []string{"--refresh", "-1s"}, wantErr: "--refresh must be greater than zero"},
		{name: "reservations stale window", cmd: reservationsDoctorCmd, args: []string{"--stale-window", "0s"}, wantErr: "--stale-window must be greater than zero"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := tt.cmd()
			cmd.SetOut(&bytes.Buffer{})
			cmd.SetErr(&bytes.Buffer{})
			cmd.SetArgs(tt.args)

			err := cmd.Execute()
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}
