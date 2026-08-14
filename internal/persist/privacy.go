package persist

import (
	"fmt"
	"os"
	"path/filepath"
)

const (
	// PrivateDirMode is the maximum access granted to directories containing
	// AXIS runtime state. Those stores can contain commands, output, topology,
	// and operator-provided descriptions.
	PrivateDirMode os.FileMode = 0o700
	// PrivateFileMode is the maximum access granted to AXIS runtime files.
	PrivateFileMode os.FileMode = 0o600
)

// EnsurePrivateDir creates dir and tightens an existing directory that was
// created by an older AXIS release with group or other access.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, PrivateDirMode); err != nil {
		return err
	}
	if err := os.Chmod(dir, PrivateDirMode); err != nil {
		return fmt.Errorf("tighten private directory %s: %w", dir, err)
	}
	return nil
}

// OpenPrivateFile opens an AXIS runtime file and tightens an existing file
// before returning it to the caller.
func OpenPrivateFile(path string, flags int) (*os.File, error) {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, flags, PrivateFileMode)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(PrivateFileMode); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("tighten private file %s: %w", path, err)
	}
	return f, nil
}

// WritePrivateFileAtomic atomically replaces an AXIS runtime file with
// owner-only permissions.
func WritePrivateFileAtomic(path string, data []byte) error {
	if err := EnsurePrivateDir(filepath.Dir(path)); err != nil {
		return err
	}
	return WriteFileAtomic(path, data, PrivateFileMode)
}
