package reservation

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
)

// DeviceHold is one device's reserved MiB. It is not Entry.VRAMMB and it is
// not added to node or cluster VRAM sums. GPUIndex has no omitempty: a nil
// index is a refusal, and 0 is a legal observed index that must round-trip.
// A past expiry is stored; Load drops it. Expiry does not stop a process.
type DeviceHold struct {
	ID        string    `json:"id"`
	Node      string    `json:"node"`
	GPUIndex  *int      `json:"gpu_index"`
	MiB       int64     `json:"mib"`
	Owner     string    `json:"owner"`
	ExpiresAt time.Time `json:"expires_at"`
}

func cloneDeviceHold(hold *DeviceHold) *DeviceHold {
	if hold == nil {
		return nil
	}
	cp := *hold
	if hold.GPUIndex != nil {
		idx := *hold.GPUIndex
		cp.GPUIndex = &idx
	}
	return &cp
}

// HoldDevice records one device hold. It does not look up a GPU fact and it
// does not spend the hold. The caller owns neither the stored index pointer
// nor the returned one.
func (l *Ledger) HoldDevice(hold DeviceHold) (*DeviceHold, error) {
	l.fileMu.Lock()
	defer l.fileMu.Unlock()

	wasLocked := l.lockFile != nil
	if !wasLocked {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := l.lockFileLocked(ctx); err != nil {
			return nil, err
		}
		defer l.unlockFileLocked()
	}

	var snap []*Entry
	var holds []*DeviceHold
	var stored *DeviceHold
	err := func() error {
		l.mu.Lock()
		defer l.mu.Unlock()

		hold.ID = strings.TrimSpace(hold.ID)
		hold.Node = strings.TrimSpace(hold.Node)
		hold.Owner = strings.TrimSpace(hold.Owner)
		if hold.ID == "" {
			return fmt.Errorf("reservation: device hold ID required")
		}
		if hold.Node == "" {
			return fmt.Errorf("reservation: device hold node required")
		}
		if hold.GPUIndex == nil {
			return fmt.Errorf("reservation: device hold GPU index required")
		}
		if hold.MiB <= 0 {
			return fmt.Errorf("reservation: device hold MiB must be > 0")
		}
		if hold.Owner == "" {
			return fmt.Errorf("reservation: device hold owner required")
		}
		if hold.ExpiresAt.IsZero() {
			return fmt.Errorf("reservation: device hold expiry required")
		}
		if l.deviceHolds == nil {
			l.deviceHolds = make(map[string]*DeviceHold)
		}
		if _, exists := l.entries[hold.ID]; exists {
			return fmt.Errorf("reservation: duplicate ID %q", hold.ID)
		}
		if _, exists := l.deviceHolds[hold.ID]; exists {
			return fmt.Errorf("reservation: duplicate ID %q", hold.ID)
		}
		stored = cloneDeviceHold(&hold)
		l.deviceHolds[stored.ID] = stored
		snap = l.snapshotEntriesLocked()
		holds = l.snapshotDeviceHoldsLocked()
		return nil
	}()
	if err != nil {
		return nil, err
	}
	if err := l.writeSnapshot(snap, holds); err != nil {
		l.logger.Error("failed to persist device hold", "error", err)
	}
	return cloneDeviceHold(stored), nil
}

// DeviceHolds returns a copy of every device hold, sorted by ID.
func (l *Ledger) DeviceHolds() []DeviceHold {
	l.mu.RLock()
	defer l.mu.RUnlock()
	snap := l.snapshotDeviceHoldsLocked()
	out := make([]DeviceHold, 0, len(snap))
	for _, hold := range snap {
		out = append(out, *hold)
	}
	return out
}

// snapshotDeviceHoldsLocked returns independent copies. The caller must hold l.mu.
func (l *Ledger) snapshotDeviceHoldsLocked() []*DeviceHold {
	out := make([]*DeviceHold, 0, len(l.deviceHolds))
	for _, hold := range l.deviceHolds {
		if cp := cloneDeviceHold(hold); cp != nil {
			out = append(out, cp)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func (l *Ledger) replaceDeviceHolds(holds []*DeviceHold) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.replaceDeviceHoldsLocked(holds)
}

func (l *Ledger) replaceDeviceHoldsLocked(holds []*DeviceHold) {
	l.deviceHolds = make(map[string]*DeviceHold, len(holds))
	for _, hold := range holds {
		cp := cloneDeviceHold(hold)
		if cp == nil || cp.ID == "" {
			continue
		}
		l.deviceHolds[cp.ID] = cp
	}
}

// dropExpiredDeviceHoldsLocked removes holds whose expiry is before now.
// A zero expiry is kept. Equal-to-now is kept. The caller must hold l.mu.
func (l *Ledger) dropExpiredDeviceHoldsLocked() int {
	now := l.now().UTC()
	dropped := 0
	for id, hold := range l.deviceHolds {
		if hold == nil || hold.ExpiresAt.IsZero() {
			continue
		}
		if now.After(hold.ExpiresAt) {
			delete(l.deviceHolds, id)
			dropped++
		}
	}
	return dropped
}
