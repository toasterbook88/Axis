package netutil

import (
	"context"
	"fmt"
	"net"
	"slices"
	"time"
)

const outboundFallbackDelay = 300 * time.Millisecond

// NewOutboundDialer snapshots the DNS resolver and explicit allowlist before
// HTTP transport starts asynchronous dials. Each call resolves once using ctx,
// applies the outbound address policy, and dials only those literal IPs. It must
// not be combined with a proxy that resolves the hostname independently.
func NewOutboundDialer(allowedHosts []string) func(context.Context, string, string) (net.Conn, error) {
	resolver := net.DefaultResolver
	allowedHosts = slices.Clone(allowedHosts)
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		return dialOutboundContext(ctx, network, address, allowedHosts, resolver)
	}
}

func dialOutboundContext(ctx context.Context, network, address string, allowedHosts []string, resolver *net.Resolver) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, err
	}
	ips, err := resolver.LookupIPAddr(ctx, host)
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
	return dialResolvedIPs(ctx, network, port, ips)
}

func dialResolvedIPs(ctx context.Context, network, port string, ips []net.IPAddr) (net.Conn, error) {
	ips = interleaveIPFamilies(ips)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	type result struct {
		conn net.Conn
		err  error
	}
	results := make(chan result)
	var dialer net.Dialer
	next, pending := 0, 0
	startDial := func() {
		ip := ips[next]
		next++
		pending++
		go func() {
			conn, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			select {
			case results <- result{conn: conn, err: err}:
			case <-ctx.Done():
				if conn != nil {
					conn.Close()
				}
			}
		}()
	}
	startDial()
	// Preserve fallback when a preferred address stalls, without resolving the
	// hostname again. Failed attempts accelerate the next checked address.
	timer := time.NewTimer(outboundFallbackDelay)
	defer timer.Stop()
	var lastErr error
	for pending > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case res := <-results:
			pending--
			if res.err == nil {
				return res.conn, nil
			}
			lastErr = res.err
			if next < len(ips) {
				startDial()
				timer.Reset(outboundFallbackDelay)
			}
		case <-timer.C:
			if next < len(ips) {
				startDial()
				timer.Reset(outboundFallbackDelay)
			}
		}
	}
	return nil, lastErr
}

// interleaveIPFamilies keeps each family's order but puts the alternate family
// second. Its first attempt therefore starts after one fallback delay, regardless
// of the number of preferred-family records, without discarding any checked IPs.
func interleaveIPFamilies(ips []net.IPAddr) []net.IPAddr {
	if len(ips) < 2 {
		return ips
	}
	var queues [2][]net.IPAddr
	firstIsIPv4 := ips[0].IP.To4() != nil
	for _, ip := range ips {
		family := 0
		if (ip.IP.To4() != nil) != firstIsIPv4 {
			family = 1
		}
		queues[family] = append(queues[family], ip)
	}
	ordered := make([]net.IPAddr, 0, len(ips))
	for i := 0; len(ordered) < len(ips); i++ {
		for _, queue := range queues {
			if i < len(queue) {
				ordered = append(ordered, queue[i])
			}
		}
	}
	return ordered
}
