//go:build !linux && !darwin

package main

import "errors"

// probeHardware has no independent reading on this operating system, so the
// facts.hardware checker can only report inconclusive here.
func probeHardware() (hardwareReading, string, error) {
	return hardwareReading{}, "", errors.New("facts.hardware has no independent probe on this operating system")
}
