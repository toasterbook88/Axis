// Package mesh is SCAFFOLDED — gossip-based peer discovery scaffolding.
// Not wired into the stable operator path.
package mesh

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"net"
	"sort"
	"sync"
	"time"
)

// PeerState tracks a discovered peer's lifecycle.
type PeerState int

const (
	// PeerDiscovered means we heard about this node but haven't verified it.
	PeerDiscovered PeerState = iota
	// PeerVerified means SSH probe succeeded at least once.
	PeerVerified
	// PeerTrusted means the operator explicitly promoted this node.
	PeerTrusted
	// PeerSuspect means the node missed consecutive heartbeats.
	PeerSuspect
	// PeerDead means the node has been unreachable beyond the eviction window.
	PeerDead
)

func (s PeerState) String() string {
	switch s {
	case PeerDiscovered:
		return "discovered"
	case PeerVerified:
		return "verified"
	case PeerTrusted:
		return "trusted"
	case PeerSuspect:
		return "suspect"
	case PeerDead:
		return "dead"
	default:
		return "unknown"
	}
}

func (s PeerState) MarshalJSON() ([]byte, error) {
	return json.Marshal(s.String())
}

func (s *PeerState) UnmarshalJSON(data []byte) error {
	var str string
	if err := json.Unmarshal(data, &str); err != nil {
		return err
	}
	switch str {
	case "discovered":
		*s = PeerDiscovered
	case "verified":
		*s = PeerVerified
	case "trusted":
		*s = PeerTrusted
	case "suspect":
		*s = PeerSuspect
	case "dead":
		*s = PeerDead
	default:
		*s = PeerState(-1)
	}
	return nil
}

// Peer represents a node in the mesh.
type Peer struct {
	Name        string    `json:"name"`
	Hostname    string    `json:"hostname"`
	Port        int       `json:"port"`
	StableID    string    `json:"stable_id"`
	State       PeerState `json:"state"`
	Source      string    `json:"source"` // "config", "gossip", "mdns", "peer-exchange"
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	MissedPings int       `json:"missed_pings"`
	Generation  uint64    `json:"generation"` // Lamport-style monotonic counter
}

// Config controls mesh behavior.
type Config struct {
	// ListenAddr is the UDP address for gossip (default ":42426").
	ListenAddr string `yaml:"listen_addr" json:"listen_addr"`
	// GossipInterval is how often we broadcast our peer list.
	GossipInterval time.Duration `yaml:"gossip_interval" json:"gossip_interval"`
	// SuspectTimeout is how long before a silent peer becomes suspect.
	SuspectTimeout time.Duration `yaml:"suspect_timeout" json:"suspect_timeout"`
	// DeadTimeout is how long a suspect peer stays before eviction.
	DeadTimeout time.Duration `yaml:"dead_timeout" json:"dead_timeout"`
	// MaxPeers caps the mesh to prevent unbounded growth.
	MaxPeers int `yaml:"max_peers" json:"max_peers"`
	// SharedSecret is the HMAC key for gossip authentication.
	SharedSecret string `yaml:"shared_secret" json:"shared_secret"`
	// FanOut is how many random peers receive each gossip round.
	FanOut int `yaml:"fan_out" json:"fan_out"`
}

// DefaultConfig returns production defaults.
func DefaultConfig() Config {
	return Config{
		ListenAddr:     ":42426",
		GossipInterval: 5 * time.Second,
		SuspectTimeout: 15 * time.Second,
		DeadTimeout:    60 * time.Second,
		MaxPeers:       64,
		FanOut:         3,
	}
}

// Mesh manages peer discovery and gossip protocol.
type Mesh struct {
	mu         sync.RWMutex
	peers      map[string]*Peer // key: stable_id or hostname
	self       Peer
	cfg        Config
	generation uint64
	conn       *net.UDPConn
	logger     *slog.Logger
	cancel     context.CancelFunc
	wg         sync.WaitGroup

	// Callbacks for integration with daemon cache.
	OnPeerJoin    func(Peer)
	OnPeerLeave   func(Peer)
	OnPeerSuspect func(Peer)
}

