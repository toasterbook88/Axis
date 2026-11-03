package mesh

import (
	"reflect"
	"testing"
	"time"
)

func TestReconcileSeedsPreservesObservationsAndLearnedPeers(t *testing.T) {
	m := New(Peer{Name: "local"}, DefaultConfig(), nil)
	seed := Peer{Name: "alpha", Hostname: "old.example", Port: 12345, StableID: "alpha-id"}
	m.AddSeed(seed)
	m.mu.Lock()
	m.peers["alpha-id"].State = PeerDead
	m.peers["alpha-id"].MissedPings = 9
	m.mergePeer(Peer{Name: "learned", Hostname: "learned.example", Port: 12345}, time.Now())
	m.mu.Unlock()
	m.mu.Lock()
	m.mergePeer(Peer{Name: "alpha", Hostname: "untrusted.example", Port: 54321, StableID: "alpha-id", Generation: 100}, time.Now())
	m.mu.Unlock()
	if got := m.Peers()[0]; got.Hostname != "old.example" || got.Port != 12345 {
		t.Fatalf("gossip overwrote configured endpoint: %+v", got)
	}
	before := m.Peers()
	if err := m.ReconcileSeeds([]Peer{seed}); err != nil {
		t.Fatal(err)
	}
	if got := m.Peers(); !reflect.DeepEqual(got, before) {
		t.Fatalf("unchanged peers lost observations: %+v", got)
	}
	seed.Hostname = "new.example"
	seed.StableID = "new-id"
	if err := m.ReconcileSeeds([]Peer{seed}); err != nil {
		t.Fatal(err)
	}
	peers := m.Peers()
	if len(peers) != 2 || peers[0].Hostname != "new.example" || peers[0].StableID != "new-id" || peers[0].State != PeerTrusted || peers[0].MissedPings != 0 || peers[1].Name != "learned" {
		t.Fatalf("reconciled peers=%+v", peers)
	}
	if err := m.ReconcileSeeds(nil); err != nil {
		t.Fatal(err)
	}
	if got := m.Peers(); len(got) != 1 || got[0].Name != "learned" {
		t.Fatalf("removed seeds also removed learned peers: %+v", got)
	}
}

func TestReconcileSeedsRejectsConflictsAtomically(t *testing.T) {
	m := New(Peer{Name: "local", StableID: "local-id"}, DefaultConfig(), nil)
	seed := Peer{Name: "alpha", Hostname: "alpha.example", Port: 12345, StableID: "alpha-id"}
	m.AddSeed(seed)
	for _, invalid := range []Peer{
		{Name: "ALPHA", Hostname: "other.example", Port: 12345},
		{Name: "beta", Hostname: "other.example", Port: 12345, StableID: "alpha-id"},
		{Name: "local", Hostname: "other.example", Port: 12345},
		{Name: "beta", Hostname: "other.example", Port: 12345, StableID: "local-id"},
		{Name: "beta", Hostname: "other.example", Port: 65536},
	} {
		before := m.Peers()
		if err := m.ReconcileSeeds([]Peer{seed, invalid}); err == nil {
			t.Errorf("conflicting/invalid peer accepted: %+v", invalid)
		}
		if got := m.Peers(); !reflect.DeepEqual(got, before) {
			t.Errorf("invalid batch changed peers: %+v", got)
		}
	}
}

func TestReconcileSeedsConfiguredIdentityWinsOverGossip(t *testing.T) {
	m := New(Peer{}, DefaultConfig(), nil)
	m.mu.Lock()
	m.mergePeer(Peer{Name: "alpha", Hostname: "learned.example", StableID: "learned-id", Port: 12345}, time.Now())
	m.mergePeer(Peer{Name: "other", Hostname: "other.example", StableID: "config-id", Port: 12345}, time.Now())
	m.mu.Unlock()
	if err := m.ReconcileSeeds([]Peer{{Name: "alpha", Hostname: "config.example", StableID: "config-id", Port: 12345}}); err != nil {
		t.Fatal(err)
	}
	if peers := m.Peers(); len(peers) != 1 || peers[0].Source != "config" || peers[0].Hostname != "config.example" {
		t.Fatalf("gossip identity conflict survived: %+v", peers)
	}
}
