package reservation

import (
	"encoding/json"
	"os"
	"path/filepath"

	"github.com/toasterbook88/axis/internal/persist"
)

// Path returns the path to the ledger persistence file.
func Path() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".axis", "ledger.json")
}

// diskFormat represents the serialized ledger.
type diskFormat struct {
	Entries []*Entry `json:"entries"`
}

// Load reads the ledger from disk, replacing current entries.
func (l *Ledger) Load() error {
	l.mu.Lock()
	defer l.mu.Unlock()

	path := Path()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	var df diskFormat
	if err := json.Unmarshal(data, &df); err != nil {
		warnErr := persist.QuarantineCorruptFile(path, err)
		return warnErr
	}

	l.entries = make(map[string]*Entry)
	l.totalReserved = 0
	for _, e := range df.Entries {
		l.entries[e.ID] = e
		l.totalReserved += e.RAMMB
	}

	// Startup reconciliation pass
	reclaimed := l.reclaimLocked()
	if reclaimed > 0 {
		l.logger.Info("startup reconciliation complete", "reclaimed", reclaimed)
	}

	return nil
}

// Save writes the ledger to disk.
func (l *Ledger) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.saveLocked()
}

func (l *Ledger) saveLocked() error {
	path := Path()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}

	out := make([]*Entry, 0, len(l.entries))
	for _, e := range l.entries {
		out = append(out, e)
	}

	df := diskFormat{Entries: out}
	data, err := json.MarshalIndent(df, "", "  ")
	if err != nil {
		return err
	}
	return persist.WriteFileAtomic(path, data, 0o644)
}
