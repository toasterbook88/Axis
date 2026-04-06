package discovery

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net"
	"os"
	"sync"
	"time"

	"github.com/toasterbook88/axis/internal/buildinfo"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

var interfaceAddrs = net.InterfaceAddrs

// beaconPayload is the canonical signed portion of a beacon — all fields
// except the Sig itself. Must stay in sync with Beacon when fields are added.
type beaconPayload struct {
	Type      string    `json:"t"`
	Name      string    `json:"n"`
	Hostname  string    `json:"h"`
	StableID  string    `json:"id,omitempty"`
	IP        string    `json:"ip"`
	SSHPort   int       `json:"p"`
	Role      string    `json:"r"`
	Version   string    `json:"v"`
	Timestamp time.Time `json:"ts"`
}

type Beacon struct {
	Type      string    `json:"t"`
	Name      string    `json:"n"`
	Hostname  string    `json:"h"`
	StableID  string    `json:"id,omitempty"`
	IP        string    `json:"ip"`
	SSHPort   int       `json:"p"`
	Role      string    `json:"r"`
	Version   string    `json:"v"`
	Timestamp time.Time `json:"ts"`
	// Sig is HMAC-SHA256(secret, canonical-beacon-JSON) hex-encoded.
	// Empty when no secret is configured (open/unsecured beacons).
	Sig string `json:"sig,omitempty"`
}

// signBeacon computes HMAC-SHA256(secret, json(payload)) and returns the hex
// signature. Returns empty string if secret is empty (no-auth mode).
func signBeacon(b Beacon, secret string) string {
	if secret == "" {
		return ""
	}
	payload := beaconPayload{
		Type: b.Type, Name: b.Name, Hostname: b.Hostname, StableID: b.StableID, IP: b.IP,
		SSHPort: b.SSHPort, Role: b.Role, Version: b.Version, Timestamp: b.Timestamp,
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(data)
	return hex.EncodeToString(mac.Sum(nil))
}

// verifyBeacon validates the beacon's HMAC signature against the shared
// secret. When secret is empty, all beacons without a Sig pass (open mode).
// When secret is set, only beacons with a valid Sig are accepted.
func verifyBeacon(b Beacon, secret string) bool {
	if secret == "" {
		return b.Sig == "" // accept only unsigned beacons in open mode
	}
	expected := signBeacon(b, secret)
	return hmac.Equal([]byte(b.Sig), []byte(expected))
}

func startUDP(ctx context.Context, cfg *config.Config, discovered map[string]config.NodeConfig, mu *sync.Mutex) {
	startBeaconBroadcaster(ctx, cfg)
	pc, secret, err := openBeaconListener(cfg)
	if err != nil {
		return
	}
	listenForBeacons(ctx, pc, secret, func(b Beacon) {
		mu.Lock()
		mergeBeaconNode(discovered, b)
		mu.Unlock()
	})
}

// WatchBeaconChanges runs a long-lived beacon broadcaster/listener pair and
// updates the provided registry as beacon-derived nodes appear, change, or age
// out. onChange is called only when the observed registry meaningfully changes.
func WatchBeaconChanges(ctx context.Context, cfg *config.Config, registry *BeaconRegistry, onChange func()) {
	if cfg == nil || cfg.Discovery == nil || !cfg.Discovery.Enabled || registry == nil {
		return
	}

	startBeaconBroadcaster(ctx, cfg)
	pc, secret, err := openBeaconListener(cfg)
	if err != nil {
		return
	}

	listenForBeacons(ctx, pc, secret, func(b Beacon) {
		if registry.UpdateFromBeacon(b) && onChange != nil {
			onChange()
		}
	})

	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				if registry.PruneExpired(now.UTC()) && onChange != nil {
					onChange()
				}
			}
		}
	}()
}

func startBeaconBroadcaster(ctx context.Context, cfg *config.Config) {
	port, interval, _, beacon, ok := udpSettings(cfg)
	if !ok {
		return
	}

	go func() {
		conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.IPv4bcast, Port: port})
		if err != nil {
			return
		}
		defer conn.Close()

		ticker := time.NewTicker(time.Duration(interval) * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				beacon.Timestamp = time.Now().UTC()
				beacon.Sig = signBeacon(beacon, udpSecret(cfg))
				data, err := json.Marshal(beacon)
				if err != nil {
					continue
				}
				_, _ = conn.Write(data)
			}
		}
	}()
}

