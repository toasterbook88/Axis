//go:build !linux

package main

func probeAdvisoryLockHeld(string) (bool, error) {
	return false, errOwnershipProbeUnsupported
}
