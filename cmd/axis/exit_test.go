package main

import (
	"errors"
	"fmt"
	"testing"
)

func TestExitCodeError_Error(t *testing.T) {
	tests := []struct {
		name string
		err  ExitCodeError
		want string
	}{
		{
			name: "with message",
			err:  ExitCodeError{Code: ExitErrConfigLoad, Message: "config not found"},
			want: "config not found",
		},
		{
			name: "without message",
			err:  ExitCodeError{Code: ExitErrNoNodesFit},
			want: fmt.Sprintf("exit code %d", ExitErrNoNodesFit),
		},
		{
			name: "empty message",
			err:  ExitCodeError{Code: ExitErrCommandFail, Message: ""},
			want: fmt.Sprintf("exit code %d", ExitErrCommandFail),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.err.Error()
			if got != tt.want {
				t.Errorf("ExitCodeError.Error() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestExitCode(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want int
	}{
		{
			name: "nil error",
			err:  nil,
			want: ExitOK,
		},
		{
			name: "ExitCodeError direct",
			err:  ExitCodeError{Code: ExitErrConfigLoad, Message: "config missing"},
			want: ExitErrConfigLoad,
		},
		{
			name: "wrapped ExitCodeError",
			err:  fmt.Errorf("wrapped: %w", ExitCodeError{Code: ExitErrNoNodesFit, Message: "no fit"}),
			want: ExitErrNoNodesFit,
		},
		{
			name: "plain error",
			err:  errors.New("something went wrong"),
			want: ExitErrGeneric,
		},
		{
			name: "wrapped plain error",
			err:  fmt.Errorf("outer: %w", errors.New("inner")),
			want: ExitErrGeneric,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExitCode(tt.err)
			if got != tt.want {
				t.Errorf("ExitCode(%v) = %d, want %d", tt.err, got, tt.want)
			}
		})
	}
}
