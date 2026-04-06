package models

import (
	"context"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"
)

type localIdentity struct {
	hostname      string
	shortHostname string
	ips           map[string]struct{}
	stableID      string
}

var localHostnameFn = os.Hostname
var localStableIDReadFile = os.ReadFile
var localStableIDCommand = func(ctx context.Context, name string, args ...string) (string, error) {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	return string(out), err
}
var localInterfaceIPsFn = localInterfaceIPs

var (
	localStableIDOnce   sync.Once
	localStableIDCached string
)

// IsLocalTarget checks if a target hostname or name matches the current machine.
func IsLocalTarget(target string, currentHostname string) bool {
	return newLocalIdentity(currentHostname, nil, "").matches(target)
}

// IsLocalNode checks if a NodeFacts entry originates from the machine running axis.
func IsLocalNode(n NodeFacts) bool {
	identity, ok := currentLocalIdentity()
	if !ok {
		return false
	}
	if n.Identity != nil && identity.stableID != "" && NormalizeStableID(n.Identity.StableID) == identity.stableID {
		return true
	}
	if identity.matches(n.Hostname) {
		return true
	}
	for _, addr := range n.Addresses {
		if identity.matches(addr.Address) {
			return true
		}
	}
	return false
}

// FindLocalNode returns the first node that matches the current machine.
func FindLocalNode(nodes []NodeFacts) (NodeFacts, bool) {
	for _, n := range nodes {
		if IsLocalNode(n) {
			return n, true
		}
	}
	return NodeFacts{}, false
}

// IsLocalConfig checks if a config entry refers to the machine running axis.
func IsLocalConfig(name, configHostname, configStableID string) bool {
	_ = name

	identity, ok := currentLocalIdentity()
	if !ok {
		return false
	}
	if identity.stableID != "" && NormalizeStableID(configStableID) == identity.stableID {
		return true
	}
	return identity.matches(configHostname)
}

// CurrentLocalStableID returns the observed stable identity for the machine
// running axis, if one is available.
func CurrentLocalStableID() string {
	return localStableID()
}

func currentLocalIdentity() (localIdentity, bool) {
	hostname, err := localHostnameFn()
	if err != nil {
		return localIdentity{}, false
	}
	return newLocalIdentity(hostname, localInterfaceIPsFn(), localStableID()), true
}

func newLocalIdentity(currentHostname string, localIPs []string, stableID string) localIdentity {
	hostname := normalizeLocalTarget(currentHostname)
	shortHostname := hostname
	if i := strings.Index(shortHostname, "."); i >= 0 {
		shortHostname = shortHostname[:i]
	}

	ips := make(map[string]struct{}, len(localIPs))
	for _, ip := range localIPs {
		if normalized := normalizeLocalTarget(ip); normalized != "" {
			ips[normalized] = struct{}{}
		}
	}

	return localIdentity{
		hostname:      hostname,
		shortHostname: shortHostname,
		ips:           ips,
		stableID:      NormalizeStableID(stableID),
	}
}

func (id localIdentity) matches(target string) bool {
	t := normalizeLocalTarget(target)
	if t == "" {
		return false
	}

	if t == "localhost" {
		return true
	}

	if ip := net.ParseIP(t); ip != nil {
		if ip.IsLoopback() {
			return true
		}
		_, ok := id.ips[ip.String()]
		return ok
	}

	if id.hostname != "" && t == id.hostname {
		return true
	}
	if id.shortHostname != "" {
		tShort := t
		if i := strings.Index(tShort, "."); i >= 0 {
			tShort = tShort[:i]
		}
		return tShort == id.shortHostname
	}
	return false
}

func localInterfaceIPs() []string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return nil
	}

	ips := make([]string, 0, len(addrs))
	for _, addr := range addrs {
		ipNet, ok := addr.(*net.IPNet)
		if !ok || ipNet.IP == nil {
			continue
		}
		ips = append(ips, ipNet.IP.String())
	}
	return ips
}

func normalizeLocalTarget(target string) string {
	return strings.ToLower(strings.TrimSpace(target))
}

func localStableID() string {
	localStableIDOnce.Do(func() {
		localStableIDCached = detectLocalStableID()
	})
	return localStableIDCached
}

func detectLocalStableID() string {
	switch runtime.GOOS {
	case "linux":
		for _, path := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			data, err := localStableIDReadFile(path)
			if err != nil {
				continue
			}
			if id := NormalizeStableID(string(data)); id != "" {
				return id
			}
		}
	case "darwin":
		ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
		defer cancel()

		if out, err := localStableIDCommand(ctx, "ioreg", "-rd1", "-c", "IOPlatformExpertDevice"); err == nil {
			if id := ParseDarwinPlatformUUID(out); id != "" {
				return id
			}
		}
	}
	return ""
}
