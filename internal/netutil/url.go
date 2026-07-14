// Package netutil contains small helpers for outbound network operations.
package netutil

import (
	"fmt"
	"net"
	"net/url"
	"sync"
)

var (
	allowMu              sync.RWMutex
	allowedInternalHosts = map[string]bool{}
)

// AllowInternalHost adds a host (hostname or IP literal, as it appears in the
// URL) to the internal-address allowlist. Outbound URLs whose host is
// allowlisted bypass the loopback/private/link-local check, enabling explicit
// self-hosted opt-in (e.g. a webhook or MCP server on the local cluster).
func AllowInternalHost(host string) {
	allowMu.Lock()
	defer allowMu.Unlock()
	allowedInternalHosts[host] = true
}

// ResetInternalAllowlist clears the internal-address allowlist.
func ResetInternalAllowlist() {
	allowMu.Lock()
	defer allowMu.Unlock()
	allowedInternalHosts = map[string]bool{}
}

func isAllowlisted(host string) bool {
	allowMu.RLock()
	defer allowMu.RUnlock()
	return allowedInternalHosts[host]
}

// ValidateOutboundURL accepts only HTTP(S) URLs with a non-empty host that does
// not resolve to an internal address (loopback, private, link-local, or
// unspecified). This mitigates SSRF to cloud metadata endpoints and internal
// services. Hosts added via AllowInternalHost are exempt for self-hosted use.
func ValidateOutboundURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("invalid outbound URL: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return fmt.Errorf("outbound URL must use http or https scheme")
	}
	host := parsed.Hostname()
	if host == "" {
		return fmt.Errorf("outbound URL must include a host")
	}
	if isAllowlisted(host) {
		return nil
	}

	// IP literal: check it directly.
	if ip := net.ParseIP(host); ip != nil {
		if isInternalIP(ip) {
			return internalAddrErr(host, ip)
		}
		return nil
	}

	// Hostname: resolve and block if any address is internal. Resolution
	// failures fail closed — a name we cannot vet must not be allowed through,
	// otherwise DNS rebinding / intermittent DNS defeats the check. Operators
	// with an unresolvable-at-config-time internal target must allowlist it.
	ips, lookupErr := net.LookupIP(host)
	if lookupErr != nil {
		return fmt.Errorf("outbound URL host %q could not be resolved for validation: %w (allowlist it explicitly to permit)", host, lookupErr)
	}
	for _, ip := range ips {
		if isInternalIP(ip) {
			return internalAddrErr(host, ip)
		}
	}
	return nil
}

func internalAddrErr(host string, ip net.IP) error {
	return fmt.Errorf("outbound URL host %q resolves to disallowed internal address %s (use netutil.AllowInternalHost to permit)", host, ip)
}

// isInternalIP reports whether ip is loopback, private, link-local, or the
// unspecified address — destinations an outbound advisory request should not
// reach unless explicitly allowlisted.
func isInternalIP(ip net.IP) bool {
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsUnspecified()
}
