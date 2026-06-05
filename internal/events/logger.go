package events

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
)

var (
	logMu      sync.RWMutex
	logPathVal string
)

func defaultLogPath() string {
	if logPathVal != "" {
		return logPathVal
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "events.jsonl")
}

// SetLogPath overrides the log path, primarily for testing.
func SetLogPath(path string) {
	logMu.Lock()
	defer logMu.Unlock()
	logPathVal = path
}

// allocateSequence increments and returns a monotonic sequence number from event-sequence file under flock.
func allocateSequence() (uint64, error) {
	path := defaultLogPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return 0, err
	}

	lockPath := filepath.Join(dir, "event-sequence.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return 0, err
	}
	defer lockFile.Close()

	// Acquire exclusive lock (blocking)
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return 0, err
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}()

	seqPath := filepath.Join(dir, "event-sequence")
	seqFile, err := os.OpenFile(seqPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return 0, err
	}
	defer seqFile.Close()

	var seq uint64
	data, err := io.ReadAll(seqFile)
	if err == nil && len(data) > 0 {
		str := strings.TrimSpace(string(data))
		if parsed, err := strconv.ParseUint(str, 10, 64); err == nil {
			seq = parsed
		}
	}

	seq++

	if _, err := seqFile.Seek(0, 0); err != nil {
		return 0, err
	}
	if err := seqFile.Truncate(0); err != nil {
		return 0, err
	}
	if _, err := fmt.Fprintf(seqFile, "%d\n", seq); err != nil {
		return 0, err
	}
	_ = seqFile.Sync()

	return seq, nil
}

// appendEventToFile appends a single event to the JSONL log file with size-based rotation.
func appendEventToFile(evt Event) error {
	logMu.Lock()
	defer logMu.Unlock()

	path := defaultLogPath()
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}

	// 1. Perform atomic append write
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return err
	}

	data, err := json.Marshal(evt)
	if err != nil {
		_ = f.Close()
		return err
	}

	if _, err := f.Write(append(data, '\n')); err != nil {
		_ = f.Close()
		return err
	}
	_ = f.Close()

	// 2. Check rotation (limit to 10MB)
	if info, err := os.Stat(path); err == nil && info.Size() > 10*1024*1024 {
		if err := rotateLogsUnderLock(path); err != nil {
			slog.Error("failed to rotate events log", "error", err)
		}
	}

	return nil
}

func rotateLogsUnderLock(path string) error {
	dir := filepath.Dir(path)
	lockPath := filepath.Join(dir, "events.lock")
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0644)
	if err != nil {
		return err
	}
	defer lockFile.Close()

	// Acquire exclusive lock
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
	}()

	// Re-verify size under lock
	if info, err := os.Stat(path); err != nil || info.Size() <= 10*1024*1024 {
		return nil // Already rotated by another process
	}

	// Rotate: shift events.9.jsonl down to events.1.jsonl
	lastPath := filepath.Join(dir, "events.9.jsonl")
	_ = os.Remove(lastPath)

	for i := 8; i >= 1; i-- {
		src := filepath.Join(dir, fmt.Sprintf("events.%d.jsonl", i))
		dst := filepath.Join(dir, fmt.Sprintf("events.%d.jsonl", i+1))
		if _, err := os.Stat(src); err == nil {
			_ = os.Rename(src, dst)
		}
	}

	// Rename events.jsonl to events.1.jsonl
	dst := filepath.Join(dir, "events.1.jsonl")
	return os.Rename(path, dst)
}

// getRecentEventsFromFile reads up to the last N events from the logs in chronological order.
func getRecentEventsFromFile(limit int) ([]Event, error) {
	logMu.RLock()
	defer logMu.RUnlock()

	path := defaultLogPath()
	dir := filepath.Dir(path)
	base := filepath.Base(path)
	ext := filepath.Ext(base)
	prefix := base[:len(base)-len(ext)]

	var files []string
	if _, err := os.Stat(path); err == nil {
		files = append(files, path)
	}

	for i := 1; i <= 9; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%s.%d%s", prefix, i, ext))
		if _, err := os.Stat(p); err == nil {
			files = append(files, p)
		}
	}

	var allEvents []Event
	// Read oldest to newest
	for i := len(files) - 1; i >= 0; i-- {
		f, err := os.Open(files[i])
		if err != nil {
			continue
		}
		dec := json.NewDecoder(f)
		for {
			var evt Event
			if err := dec.Decode(&evt); err != nil {
				if err == io.EOF {
					break
				}
				slog.Error("failed decoding event", "path", files[i], "error", err)
				break
			}
			allEvents = append(allEvents, evt)
		}
		_ = f.Close()
	}

	if len(allEvents) > limit {
		allEvents = allEvents[len(allEvents)-limit:]
	}

	return allEvents, nil
}