// gossipMessage is the wire format for UDP gossip. Timestamp and Nonce are
// emitted for future freshness checks, but this branch does not enforce replay
// protection.
type gossipMessage struct {
	Type      string `json:"type"` // "ping", "peer-list", "join", "leave"
	Sender    Peer   `json:"sender"`
	Peers     []Peer `json:"peers,omitempty"`
	Timestamp int64  `json:"ts"`
	Nonce     string `json:"nonce"`
	HMAC      string `json:"hmac"`
}

// New creates a mesh instance. Call Start() to begin gossip.
func New(self Peer, cfg Config, logger *slog.Logger) *Mesh {
	if logger == nil {
		logger = slog.Default()
	}
	return &Mesh{
		peers:  make(map[string]*Peer),
		self:   self,
		cfg:    cfg,
		logger: logger.With("component", "mesh"),
	}
}

// Start begins listening for gossip and broadcasting our peer list.
func (m *Mesh) Start(ctx context.Context) error {
	addr, err := net.ResolveUDPAddr("udp", m.cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("mesh: resolve listen addr: %w", err)
	}

	conn, err := net.ListenUDP("udp", addr)
	if err != nil {
		return fmt.Errorf("mesh: listen: %w", err)
	}
	m.conn = conn

	ctx, m.cancel = context.WithCancel(ctx)

	// Listener goroutine
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.listenLoop(ctx)
	}()

	// Gossip broadcaster
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.gossipLoop(ctx)
	}()

	// Failure detector
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		m.failureDetectorLoop(ctx)
	}()

	m.logger.Info("mesh started",
		"listen", m.cfg.ListenAddr,
		"gossip_interval", m.cfg.GossipInterval,
		"fan_out", m.cfg.FanOut,
	)
	return nil
}

// Stop gracefully shuts down the mesh.
func (m *Mesh) Stop() {
	if m.cancel != nil {
		m.cancel()
	}
	if m.conn != nil {
		m.conn.Close()
	}
	m.wg.Wait()
	m.logger.Info("mesh stopped")
}

// Peers returns a snapshot of all known peers sorted by name.
func (m *Mesh) Peers() []Peer {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]Peer, 0, len(m.peers))
	for _, p := range m.peers {
		out = append(out, *p)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// ActivePeers returns only peers that are verified or trusted.
func (m *Mesh) ActivePeers() []Peer {
	all := m.Peers()
	out := make([]Peer, 0, len(all))
	for _, p := range all {
		if p.State == PeerVerified || p.State == PeerTrusted {
			out = append(out, p)
		}
	}
	return out
}

// Trust promotes a discovered/verified peer to trusted status.
func (m *Mesh) Trust(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.peers[id]
	if !ok {
		return fmt.Errorf("mesh: unknown peer %q", id)
	}
	if p.State == PeerDead {
		return fmt.Errorf("mesh: cannot trust dead peer %q", id)
	}
	p.State = PeerTrusted
	m.logger.Info("peer trusted", "peer", p.Name, "id", id)
	return nil
}

// AddSeed injects a known seed peer (from nodes.yaml).
// Seed peers start as Trusted.
func (m *Mesh) AddSeed(p Peer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	key := m.peerKey(p)
	p.State = PeerTrusted
	p.Source = "config"
	p.FirstSeen = time.Now()
	p.LastSeen = time.Now()
	m.peers[key] = &p
}

func (m *Mesh) peerKey(p Peer) string {
	if p.StableID != "" {
		return p.StableID
	}
	return p.Hostname
}

// listenLoop reads incoming UDP gossip messages.
func (m *Mesh) listenLoop(ctx context.Context) {
	buf := make([]byte, 65536)
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}
		m.conn.SetReadDeadline(time.Now().Add(1 * time.Second))
		n, addr, err := m.conn.ReadFromUDP(buf)
		if err != nil {
			if ne, ok := err.(net.Error); ok && ne.Timeout() {
				continue
			}
			if ctx.Err() != nil {
				return
			}
			m.logger.Warn("mesh read error", "err", err)
			continue
		}

		var msg gossipMessage
		if err := json.Unmarshal(buf[:n], &msg); err != nil {
			m.logger.Debug("mesh: invalid gossip message", "from", addr, "err", err)
			continue
		}

		if !m.verifyMessageHMAC(msg) {
			m.logger.Warn("mesh: HMAC verification failed", "from", addr)
			continue
		}

		m.handleMessage(msg, addr)
	}
}

