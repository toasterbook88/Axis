//go:build linux

package netutil

import (
	"context"
	"net"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestOutboundDialerManyPreferredAddressesFallback(t *testing.T) {
	for _, tc := range []struct {
		name            string
		preferredFamily string
		preferredIP     string
		alternateFamily string
		alternateIP     string
	}{
		{name: "IPv6 preferred", preferredFamily: "tcp6", preferredIP: "::1", alternateFamily: "tcp4", alternateIP: "127.0.0.1"},
		{name: "IPv4 preferred", preferredFamily: "tcp4", preferredIP: "127.0.0.1", alternateFamily: "tcp6", alternateIP: "::1"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			good, err := net.Listen(tc.alternateFamily, net.JoinHostPort(tc.alternateIP, "0"))
			if err != nil {
				t.Skipf("both loopback families required: %v", err)
			}
			defer good.Close()
			port := good.Addr().(*net.TCPAddr).Port
			bad, err := net.ListenTCP(tc.preferredFamily, &net.TCPAddr{IP: net.ParseIP(tc.preferredIP), Port: port})
			if err != nil {
				t.Skipf("both loopback families required on one port: %v", err)
			}
			defer bad.Close()
			raw, err := bad.SyscallConn()
			if err != nil {
				t.Fatal(err)
			}
			var listenErr error
			if err := raw.Control(func(fd uintptr) { listenErr = syscall.Listen(int(fd), 0) }); err != nil {
				t.Fatal(err)
			}
			if listenErr != nil {
				t.Fatal(listenErr)
			}
			fill, err := net.DialTimeout(tc.preferredFamily, bad.Addr().String(), time.Second)
			if err != nil {
				t.Fatal(err)
			}
			defer fill.Close()
			var ips []net.IPAddr
			for range 18 {
				ips = append(ips, net.IPAddr{IP: net.ParseIP(tc.preferredIP)})
			}
			ips = append(ips, net.IPAddr{IP: net.ParseIP(tc.alternateIP)})
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			started := time.Now()
			conn, err := dialResolvedIPs(ctx, "tcp", strconv.Itoa(port), ips)
			if err != nil {
				t.Fatalf("alternate family starved by %d preferred addresses after %v: %v", len(ips)-1, time.Since(started), err)
			}
			defer conn.Close()
			if !conn.RemoteAddr().(*net.TCPAddr).IP.Equal(net.ParseIP(tc.alternateIP)) {
				t.Fatalf("connected to %s, want alternate family %s", conn.RemoteAddr(), tc.alternateIP)
			}
			t.Logf("alternate family succeeded in %v with %d preferred records", time.Since(started), len(ips)-1)
		})
	}
}
