//go:build !darwin

package facts

import "io/fs"

func statFlags(fs.FileInfo) uint32 { return 0 }

// entryDataless skips the per-directory lstat where there are no placeholders.
func entryDataless(fs.DirEntry) bool { return false }
