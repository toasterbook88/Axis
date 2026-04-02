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
		Type: b.Type, Name: b.Name, Hostname: b.Hostname, IP: b.IP,
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
	port := 42424
	interval := 3
	secret := ""
	if cfg.Discovery != nil {
		if cfg.Discovery.UDPPort > 0 {
			port = cfg.Discovery.UDPPort
		}
		if cfg.Discovery.BeaconInterval > 0 {
			interval = cfg.Discovery.BeaconInterval
		}
		secret = cfg.Discovery.Secret
	}

	hostname, _ := os.Hostname()
	localName := hostname
	localRole := "unknown"
	localSSHPort := 22
	for _, n := range cfg.Nodes {
		if models.IsLocalConfig(n.Name, n.Hostname) {
			localName = n.Name
			localRole = n.Role
			localSSHPort = n.EffectiveSSHPort()
			break
		}
	}

	ipStr := localIP()

	pc, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err != nil {
		return
	}

	// Broadcaster
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
				b := Beacon{
					Type:      "axis",
					Name:      localName,
					Hostname:  hostname,
					IP:        ipStr,
					SSHPort:   localSSHPort,
					Role:      localRole,
					Version:   buildinfo.Version,
					Timestamp: time.Now().UTC(),
				}
				b.Sig = signBeacon(b, secret)
				data, _ := json.Marshal(b)
				conn.Write(data)
			}
		}
	}()

	// Listener
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
				if time.Since(b.Timestamp) > 30*time.Second {
					continue
				}

				mu.Lock()
				// Only inject discovered nodes that aren't already explicitly bound
				// to static SSH configurations to avoid clobbering robust setups
				if _, exists := discovered[b.Name]; !exists {
					discovered[b.Name] = config.NodeConfig{
						Name:       b.Name,
						Hostname:   b.IP,
						SSHUser:    "axis", // Need default because UDP doesn't broadcast SSH user safely
						Role:       b.Role,
						SSHPort:    b.SSHPort,
						TimeoutSec: 10,
					}
				}
				mu.Unlock()
			}
		}
	}()
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
