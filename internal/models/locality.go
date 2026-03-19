package models

import (
	"os"
	"strings"
)

// IsLocalTarget checks if a target hostname or name matches the current machine.
func IsLocalTarget(target string, currentHostname string) bool {
	if target == "" {
		return false
	}
	t := strings.ToLower(target)
	h := strings.ToLower(currentHostname)

	if t == "localhost" || t == "127.0.0.1" {
		return true
	}

	if t == h {
		return true
	}

	tShort := strings.Split(t, ".")[0]
	hShort := strings.Split(h, ".")[0]

	return tShort == hShort
}

// IsLocalNode checks if a NodeFacts entry originates from the machine running axis.
func IsLocalNode(n NodeFacts) bool {
	hostname, err := os.Hostname()
	if err != nil {
		return false
	}
	return IsLocalTarget(n.Name, hostname) || IsLocalTarget(n.Hostname, hostname)
}

// IsLocalConfig checks if a config entry refers to the machine running axis.
func IsLocalConfig(name, configHostname string) bool {
	hostname, err := os.Hostname()
	if err != nil {
		return false
	}
	return IsLocalTarget(name, hostname) || IsLocalTarget(configHostname, hostname)
}