func openBeaconListener(cfg *config.Config) (*net.UDPConn, string, error) {
	port, _, secret, _, ok := udpSettings(cfg)
	if !ok {
		return nil, "", net.InvalidAddrError("discovery disabled")
	}
	pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return nil, "", err
	}
	return pc, secret, nil
}

func listenForBeacons(ctx context.Context, pc *net.UDPConn, secret string, onBeacon func(Beacon)) {
	go func() {
		defer pc.Close()

		go func() {
			<-ctx.Done()
			_ = pc.Close()
		}()

		buf := make([]byte, 1024)
		for {
			n, _, err := pc.ReadFromUDP(buf)
			if err != nil {
				return
			}

			var b Beacon
			if err := json.Unmarshal(buf[:n], &b); err == nil && b.Type == "axis" {
				if !verifyBeacon(b, secret) {
					continue
				}
				if time.Since(b.Timestamp) > beaconTTL {
					continue
				}
				onBeacon(b)
			}
		}
	}()
}

func udpSettings(cfg *config.Config) (int, int, string, Beacon, bool) {
	if cfg == nil || cfg.Discovery == nil || !cfg.Discovery.Enabled {
		return 0, 0, "", Beacon{}, false
	}

	port := 42424
	interval := 3
	if cfg.Discovery.UDPPort > 0 {
		port = cfg.Discovery.UDPPort
	}
	if cfg.Discovery.BeaconInterval > 0 {
		interval = cfg.Discovery.BeaconInterval
	}

	hostname, _ := os.Hostname()
	localStableID := models.CurrentLocalStableID()
	localName := hostname
	localRole := "unknown"
	localSSHPort := 22
	for _, n := range cfg.Nodes {
		if discoveryIsLocalConfig(n) {
			localName = n.Name
			localRole = n.Role
			localSSHPort = n.EffectiveSSHPort()
			break
		}
	}

	beacon := Beacon{
		Type:     "axis",
		Name:     localName,
		Hostname: hostname,
		StableID: localStableID,
		IP:       localIP(),
		SSHPort:  localSSHPort,
		Role:     localRole,
		Version:  buildinfo.Version,
	}
	return port, interval, cfg.Discovery.Secret, beacon, true
}

func udpSecret(cfg *config.Config) string {
	if cfg == nil || cfg.Discovery == nil {
		return ""
	}
	return cfg.Discovery.Secret
}

func mergeBeaconNode(discovered map[string]config.NodeConfig, b Beacon) {
	if key, exists := discoveredNodeKey(discovered, b.Name, b.StableID); exists {
		existing := discovered[key]
		if existing.StableID == "" {
			existing.StableID = models.NormalizeStableID(b.StableID)
			discovered[key] = existing
		}
		return
	}

	discovered[b.Name] = config.NodeConfig{
		Name:       beaconNodeConfig(b).Name,
		Hostname:   beaconNodeConfig(b).Hostname,
		StableID:   beaconNodeConfig(b).StableID,
		SSHUser:    beaconNodeConfig(b).SSHUser,
		Role:       beaconNodeConfig(b).Role,
		SSHPort:    beaconNodeConfig(b).SSHPort,
		TimeoutSec: beaconNodeConfig(b).TimeoutSec,
	}
}

func discoveredNodeKey(discovered map[string]config.NodeConfig, name, stableID string) (string, bool) {
	if _, exists := discovered[name]; exists {
		return name, true
	}

	normalizedStableID := models.NormalizeStableID(stableID)
	if normalizedStableID == "" {
		return "", false
	}

	for key, node := range discovered {
		if models.NormalizeStableID(node.StableID) == normalizedStableID {
			return key, true
		}
	}
	return "", false
}

func localIP() string {
	addrs, err := interfaceAddrs()
	if err != nil {
		return "127.0.0.1"
	}
	for _, address := range addrs {
		if ipnet, ok := address.(*net.IPNet); ok && !ipnet.IP.IsLoopback() {
			if ipnet.IP.To4() != nil {
				return ipnet.IP.String()
			}
		}
	}
	return "127.0.0.1"
}
