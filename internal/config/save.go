package config

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"gopkg.in/yaml.v3"
)

// SaveResult describes the outcome of an atomic configuration write.
type SaveResult struct {
	Changed    bool
	BackupPath string
}

// SaveAtomic validates cfg before mutation, skips semantically identical writes,
// preserves the previous file as a timestamped backup, and atomically replaces
// path with mode 0600.
func SaveAtomic(path string, cfg *Config) (SaveResult, error) {
	if cfg == nil {
		return SaveResult{}, errors.New("config: cannot save nil configuration")
	}
	if err := cfg.MigrateProviders(); err != nil {
		return SaveResult{}, err
	}
	if err := cfg.Validate(); err != nil {
		return SaveResult{}, err
	}

	if existing, err := Load(path); err == nil && reflect.DeepEqual(existing, cfg) {
		return SaveResult{}, nil
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return SaveResult{}, fmt.Errorf("marshalling config: %w", err)
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return SaveResult{}, fmt.Errorf("creating config directory: %w", err)
	}
	if err := os.Chmod(dir, 0700); err != nil && !errors.Is(err, os.ErrPermission) {
		return SaveResult{}, fmt.Errorf("securing config directory: %w", err)
	}

	result := SaveResult{Changed: true}
	tmp, err := os.CreateTemp(dir, ".nodes.yaml.tmp-*")
	if err != nil {
		return SaveResult{}, fmt.Errorf("creating temporary config: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}

	if err := tmp.Chmod(0600); err != nil {
		cleanup()
		return SaveResult{}, fmt.Errorf("securing temporary config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		cleanup()
		return SaveResult{}, fmt.Errorf("writing temporary config: %w", err)
	}
	if err := tmp.Sync(); err != nil {
		cleanup()
		return SaveResult{}, fmt.Errorf("syncing temporary config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return SaveResult{}, fmt.Errorf("closing temporary config: %w", err)
	}

	if old, err := os.Open(path); err == nil {
		result.BackupPath = nextBackupPath(path, time.Now().UTC())
		copyErr := copyFileSecure(old, result.BackupPath)
		closeErr := old.Close()
		if copyErr != nil {
			_ = os.Remove(tmpPath)
			return SaveResult{}, fmt.Errorf("backing up config: %w", copyErr)
		}
		if closeErr != nil {
			_ = os.Remove(tmpPath)
			return SaveResult{}, fmt.Errorf("closing existing config: %w", closeErr)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		_ = os.Remove(tmpPath)
		return SaveResult{}, fmt.Errorf("opening existing config: %w", err)
	}

	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return SaveResult{}, fmt.Errorf("replacing config: %w", err)
	}

	if dirHandle, err := os.Open(dir); err == nil {
		_ = dirHandle.Sync()
		_ = dirHandle.Close()
	}
	return result, nil
}

func copyFileSecure(src io.Reader, dst string) error {
	out, err := os.OpenFile(dst, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return err
	}
	ok := false
	defer func() {
		_ = out.Close()
		if !ok {
			_ = os.Remove(dst)
		}
	}()
	if _, err := io.Copy(out, src); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	ok = true
	return nil
}

func nextBackupPath(path string, now time.Time) string {
	base := path + ".bak-" + now.Format("20060102T150405Z")
	candidate := base
	// Cap iterations so a permission error (or other non-NotExist Stat
	// failure) cannot hang SaveAtomic forever. Any Stat error other than
	// "exists" means the path is usable for a create attempt; the subsequent
	// O_EXCL write will surface real filesystem problems.
	for i := 1; i <= 1000; i++ {
		_, err := os.Stat(candidate)
		if err != nil {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, i)
	}
	return fmt.Sprintf("%s-%d", base, now.UnixNano())
}
