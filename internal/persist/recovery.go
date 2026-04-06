package persist

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// RecoveryWarning reports that a corrupt local persistence file was quarantined
// and the caller can continue with an empty in-memory store.
type RecoveryWarning struct {
	Path       string
	BackupPath string
	Cause      error
}

func (w *RecoveryWarning) Error() string {
	return fmt.Sprintf("quarantined corrupt file %s to %s: %v", w.Path, w.BackupPath, w.Cause)
}

// QuarantineCorruptFile renames a corrupt file aside so the caller can recover
// with a clean in-memory value. Rename failures are returned as hard errors.
func QuarantineCorruptFile(path string, cause error) error {
	backupPath := fmt.Sprintf("%s.corrupt-%s", path, time.Now().UTC().Format("20060102T150405Z"))
	if err := os.Rename(path, backupPath); err != nil {
		return fmt.Errorf("rename corrupt file %s: %w", path, err)
	}
	return &RecoveryWarning{
		Path:       path,
		BackupPath: backupPath,
		Cause:      cause,
	}
}

// WriteFileAtomic writes data to path via a same-directory temporary file and
// rename, so readers never observe a partially-written persistence file.
func WriteFileAtomic(path string, data []byte, perm os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tmpPath := tmp.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}
