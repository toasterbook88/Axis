package netutil

import (
	"net/netip"
	"testing"
)

func TestIsTailscaleAddr(t *testing.T) {
	tailscale6 := netip.AddrFrom16([16]byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0, 15: 1})
	nextULA := netip.AddrFrom16([16]byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe1, 15: 1})
	for _, tt := range []struct {
		addr netip.Addr
		want bool
	}{
		{tailscale6, true},
		{nextULA, false},
		{netip.MustParseAddr("fd00::1"), false},
		{netip.MustParseAddr("100.64.0.1"), true},
		{netip.MustParseAddr("100.127.255.254"), true},
		{netip.MustParseAddr("100.63.0.1"), false},
		{netip.MustParseAddr("100.128.0.1"), false},
		{netip.MustParseAddr("::ffff:100.64.0.1"), true},
		{netip.MustParseAddr("192.168.1.1"), false},
		{netip.Addr{}, false},
	} {
		if got := IsTailscaleAddr(tt.addr); got != tt.want {
			t.Errorf("IsTailscaleAddr(%v) = %t, want %t", tt.addr, got, tt.want)
		}
	}
}
