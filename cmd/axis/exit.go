package main

import (
	"errors"
	"fmt"
)

// Exit codes for the AXIS CLI
const (
	ExitOK               = 0
	ExitErrGeneric       = 1
	ExitErrConfigLoad    = 2
	ExitErrNoNodesFit    = 3
	ExitErrCommandFail   = 4
	ExitErrContextWrite  = 5
	ExitErrIO            = 6
	ExitErrModelUnlisted = 7 // axis ai route: model not listed on healthy backends
)

// ExitCodeError wraps an exit code and a user-facing message so Cobra
// RunE handlers can return a specific exit code without calling os.Exit directly.
// The message is printed by the handler before returning; Cobra's own error
// printing is silenced via SilenceErrors on the root command.
type ExitCodeError struct {
	Code    int
	Message string
}

func (e ExitCodeError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	return fmt.Sprintf("exit code %d", e.Code)
}

// ExitCode returns the integer exit code from an error if it is an ExitCodeError,
// or 1 for any other non-nil error, or 0 for nil. Uses errors.As to unwrap.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	var codeErr ExitCodeError
	if errors.As(err, &codeErr) {
		return codeErr.Code
	}
	return ExitErrGeneric
}
