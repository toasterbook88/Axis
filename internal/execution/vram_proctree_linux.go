//go:build linux

package execution

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// taskProcessTree returns root and every live descendant, read from /proc.
var taskProcessTree = func(root int) map[int]bool {
	return processTreeFromProc("/proc", root)
}

func processTreeFromProc(procRoot string, root int) map[int]bool {
	children := make(map[int][]int)
	entries, err := os.ReadDir(procRoot)
	if err == nil {
		for _, e := range entries {
			pid, err := strconv.Atoi(e.Name())
			if err != nil {
				continue
			}
			stat, err := os.ReadFile(filepath.Join(procRoot, e.Name(), "stat"))
			if err != nil {
				continue
			}
			if ppid, ok := parentPIDFromStat(string(stat)); ok {
				children[ppid] = append(children[ppid], pid)
			}
		}
	}
	tree := map[int]bool{root: true}
	queue := []int{root}
	for len(queue) > 0 {
		pid := queue[0]
		queue = queue[1:]
		for _, child := range children[pid] {
			if !tree[child] {
				tree[child] = true
				queue = append(queue, child)
			}
		}
	}
	return tree
}

// parentPIDFromStat reads the ppid field of /proc/<pid>/stat. The command
// name is parenthesised and may contain spaces or parentheses, so fields are
// counted from the last ')'.
func parentPIDFromStat(stat string) (int, bool) {
	i := strings.LastIndexByte(stat, ')')
	if i < 0 {
		return 0, false
	}
	fields := strings.Fields(stat[i+1:])
	if len(fields) < 2 {
		return 0, false
	}
	ppid, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return ppid, true
}
