package persist

import (
	"fmt"
	"os"
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
