package models

import (
	"net"
	"os"
	"strings"
)

type localIdentity struct {
	hostname      string
	shortHostname string
	ips           map[string]struct{}
}

// IsLocalTarget checks if a target hostname or name matches the current machine.
func IsLocalTarget(target string, currentHostname string) bool {
	return newLocalIdentity(currentHostname, nil).matches(target)
}

// IsLocalNode checks if a NodeFacts entry originates from the machine running axis.
func IsLocalNode(n NodeFacts) bool {
	identity, ok := currentLocalIdentity()
	if !ok {
		return false
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

// IsLocalConfig checks if a config entry refers to the machine running axis.
func IsLocalConfig(name, configHostname string) bool {
	_ = name

	identity, ok := currentLocalIdentity()
	if !ok {
		return false
	}
	return identity.matches(configHostname)
}

func currentLocalIdentity() (localIdentity, bool) {
	hostname, err := os.Hostname()
	if err != nil {
		return localIdentity{}, false
	}
	return newLocalIdentity(hostname, localInterfaceIPs()), true
}

func newLocalIdentity(currentHostname string, localIPs []string) localIdentity {
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
