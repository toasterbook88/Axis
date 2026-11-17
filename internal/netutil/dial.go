package netutil

import (
	"context"
	"fmt"
	"net"
	"slices"
)

// DialOutboundContext resolves once using ctx, applies the outbound address
// policy, then dials only those literal IPs. It must not be combined with a
// proxy that resolves the original hostname independently.
func DialOutboundContext(ctx context.Context, network, address string, allowedHosts []string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("resolve outbound host %q: %w", host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("outbound host %q resolved to no addresses", host)
	}
	if !slices.Contains(allowedHosts, host) && !isAllowlisted(host) {
		for _, ip := range ips {
			if isInternalIP(ip.IP) {
				return nil, internalAddrErr(host, ip.IP)
			}
		}
	}
	var dialer net.Dialer
	for _, ip := range ips {
		var conn net.Conn
		conn, err = dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		if err == nil {
			return conn, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
	}
	return nil, err
}