// gossipLoop periodically broadcasts our peer knowledge to random peers.
func (m *Mesh) gossipLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.GossipInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.broadcastPeerList()
		}
	}
}

// failureDetectorLoop periodically checks for suspect/dead peers.
func (m *Mesh) failureDetectorLoop(ctx context.Context) {
	ticker := time.NewTicker(m.cfg.GossipInterval * 2)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.detectFailures()
		}
	}
}

func (m *Mesh) handleMessage(msg gossipMessage, from *net.UDPAddr) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	switch msg.Type {
	case "ping":
		key := m.peerKey(msg.Sender)
		if p, ok := m.peers[key]; ok {
			p.LastSeen = now
			p.MissedPings = 0
			if p.State == PeerSuspect {
				p.State = PeerVerified
				m.logger.Info("peer recovered", "peer", p.Name)
			}
		}

	case "join":
		m.mergePeer(msg.Sender, now)

	case "peer-list":
		for _, p := range msg.Peers {
			m.mergePeer(p, now)
		}

	case "leave":
		key := m.peerKey(msg.Sender)
		if p, ok := m.peers[key]; ok {
			p.State = PeerDead
			m.logger.Info("peer announced departure", "peer", p.Name)
			if m.OnPeerLeave != nil {
				go m.OnPeerLeave(*p)
			}
		}
	}
}

// mergePeer integrates a gossipped peer into our local state.
// Never demotes a trusted peer. Never exceeds MaxPeers.
func (m *Mesh) mergePeer(p Peer, now time.Time) {
	key := m.peerKey(p)
	if key == m.peerKey(m.self) {
		return // ignore self
	}

	existing, ok := m.peers[key]
	if !ok {
		if len(m.peers) >= m.cfg.MaxPeers {
			return // cap reached for new peers
		}
		// New peer discovered via gossip
		p.State = PeerDiscovered
		p.FirstSeen = now
		p.LastSeen = now
		if p.Source == "" {
			p.Source = "gossip"
		}
		m.peers[key] = &p
		m.logger.Info("new peer discovered", "peer", p.Name, "source", p.Source)
		if m.OnPeerJoin != nil {
			go m.OnPeerJoin(p)
		}
		return
	}

	// Update existing peer — never demote trusted
	if existing.State == PeerTrusted {
		existing.LastSeen = now
		existing.MissedPings = 0
		return
	}

	// Accept newer generation data
	if p.Generation > existing.Generation {
		existing.Generation = p.Generation
		existing.Hostname = p.Hostname
		existing.Port = p.Port
	}
	existing.LastSeen = now
	existing.MissedPings = 0
	if existing.State == PeerSuspect {
		existing.State = PeerVerified
	}
}

