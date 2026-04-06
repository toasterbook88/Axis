//go:build !darwin && !linux

package state

func processAlive(pid int) bool {
	return pid > 0
}
