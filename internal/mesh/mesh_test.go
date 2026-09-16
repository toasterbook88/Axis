package mesh

import (
	"context"
	"log/slog"
	"sync"
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

func TestAddSeed(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	seed := Peer{Name: "node-b", Hostname: "10.0.0.2", StableID: "id-b"}
	m.AddSeed(seed)

	peers := m.Peers()
	if len(peers) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(peers))
	}
	if peers[0].State != PeerTrusted {
		t.Errorf("seed should be trusted, got %s", peers[0].State)
	}
	if peers[0].Source != "config" {
		t.Errorf("seed source should be config, got %s", peers[0].Source)
	}
}

func TestMergePeer_NeverDemoteTrusted(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	seed := Peer{Name: "node-b", Hostname: "10.0.0.2", StableID: "id-b"}
	m.AddSeed(seed)

	// Simulate gossip with lower state — should not demote.
	m.mu.Lock()
	m.mergePeer(Peer{
		Name:     "node-b",
		Hostname: "10.0.0.2",
		StableID: "id-b",
		State:    PeerDiscovered,
	}, time.Now())
	m.mu.Unlock()

	peers := m.Peers()
	if peers[0].State != PeerTrusted {
		t.Errorf("trusted peer should not be demoted, got %s", peers[0].State)
	}
}

func TestMergePeer_IgnoresSelf(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	m.mu.Lock()
	m.mergePeer(self, time.Now())
	m.mu.Unlock()

	if len(m.Peers()) != 0 {
		t.Error("should not add self to peer list")
	}
}

func TestMergePeer_RespectsMaxPeers(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.MaxPeers = 2
	m := New(self, cfg, nil)

	m.mu.Lock()
	m.mergePeer(Peer{Name: "b", Hostname: "10.0.0.2", StableID: "id-b"}, time.Now())
	m.mergePeer(Peer{Name: "c", Hostname: "10.0.0.3", StableID: "id-c"}, time.Now())
	m.mergePeer(Peer{Name: "d", Hostname: "10.0.0.4", StableID: "id-d"}, time.Now()) // should be dropped
	m.mu.Unlock()

	if len(m.Peers()) != 2 {
		t.Errorf("expected 2 peers (MaxPeers cap), got %d", len(m.Peers()))
	}
}

func TestTrust(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	m.mu.Lock()
	m.mergePeer(Peer{Name: "node-b", Hostname: "10.0.0.2", StableID: "id-b"}, time.Now())
	m.mu.Unlock()

	err := m.Trust("id-b")
	if err != nil {
		t.Fatalf("Trust failed: %v", err)
	}
	peers := m.Peers()
	if peers[0].State != PeerTrusted {
		t.Errorf("expected trusted, got %s", peers[0].State)
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

func TestActivePeers(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	m.mu.Lock()
	m.peers["id-b"] = &Peer{Name: "node-b", StableID: "id-b", State: PeerTrusted}
	m.peers["id-c"] = &Peer{Name: "node-c", StableID: "id-c", State: PeerDiscovered}
	m.peers["id-d"] = &Peer{Name: "node-d", StableID: "id-d", State: PeerVerified}
	m.peers["id-e"] = &Peer{Name: "node-e", StableID: "id-e", State: PeerDead}
	m.mu.Unlock()

	active := m.ActivePeers()
	if len(active) != 2 {
		t.Errorf("expected 2 active peers, got %d", len(active))
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

func TestOnPeerJoinCallback(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	m := New(self, DefaultConfig(), nil)

	var mu sync.Mutex
	var joined []string
	joinedCh := make(chan struct{}, 1)
	m.OnPeerJoin = func(p Peer) {
		mu.Lock()
		joined = append(joined, p.Name)
		mu.Unlock()
		select {
		case joinedCh <- struct{}{}:
		default:
		}
	}

	m.mu.Lock()
	m.mergePeer(Peer{Name: "new-node", Hostname: "10.0.0.5", StableID: "id-new"}, time.Now())
	m.mu.Unlock()

	select {
	case <-joinedCh:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for OnPeerJoin callback")
	}
	mu.Lock()
	defer mu.Unlock()
	if len(joined) != 1 || joined[0] != "new-node" {
		t.Errorf("expected OnPeerJoin for 'new-node', got %v", joined)
	}
}

func TestPeerState_String(t *testing.T) {
	tests := []struct {
		state PeerState
		want  string
	}{
		{PeerDiscovered, "discovered"},
		{PeerVerified, "verified"},
		{PeerTrusted, "trusted"},
		{PeerSuspect, "suspect"},
		{PeerDead, "dead"},
		{PeerState(99), "unknown"},
	}
	for _, tt := range tests {
		if got := tt.state.String(); got != tt.want {
			t.Errorf("PeerState(%d).String() = %q, want %q", tt.state, got, tt.want)
		}
	}
}

func TestStartStop(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "127.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.ListenAddr = ":0" // random port
	cfg.GossipInterval = 50 * time.Millisecond
	m := New(self, cfg, slog.Default())

	ctx := context.Background()
	err := m.Start(ctx)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}

	// Let it run briefly
	time.Sleep(100 * time.Millisecond)
	m.Stop()
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

func TestHMAC_VerifyMessageWithEmbeddedMAC(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SharedSecret = "test-secret-key"
	m := New(Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}, cfg, nil)

	msg := gossipMessage{
		Type:      "peer-list",
		Sender:    Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a", Generation: 2},
		Peers:     []Peer{{Name: "node-b", Hostname: "10.0.0.2", StableID: "id-b", Generation: 1}},
		Timestamp: time.Now().UnixMilli(),
		Nonce:     "nonce-1",
	}
	msg.HMAC = m.computeMessageHMAC(msg)
	if !m.verifyMessageHMAC(msg) {
		t.Fatal("expected message HMAC to verify")
	}

	msg.Nonce = "tampered"
	if m.verifyMessageHMAC(msg) {
		t.Fatal("expected tampered message HMAC verification to fail")
	}
}

func TestSelectFanOut_LessThanFanOut(t *testing.T) {
	self := Peer{Name: "node-a", Hostname: "10.0.0.1", StableID: "id-a"}
	cfg := DefaultConfig()
	cfg.FanOut = 5
	m := New(self, cfg, nil)

	m.mu.Lock()
	m.peers["id-b"] = &Peer{Name: "b", Hostname: "10.0.0.2", StableID: "id-b", State: PeerVerified}
	m.peers["id-c"] = &Peer{Name: "c", Hostname: "10.0.0.3", StableID: "id-c", State: PeerVerified}
	m.mu.Unlock()

	m.mu.RLock()
	targets := m.selectFanOut()
	m.mu.RUnlock()
	if len(targets) != 2 {
		t.Errorf("with fewer peers than fan_out, should return all: got %d", len(targets))
	}
}
