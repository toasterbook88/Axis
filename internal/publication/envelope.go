// Package publication is INTERNAL-ONLY — evidence envelopes for assembled snapshots.
package publication

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/state"
)

const (
	SourceLiveRuntime = "live-runtime"
	SourceDaemonCache = "daemon-cache"
)

// Build constructs immutable evidence for one snapshot assembly. facts must be
// the snapshot before reservation overlays are applied.
func Build(source string, assembledAt time.Time, facts *models.ClusterSnapshot, ledgerEntries []reservation.Entry, ledgerAvailable bool, st *state.ClusterState, stateWarning error) (*models.PublicationEnvelope, error) {
	if strings.TrimSpace(source) == "" {
		return nil, fmt.Errorf("publication source is required")
	}
	if assembledAt.IsZero() {
		return nil, fmt.Errorf("publication assembly time is required")
	}

	envelope := &models.PublicationEnvelope{
		ID:          models.GenerateID("pub"),
		Source:      source,
		AssembledAt: assembledAt.UTC(),
	}

	if facts == nil {
		envelope.Facts.Warning = "facts authority unavailable"
	} else {
		digest, err := digestJSON(struct {
			Nodes     []models.NodeFacts         `json:"nodes"`
			Freshness *models.DiscoveryFreshness `json:"freshness,omitempty"`
		}{Nodes: facts.Nodes, Freshness: facts.Freshness})
		if err != nil {
			return nil, fmt.Errorf("fingerprint facts: %w", err)
		}
		envelope.Facts = models.PublicationFactsEvidence{
			Available:       true,
			ObservedAt:      facts.Timestamp,
			DiscoveryDigest: digest,
		}
		if facts.Timestamp.IsZero() {
			envelope.Facts.Warning = "facts observation timestamp unavailable"
		}
	}

	if !ledgerAvailable {
		envelope.Ledger.Warning = "reservation ledger authority unavailable"
	} else {
		entries := append([]reservation.Entry(nil), ledgerEntries...)
		sort.Slice(entries, func(i, j int) bool { return entries[i].ID < entries[j].ID })
		digest, err := digestJSON(entries)
		if err != nil {
			return nil, fmt.Errorf("fingerprint reservation ledger: %w", err)
		}
		envelope.Ledger = models.PublicationLedgerEvidence{
			Available:  true,
			Digest:     digest,
			EntryCount: len(entries),
		}
	}

	if st == nil {
		envelope.State.Warning = "state authority unavailable"
		if stateWarning != nil {
			envelope.State.Warning = stateWarning.Error()
		}
	} else {
		digest, err := digestJSON(st)
		if err != nil {
			return nil, fmt.Errorf("fingerprint state: %w", err)
		}
		envelope.State = models.PublicationStateEvidence{
			Available:     true,
			Digest:        digest,
			SchemaVersion: st.Version,
			UpdatedAt:     st.UpdatedAt,
		}
		if stateWarning != nil {
			envelope.State.Warning = stateWarning.Error()
		}
	}

	return envelope, nil
}

func digestJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}