// detectFailures transitions silent peers through suspect → dead.
func (m *Mesh) detectFailures() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	for key, p := range m.peers {
		if p.Source == "config" {
			continue // seed nodes exempt from automatic eviction
		}
		silence := now.Sub(p.LastSeen)
		switch {
		case p.State == PeerSuspect && silence > m.cfg.DeadTimeout:
			p.State = PeerDead
			m.logger.Warn("peer declared dead", "peer", p.Name, "silence", silence)
			if m.OnPeerLeave != nil {
				go m.OnPeerLeave(*p)
			}
			delete(m.peers, key)

		case p.State != PeerDead && silence > m.cfg.SuspectTimeout:
			p.State = PeerSuspect
			p.MissedPings++
			m.logger.Info("peer suspect", "peer", p.Name, "missed", p.MissedPings)
			if m.OnPeerSuspect != nil {
				go m.OnPeerSuspect(*p)
			}
		}
	}
}

// broadcastPeerList sends our peer knowledge to a random subset of peers.
func (m *Mesh) broadcastPeerList() {
	m.mu.Lock()
	m.generation++
	m.self.Generation = m.generation
	sender := m.self
	targets := m.selectFanOut()
	peers := make([]Peer, 0, len(m.peers))
	for _, p := range m.peers {
		if p.State != PeerDead {
			peers = append(peers, *p)
		}
	}
	m.mu.Unlock()

	if len(targets) == 0 {
		return
	}

	msg := gossipMessage{
		Type:      "peer-list",
		Sender:    sender,
		Peers:     peers,
		Timestamp: time.Now().UnixMilli(),
	}
	nonce, err := m.generateNonce()
	if err != nil {
		m.logger.Warn("mesh: failed to generate nonce for peer-list broadcast", "err", err)
		return
	}
	msg.Nonce = nonce
	msg.HMAC = m.computeMessageHMAC(msg)
	data, err := json.Marshal(msg)
	if err != nil {
		m.logger.Error("mesh: marshal gossip", "err", err)
		return
	}

	for _, target := range targets {
		addr, err := net.ResolveUDPAddr("udp", target)
		if err != nil {
			continue
		}
		m.conn.WriteToUDP(data, addr)
	}
}

// selectFanOut picks random peers to gossip with, up to cfg.FanOut.
func (m *Mesh) selectFanOut() []string {
	var addrs []string
	for _, p := range m.peers {
		if p.State != PeerDead && p.Hostname != "" {
			port := p.Port
			if port == 0 {
				port = 42426
			}
			addrs = append(addrs, fmt.Sprintf("%s:%d", p.Hostname, port))
		}
	}
	if len(addrs) <= m.cfg.FanOut {
		return addrs
	}
	// Fisher-Yates shuffle, take first FanOut
	for i := len(addrs) - 1; i > 0; i-- {
		n, err := rand.Int(rand.Reader, big.NewInt(int64(i+1)))
		if err != nil {
			m.logger.Warn("mesh: random fanout selection failed", "err", err)
			return addrs[:m.cfg.FanOut]
		}
		j := int(n.Int64())
		addrs[i], addrs[j] = addrs[j], addrs[i]
	}
	return addrs[:m.cfg.FanOut]
}

func (m *Mesh) computeHMAC(data []byte) string {
	if m.cfg.SharedSecret == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(m.cfg.SharedSecret))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

func (m *Mesh) verifyHMAC(data []byte, received string) bool {
	if m.cfg.SharedSecret == "" {
		return true // no auth configured
	}
	expected := m.computeHMAC(data)
	return hmac.Equal([]byte(expected), []byte(received))
}

func (m *Mesh) computeMessageHMAC(msg gossipMessage) string {
	if m.cfg.SharedSecret == "" {
		return ""
	}
	msg.HMAC = ""
	payload, err := json.Marshal(msg)
	if err != nil {
		return ""
	}
	return m.computeHMAC(payload)
}

func (m *Mesh) verifyMessageHMAC(msg gossipMessage) bool {
	if m.cfg.SharedSecret == "" {
		return true
	}
	expected := m.computeMessageHMAC(msg)
	return hmac.Equal([]byte(expected), []byte(msg.HMAC))
}

func (m *Mesh) generateNonce() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
