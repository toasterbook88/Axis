package mesh

import (
	"log/slog"
	"testing"
	"time"
)

func TestNewMesh(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)
	if m == nil {
		t.Fatal("New returned nil")
	}
	if len(m.peers) != 0 {
		t.Fatalf("expected 0 peers, got %d", len(m.peers))
	}
}

func TestTrust_UnknownPeer(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	err := m.Trust("nonexistent")
	if err == nil {
		t.Error("Trust should fail for unknown peer")
	}
}

func TestDetectFailures_SuspectAndDead(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.SuspectTimeout = 10 * time.Millisecond
	cfg.DeadTimeout = 30 * time.Millisecond
	m := New(self, cfg, slog.Default())

	staleTime := time.Now().Add(-20 * time.Millisecond)
	m.mu.Lock()
	m.peers["id-b"] = &Peer{
		Name:     "node-b",
		StableID: "id-b",
		State:    PeerVerified,
		Source:   "gossip",
		LastSeen: staleTime,
	}
	m.mu.Unlock()

	// First detection: should become suspect
	m.detectFailures()
	m.mu.RLock()
	state := m.peers["id-b"].State
	m.mu.RUnlock()
	if state != PeerSuspect {
		t.Errorf("expected suspect, got %s", state)
	}

	// Wait past dead timeout
	time.Sleep(35 * time.Millisecond)
	m.detectFailures()

	m.mu.RLock()
	_, exists := m.peers["id-b"]
	m.mu.RUnlock()
	if exists {
		t.Error("dead peer should have been evicted")
	}
}

func TestDetectFailures_SeedNodesExempt(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.SuspectTimeout = 1 * time.Millisecond
	cfg.DeadTimeout = 2 * time.Millisecond
	m := New(self, cfg, slog.Default())

	m.AddSeed(Peer{Name: "seed-b", Hostname: "10.0.0.2", StableID: "id-seed"})
	// Make it look stale
	m.mu.Lock()
	m.peers["id-seed"].LastSeen = time.Now().Add(-time.Hour)
	m.mu.Unlock()

	time.Sleep(5 * time.Millisecond)
	m.detectFailures()

	m.mu.RLock()
	_, exists := m.peers["id-seed"]
	m.mu.RUnlock()
	if !exists {
		t.Error("seed nodes should be exempt from eviction")
	}
}

func TestHMAC_EmptySecret(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.SharedSecret = ""
	m := New(self, cfg, nil)

	if !m.verifyHMAC([]byte("anything"), "") {
		t.Error("empty secret should always verify")
	}
}

func TestHMAC_ValidSecret(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.SharedSecret = "test-secret-key"
	m := New(self, cfg, nil)

	data := []byte("test-data")
	mac := m.computeHMAC(data)
	if mac == "" {
		t.Error("HMAC should not be empty with secret")
	}
	if !m.verifyHMAC(data, mac) {
		t.Error("valid HMAC should verify")
	}
	if m.verifyHMAC(data, "bad-mac") {
		t.Error("invalid HMAC should not verify")
	}
}
