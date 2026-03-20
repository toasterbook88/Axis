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

// Fatal exits the program with the given code and prints an optional error message to stderr.
func Fatal(code int, format string, args ...interface{}) {
	if format != "" {
		fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	}
	os.Exit(code)
}
