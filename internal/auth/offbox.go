package auth

import (
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
)

// AllowOffBoxBearerEnv is the break-glass switch for aiming a daemon-facing
// request at a non-loopback TCP host. Without it, every consumer that attaches
// the cluster bearer token (~/.axis/token) to a user-supplied address refuses
// the dial: the token authenticates the whole cluster API, and handing it to an
// arbitrary host is a credential disclosure, not a routing choice.
const AllowOffBoxBearerEnv = "AXIS_ALLOW_OFFBOX_BEARER"

// RequestBaseURL is the URL the daemon client and the A2A client request.
// Only a lowercase http or https prefix is kept. Every other value, including
// an uppercase scheme or a custom scheme, gets an "http://" prefix, so the
// original scheme text becomes the host. Callers that attach a bearer must
// classify this URL, not the raw address.
func RequestBaseURL(addr string) string {
	addr = strings.TrimSpace(addr)
	addr = strings.TrimRight(addr, "/")
	if !strings.HasPrefix(addr, "http://") && !strings.HasPrefix(addr, "https://") {
		addr = "http://" + addr
	}
	return addr
}

// AddrKeepsClusterBearer reports whether the request that would carry the
// bearer stays on this machine. Unix sockets are local. Anything else is
// judged by the host of RequestBaseURL, which is the URL the clients dial.
func AddrKeepsClusterBearer(addr string) bool {
	if IsUnixAddr(addr) {
		return true
	}
	host := DialHost(RequestBaseURL(addr))
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// RefuseOffBoxBearer refuses addr when a cluster bearer token would be
// attached to a non-local host without the explicit break-glass env set.
// The error names the env switch so an operator who really means it can
// opt in explicitly.
func RefuseOffBoxBearer(addr string) error {
	if addr == "" || AddrKeepsClusterBearer(addr) {
		return nil
	}
	if os.Getenv(AllowOffBoxBearerEnv) == "1" {
		return nil
	}
	return fmt.Errorf("refusing off-box daemon address %q: it would attach the cluster bearer token (set %s=1 to allow)", addr, AllowOffBoxBearerEnv)
}

// DialHost extracts the host part of addr, stripping scheme, port, and
// IPv6 brackets. Empty on an unparseable scheme'd URL.
func DialHost(addr string) string {
	raw := strings.TrimSpace(addr)
	if strings.Contains(raw, "://") {
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			return ""
		}
		raw = u.Host
	}
	host, _, err := net.SplitHostPort(raw)
	if err != nil {
		host = raw
	}
	return strings.Trim(strings.ToLower(host), "[]")
}
