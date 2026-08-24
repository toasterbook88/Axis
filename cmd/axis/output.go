package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

const machineOutputSchemaV1 = "axis.output/v1"

type machineWarning struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// machineOutput is the stable outer contract for command output intended for
// automation. Command-specific payloads live under data so they can evolve
// without colliding with envelope metadata.
type machineOutput struct {
	SchemaVersion string           `json:"schema_version"`
	Command       string           `json:"command"`
	OK            bool             `json:"ok"`
	Status        string           `json:"status"`
	Data          any              `json:"data"`
	Warnings      []machineWarning `json:"warnings"`
}

func writeMachineOutput(out io.Writer, command, status string, ok bool, data any, warnings []machineWarning) error {
	if warnings == nil {
		warnings = []machineWarning{}
	}
	return json.NewEncoder(out).Encode(machineOutput{
		SchemaVersion: machineOutputSchemaV1,
		Command:       command,
		OK:            ok,
		Status:        status,
		Data:          data,
		Warnings:      warnings,
	})
}

func validateOutputFormat(format *string, allowed ...string) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		normalized := strings.ToLower(strings.TrimSpace(*format))
		for _, candidate := range allowed {
			if normalized == candidate {
				*format = normalized
				return nil
			}
		}
		return fmt.Errorf("invalid --format %q (expected one of: %s)", *format, strings.Join(allowed, ", "))
	}
}

func validatePositiveDuration(flagName string, value *time.Duration) func(*cobra.Command, []string) error {
	return func(_ *cobra.Command, _ []string) error {
		if *value <= 0 {
			return fmt.Errorf("--%s must be greater than zero", flagName)
		}
		return nil
	}
}
