package state

import (
	"crypto/sha256"
	"encoding/json"
	"os"
	"time"
)

// SemanticFingerprint returns a stable on-disk fingerprint for state watcher
// use. It intentionally ignores fields that change during healthy execution
// heartbeats so daemon refreshes only fire on meaningful reservation/failure
// changes. If the file cannot be parsed, the raw file hash is returned so
// corrupt-state changes still trigger watcher activity.
func SemanticFingerprint(path string) ([sha256.Size]byte, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return [sha256.Size]byte{}, false, nil
		}
		return [sha256.Size]byte{}, false, err
	}

	rawSum := sha256.Sum256(data)
	semanticSum, err := semanticFingerprintBytes(data)
	if err != nil {
		return rawSum, true, nil
	}
	return semanticSum, true, nil
}

func semanticFingerprintBytes(data []byte) ([sha256.Size]byte, error) {
	var s ClusterState
	if err := json.Unmarshal(data, &s); err != nil {
		return [sha256.Size]byte{}, err
	}

	s.UpdatedAt = time.Time{}
	if len(s.Nodes) == 0 {
		s.Nodes = nil
	} else {
		nodes := make(map[string]NodeState, len(s.Nodes))
		for name, ns := range s.Nodes {
			ns.ExecHeartbeatAt = nil
			if len(ns.ExecReservationsMB) == 0 {
				ns.ExecReservationsMB = nil
			}
			if len(ns.ExecOwnerPID) == 0 {
				ns.ExecOwnerPID = nil
			}
			if len(ns.ExecOwnerSurface) == 0 {
				ns.ExecOwnerSurface = nil
			}
			if len(ns.ExecOwnerLabel) == 0 {
				ns.ExecOwnerLabel = nil
			}
			if len(ns.ExecOrigin) == 0 {
				ns.ExecOrigin = nil
			}
			if len(ns.ActiveExecs) == 0 {
				ns.ActiveExecs = nil
			}
			nodes[name] = ns
		}
		s.Nodes = nodes
	}
	if len(s.Decisions) == 0 {
		s.Decisions = nil
	} else {
		s.Decisions = append([]string(nil), s.Decisions...)
	}
	if len(s.Tombstones) == 0 {
		s.Tombstones = nil
	}
	if len(s.Failures) == 0 {
		s.Failures = nil
	}
	if len(s.Observations) == 0 {
		s.Observations = nil
	}

	canonical, err := json.Marshal(s)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	return sha256.Sum256(canonical), nil
}
