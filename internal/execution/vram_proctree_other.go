//go:build !linux

package execution

// taskProcessTree returns only root: descendants are not enumerated off
// Linux, where nvidia-smi per-process accounting is not expected.
var taskProcessTree = func(root int) map[int]bool {
	return map[int]bool{root: true}
}
