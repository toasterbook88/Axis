package main

import (
	"fmt"
	"os"
)

// Exit codes for the AXIS CLI
const (
	ExitOK              = 0
	ExitErrGeneric      = 1
	ExitErrConfigLoad   = 2
	ExitErrNoNodesFit   = 3
	ExitErrCommandFail  = 4
	ExitErrContextWrite = 5
)

// ExitCodeError wraps an exit code so Cobra RunE handlers can return
// a specific exit code without calling os.Exit directly.
type ExitCodeError int

func (e ExitCodeError) Error() string {
	return fmt.Sprintf("exit code %d", int(e))
}

// ExitCode returns the integer exit code from an error if it is an ExitCodeError,
// or 1 for any other non-nil error, or 0 for nil.
func ExitCode(err error) int {
	if err == nil {
		return ExitOK
	}
	if code, ok := err.(ExitCodeError); ok {
		return int(code)
	}
	return ExitErrGeneric
}

// Fatal exits the program with the given code and prints an optional error message to stderr.
func Fatal(code int, format string, args ...interface{}) {
	if format != "" {
		fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	}
	os.Exit(code)
}
