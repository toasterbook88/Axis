package netutil

import "net/netip"

// IsTailscaleAddr reports whether ip is in Tailscale's address ranges: its IPv4
// CGNAT range 100.64.0.0/10 and its own IPv6 /48 (not all of IPv6 ULA). An
// IPv4-mapped address is unmapped first, since a mapped address never matches
// an IPv4 range by bit length.
func IsTailscaleAddr(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is4() {
		b := ip.As4()
		return b[0] == 100 && b[1] >= 64 && b[1] <= 127
	}
	if !ip.Is6() {
		return false
	}
	b := ip.As16()
	return b[0] == 0xfd && b[1] == 0x7a && b[2] == 0x11 && b[3] == 0x5c && b[4] == 0xa1 && b[5] == 0xe0
}
