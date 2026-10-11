package facts

import (
	"io/fs"
	"syscall"
)

func statFlags(info fs.FileInfo) uint32 {
	if st, ok := info.Sys().(*syscall.Stat_t); ok {
		return st.Flags
	}
	return 0
}

func entryDataless(d fs.DirEntry) bool {
	info, err := d.Info()
	return err == nil && isDataless(info)
}
