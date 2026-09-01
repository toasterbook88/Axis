package publication

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/reservation"
	"github.com/toasterbook88/axis/internal/state"
)

func TestBuildRecordsAndFingerprintsEveryAuthority(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	observedAt := time.Unix(1_700_000_000, 0).UTC()
	assembledAt := observedAt.Add(5 * time.Second)
	facts := &models.ClusterSnapshot{
		Timestamp: observedAt,
		Nodes:     []models.NodeFacts{{Name: "node-a", Status: models.StatusComplete}},
		Freshness: &models.DiscoveryFreshness{Source: "udp-window", CompletedWindow: true},
	}
	ledger := reservation.NewLedger(reservation.DefaultLimits(), nil)
	ledger.SetNodeCapacity("node-a", 8192)
	st := &state.ClusterState{
		Version:   1,
		Nodes:     map[string]state.NodeState{"node-a": {ReservedMB: 512}},
		UpdatedAt: observedAt.Add(2 * time.Second),
	}

	first, err := Build(SourceLiveRuntime, assembledAt, facts, ledger.Entries(), true, st, errors.New("recovered state"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if !strings.HasPrefix(first.ID, "pub-") || first.Source != SourceLiveRuntime || !first.AssembledAt.Equal(assembledAt) {
		t.Fatalf("unexpected publication identity: %+v", first)
	}
	if !first.Facts.Available || !first.Facts.ObservedAt.Equal(observedAt) || !strings.HasPrefix(first.Facts.DiscoveryDigest, "sha256:") {
		t.Fatalf("unexpected facts evidence: %+v", first.Facts)
	}
	if !first.Ledger.Available || first.Ledger.EntryCount != 0 || !strings.HasPrefix(first.Ledger.Digest, "sha256:") {
		t.Fatalf("unexpected ledger evidence: %+v", first.Ledger)
	}
	if !first.State.Available || first.State.SchemaVersion != 1 || !first.State.UpdatedAt.Equal(st.UpdatedAt) || first.State.Warning != "recovered state" {
		t.Fatalf("unexpected state evidence: %+v", first.State)
	}

	if _, err := ledger.Reserve(reservation.Entry{ID: "exec-1", Node: "node-a", RAMMB: 256}); err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	second, err := Build(SourceLiveRuntime, assembledAt, facts, ledger.Entries(), true, st, nil)
	if err != nil {
		t.Fatalf("Build after reservation: %v", err)
	}
	if second.Ledger.EntryCount != 1 || second.Ledger.Digest == first.Ledger.Digest {
		t.Fatalf("ledger content change not reflected: before=%+v after=%+v", first.Ledger, second.Ledger)
	}
	if second.Facts.DiscoveryDigest != first.Facts.DiscoveryDigest || second.State.Digest != first.State.Digest {
		t.Fatal("unchanged component digests must remain stable")
	}
}

func TestBuildMakesUnavailableAuthorityExplicit(t *testing.T) {
	envelope, err := Build(SourceDaemonCache, time.Now().UTC(), nil, nil, false, nil, errors.New("state read failed"))
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if envelope.Facts.Available || envelope.Facts.Warning == "" {
		t.Fatalf("facts unavailability not explicit: %+v", envelope.Facts)
	}
	if envelope.Ledger.Available || envelope.Ledger.Warning == "" {
		t.Fatalf("ledger unavailability not explicit: %+v", envelope.Ledger)
	}
	if envelope.State.Available || envelope.State.Warning != "state read failed" {
		t.Fatalf("state unavailability not explicit: %+v", envelope.State)
	}
}

func TestBuildRejectsMissingPublicationIdentity(t *testing.T) {
	if _, err := Build("", time.Now().UTC(), &models.ClusterSnapshot{}, nil, false, nil, nil); err == nil {
		t.Fatal("expected missing source error")
	}
	if _, err := Build(SourceLiveRuntime, time.Time{}, &models.ClusterSnapshot{}, nil, false, nil, nil); err == nil {
		t.Fatal("expected missing assembly time error")
	}
}
