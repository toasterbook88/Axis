package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/transport"
)

const genSuffix = "tailnet.example.com"

type fakePeer struct {
	HostName, DNSName string
	IPs               []string
	Online            bool
	Addrs             []string
	ShareeNode        bool
	AltSharerUserID   int64
}

type fakeTailnet struct {
	backend      string
	selfOnline   *bool
	selfIPs      []string
	selfDNS      string
	suffix       string // CurrentTailnet.MagicDNSSuffix
	legacySuffix string // top-level MagicDNSSuffix
	magicDNS     bool
	peers        []fakePeer
}

func onlineTailnet(peers ...fakePeer) fakeTailnet {
	on := true
	return fakeTailnet{backend: "Running", selfOnline: &on, selfIPs: []string{"100.64.0.1"},
		selfDNS: "here." + genSuffix + ".", suffix: genSuffix + ".", magicDNS: true, peers: peers}
}

func (f fakeTailnet) json() []byte {
	peers := map[string]any{}
	for i, p := range f.peers {
		dns := p.DNSName
		if dns != "" && !strings.HasSuffix(dns, ".") {
			dns += "."
		}
		peers[string(rune('a'+i))] = map[string]any{
			"HostName": p.HostName, "DNSName": dns, "TailscaleIPs": p.IPs, "Online": p.Online,
			"Addrs": p.Addrs, "ShareeNode": p.ShareeNode, "AltSharerUserID": p.AltSharerUserID,
		}
	}
	self := map[string]any{"HostName": "here", "DNSName": f.selfDNS, "TailscaleIPs": f.selfIPs}
	if f.selfOnline != nil {
		self["Online"] = *f.selfOnline
	}
	st := map[string]any{"BackendState": f.backend, "Self": self, "Peer": peers, "MagicDNSSuffix": f.legacySuffix,
		"CurrentTailnet": map[string]any{"MagicDNSSuffix": f.suffix, "MagicDNSEnabled": f.magicDNS}}
	data, _ := json.Marshal(st)
	return data
}

type genFixture struct {
	status   func(context.Context) ([]byte, error)
	ssh      map[string]transport.SSHTarget
	dns      map[string][]string
	hang     map[string]bool
	sshHang  map[string]bool
	sshFail  map[string]bool
	local    map[string]bool
	localCfg string // first-label hostname that NodeConfig.IsLocal() would accept
}

func (f *genFixture) deps() generateDependencies {
	return generateDependencies{
		tailscaleStatus: f.status,
		resolveSSH: func(ctx context.Context, host string, port int, user string) (transport.SSHTarget, error) {
			if f.sshHang[host] {
				<-ctx.Done()
				return transport.SSHTarget{}, ctx.Err()
			}
			if f.sshFail[host] {
				return transport.SSHTarget{}, errors.New("ssh -G: bad configuration option")
			}
			if t, ok := f.ssh[host]; ok {
				return t, nil
			}
			return transport.SSHTarget{Hostname: host, Port: port, User: user}, nil
		},
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			if f.hang[host] {
				<-ctx.Done()
				return nil, ctx.Err()
			}
			var out []netip.Addr
			for _, s := range f.dns[normalizeGenerateName(host)] {
				out = append(out, netip.MustParseAddr(s))
			}
			if len(out) == 0 {
				return nil, errors.New("no such host")
			}
			return out, nil
		},
		// Like models.IsLocalHost: case-insensitive, but a trailing dot is not removed.
		isLocalHost: func(name string) bool { return f.local[strings.ToLower(strings.TrimSpace(name))] },
		isLocalConfig: func(n config.NodeConfig) bool {
			label, _, _ := strings.Cut(normalizeGenerateName(n.Hostname), ".")
			return f.localCfg != "" && label == f.localCfg
		},
		localStableID: func() string { return "local-stable-id" },
		now:           func() time.Time { return time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC) },
	}
}

func fixture(t fakeTailnet) *genFixture {
	return &genFixture{status: func(context.Context) ([]byte, error) { return t.json(), nil },
		local: map[string]bool{"localhost": true}}
}

func genCfg(nodes ...config.NodeConfig) *config.Config { return &config.Config{Nodes: nodes} }

func node(name, host string) config.NodeConfig {
	return config.NodeConfig{Name: name, Hostname: host, SSHUser: "operator"}
}

func run(t *testing.T, f *genFixture, cfg *config.Config) generateReport {
	t.Helper()
	return buildGenerateReport(context.Background(), f.deps(), cfg, "operator")
}

func proposalNames(r generateReport) []string {
	var out []string
	for _, p := range r.Proposed {
		out = append(out, p.Name)
	}
	return out
}

func notSeen(r generateReport, name string) string {
	for _, n := range r.NotSeen {
		if n.Name == name {
			return n.Status
		}
	}
	for _, n := range r.NotObservable {
		if n == name {
			return "not observable"
		}
	}
	return ""
}

func hasFlag(p generateProposal, prefix string) bool {
	for _, f := range p.Flags {
		if strings.HasPrefix(f, prefix) {
			return true
		}
	}
	return false
}

func proposal(t *testing.T, r generateReport, name string) generateProposal {
	t.Helper()
	for _, p := range r.Proposed {
		if p.Name == name {
			return p
		}
	}
	t.Fatalf("no proposal %q in %v", name, proposalNames(r))
	return generateProposal{}
}

func alpha() fakePeer {
	return fakePeer{HostName: "alpha", DNSName: "alpha." + genSuffix, IPs: []string{"100.64.0.10"}, Online: true}
}

func TestGenerateMatchingProposesNothingForConfiguredPeers(t *testing.T) {
	for name, cfgNode := range map[string]config.NodeConfig{
		"tailnet IP":         node("a", "100.64.0.10"),
		"DNSName":            node("a", "alpha."+genSuffix),
		"short name via DNS": node("a", "alpha"),
		"ssh alias":          node("a", "build-alias"),
		"endpoint":           {Name: "a", Hostname: "192.0.2.50", SSHUser: "operator", Endpoints: []config.NodeEndpoint{{Hostname: "100.64.0.10"}}},
		"non-22 port":        {Name: "a", Hostname: "100.64.0.10", SSHUser: "operator", SSHPort: 2222},
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture(onlineTailnet(alpha()))
			f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}, "alpha": {"100.64.0.10"}, "alias-target.example.com": {"100.64.0.10"}}
			f.ssh = map[string]transport.SSHTarget{"build-alias": {Hostname: "alias-target.example.com"}}
			r := run(t, f, genCfg(cfgNode))
			if len(r.Proposed) != 0 || notSeen(r, "a") != "" {
				t.Fatalf("proposed %v, not seen %q; want alpha matched", proposalNames(r), notSeen(r, "a"))
			}
		})
	}
}

func TestGenerateStaleLANAddressIsFlaggedUntilATailnetEndpointIsAdded(t *testing.T) {
	box := fakePeer{HostName: "box", DNSName: "box." + genSuffix, IPs: []string{"100.64.0.40"}, Online: true}
	f := fixture(onlineTailnet(box))
	f.dns = map[string][]string{"box." + genSuffix: {"100.64.0.40"}}

	r := run(t, f, genCfg(node("box", "192.0.2.40")))
	if p := proposal(t, r, "box-2"); !hasFlag(p, "possible duplicate of box") {
		t.Fatalf("LAN-only node: proposal %+v, want box-2 flagged as a possible duplicate", p)
	}

	repaired := config.NodeConfig{Name: "box", Hostname: "192.0.2.41", SSHUser: "operator", Endpoints: []config.NodeEndpoint{
		{Name: "lan", Hostname: "192.0.2.41"},
		{Name: "tailscale", Hostname: "100.64.0.40"},
	}}
	r = run(t, f, genCfg(repaired))
	if len(r.Proposed) != 0 || notSeen(r, "box") != "" {
		t.Fatalf("with a Tailscale endpoint: proposed %v, not seen %q; want box matched", proposalNames(r), notSeen(r, "box"))
	}
}

func TestGenerateSameHostNameDifferentMachinesIsSuffixedAndFlagged(t *testing.T) {
	p1 := fakePeer{HostName: "box", IPs: []string{"100.64.0.11"}, Online: true}
	p2 := fakePeer{HostName: "box", IPs: []string{"100.64.0.12"}, Online: true}
	f := fixture(onlineTailnet(p1, p2))
	f.ssh = map[string]transport.SSHTarget{}
	r := run(t, f, genCfg(node("box", "100.64.0.11")))
	p := proposal(t, r, "box-2")
	if p.Hostname != "100.64.0.12" || !hasFlag(p, "possible duplicate of box") {
		t.Fatalf("proposal %+v, want box-2 at 100.64.0.12 flagged", p)
	}
}

func TestGenerateLANAliasSharingALabelIsNotAMatch(t *testing.T) {
	web := fakePeer{HostName: "web1", DNSName: "web1." + genSuffix, IPs: []string{"100.64.0.20"}, Online: true}
	f := fixture(onlineTailnet(web))
	f.dns = map[string][]string{"web1": {"192.0.2.20"}, "web1." + genSuffix: {"100.64.0.20"}}
	r := run(t, f, genCfg(node("web1", "web1")))
	p := proposal(t, r, "web1-2")
	if !hasFlag(p, "possible duplicate of web1") {
		t.Fatalf("proposal %+v, want a possible-duplicate flag", p)
	}
}

func TestGenerateLANEndpointFlagsButDoesNotMatch(t *testing.T) {
	peer := fakePeer{HostName: "gpu", DNSName: "gpu." + genSuffix, IPs: []string{"100.64.0.30"}, Online: true,
		Addrs: []string{"10.0.0.50:41641", "203.0.113.9:41641"}}
	f := fixture(onlineTailnet(peer))
	f.dns = map[string][]string{"gpu." + genSuffix: {"100.64.0.30"}}
	r := run(t, f, genCfg(node("workstation", "10.0.0.50")))
	p := proposal(t, r, "gpu")
	if !hasFlag(p, "possible duplicate of workstation (by LAN endpoint)") {
		t.Fatalf("proposal %+v, want the LAN-endpoint flag", p)
	}
	if notSeen(r, "workstation") != "not observable" {
		t.Fatalf("workstation status %q, want not observable (an endpoint never counts as seen)", notSeen(r, "workstation"))
	}
}

func TestGenerateSharedEndpointsNeverFlag(t *testing.T) {
	a := fakePeer{HostName: "a", IPs: []string{"100.64.0.31"}, Online: true, Addrs: []string{"203.0.113.9:1", "10.0.0.10:1"}}
	b := fakePeer{HostName: "b", IPs: []string{"100.64.0.32"}, Online: true, Addrs: []string{"203.0.113.9:2", "10.0.0.10:2"}}
	f := fixture(onlineTailnet(a, b))
	f.dns = map[string][]string{"dyn.example.com": {"203.0.113.9"}}
	r := run(t, f, genCfg(node("dyn", "dyn.example.com"), node("lan", "10.0.0.10")))
	for _, p := range r.Proposed {
		if len(p.Flags) != 0 {
			t.Fatalf("proposal %+v flagged by a shared endpoint", p)
		}
	}
	if notSeen(r, "dyn") == "" || notSeen(r, "lan") == "" {
		t.Fatalf("nodes counted as seen through shared endpoints: %+v", r.NotSeen)
	}
}

func TestGenerateDepartedNodeIsNotSeenEvenIfItsLANIPIsAnotherPeers(t *testing.T) {
	other := fakePeer{HostName: "other", IPs: []string{"100.64.0.40"}, Online: true, Addrs: []string{"10.0.0.10:1"}}
	f := fixture(onlineTailnet(other))
	gone := config.NodeConfig{Name: "gone", Hostname: "100.64.0.99", SSHUser: "operator", Endpoints: []config.NodeEndpoint{{Hostname: "10.0.0.10"}}}
	r := run(t, f, genCfg(gone))
	if notSeen(r, "gone") != notSeenNotInMap {
		t.Fatalf("gone status %q, want %q", notSeen(r, "gone"), notSeenNotInMap)
	}
}

func TestGenerateSuffixOrderFollowsTailnetIPs(t *testing.T) {
	peers := []fakePeer{
		{HostName: "pi", IPs: []string{"100.64.0.9"}, Online: true},
		{HostName: "pi", IPs: []string{"100.64.0.7"}, Online: true},
		{HostName: "pi", IPs: []string{"100.64.0.8"}, Online: true},
	}
	for range 3 {
		r := run(t, fixture(onlineTailnet(peers...)), nil)
		got := map[string]string{}
		for _, p := range r.Proposed {
			got[p.Name] = p.TailscaleIP
		}
		if got["pi"] != "100.64.0.7" || got["pi-2"] != "100.64.0.8" || got["pi-3"] != "100.64.0.9" {
			t.Fatalf("names %v, want pi, pi-2, pi-3 in tailnet-IP order", got)
		}
	}
}

func TestGenerateOfflineAndNotSeen(t *testing.T) {
	offline := fakePeer{HostName: "sleepy", IPs: []string{"100.64.0.50"}, Online: false}
	stranger := fakePeer{HostName: "stranger", IPs: []string{"100.64.0.51"}, Online: false}
	ula := netip.AddrFrom16([16]byte{0xfd, 0x00, 15: 7}).String()
	f := fixture(onlineTailnet(offline, stranger))
	r := run(t, f, genCfg(node("sleepy", "100.64.0.50"), node("missing", "100.64.0.60"),
		node("lan", "192.0.2.70"), node("lan6", ula)))
	if len(r.Proposed) != 0 || len(r.OfflinePeers) != 1 || r.OfflinePeers[0].HostName != "stranger" {
		t.Fatalf("proposed %v, offline %+v; want stranger listed, nothing proposed", proposalNames(r), r.OfflinePeers)
	}
	for name, want := range map[string]string{"sleepy": notSeenOffline, "missing": notSeenNotInMap, "lan": "not observable", "lan6": "not observable"} {
		if got := notSeen(r, name); got != want {
			t.Errorf("%s: %q, want %q", name, got, want)
		}
	}
}

func TestGenerateNotObserved(t *testing.T) {
	off := false
	on := true
	peers := []fakePeer{alpha()}
	cases := map[string]*genFixture{
		"command fails": {status: func(context.Context) ([]byte, error) { return nil, errors.New("exit status 1") }},
		"hangs": {status: func(ctx context.Context) ([]byte, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}},
		"invalid json":       {status: func(context.Context) ([]byte, error) { return []byte("{"), nil }},
		"stopped":            fixture(fakeTailnet{backend: "Stopped", selfOnline: &on, peers: peers}),
		"needs login":        fixture(fakeTailnet{backend: "NeedsLogin", selfOnline: &on}),
		"needs login, peers": fixture(fakeTailnet{backend: "NeedsLogin", selfOnline: &on, peers: peers}),
		"self offline":       fixture(fakeTailnet{backend: "Running", selfOnline: &off, peers: peers}),
		"self.online ?":      fixture(fakeTailnet{backend: "Running", peers: peers}),
	}
	for name, f := range cases {
		t.Run(name, func(t *testing.T) {
			if f.local == nil {
				f.local = map[string]bool{}
			}
			r := run(t, f, genCfg(node("a", "100.64.0.10")))
			if r.Observed || r.NotObservedReason == "" || len(r.NotSeen) != 0 || len(r.Proposed) != 0 {
				t.Fatalf("report %+v, want not observed with nothing listed", r)
			}
		})
	}
}

func TestGenerateEveryPeerOfflineIsStillObserved(t *testing.T) {
	p := alpha()
	p.Online = false
	r := run(t, fixture(onlineTailnet(p)), genCfg(node("a", "100.64.0.10")))
	if !r.Observed || notSeen(r, "a") != notSeenOffline {
		t.Fatalf("observed %v, a %q; want observed and offline", r.Observed, notSeen(r, "a"))
	}
}

func TestGenerateSkippedPeers(t *testing.T) {
	peers := []fakePeer{
		{HostName: "sharee", IPs: []string{"100.64.0.71"}, Online: true, ShareeNode: true},
		{HostName: "sharedin", IPs: []string{"100.64.0.72"}, Online: true, AltSharerUserID: 42},
		{HostName: "foreign", DNSName: "foreign.other.example.com", IPs: []string{"100.64.0.73"}, Online: true},
		{HostName: "noaddr", Online: true},
	}
	r := run(t, fixture(onlineTailnet(peers...)), genCfg(node("known", "100.64.0.73")))
	if len(r.Proposed) != 0 || len(r.SkippedPeers) != 4 {
		t.Fatalf("proposed %v, skipped %+v; want all four skipped", proposalNames(r), r.SkippedPeers)
	}
	if notSeen(r, "known") != "" {
		t.Fatalf("a configured node at a skipped peer's IP was not seen: %q", notSeen(r, "known"))
	}
}

func TestGenerateSuffixSources(t *testing.T) {
	tn := onlineTailnet(alpha())
	tn.suffix, tn.legacySuffix = "", genSuffix+"."
	if r := run(t, fixture(tn), nil); r.TailnetSuffix != genSuffix {
		t.Fatalf("suffix %q, want the top-level fallback", r.TailnetSuffix)
	}
	owner := fakePeer{HostName: "owner-shared", DNSName: "o.other.example.com", IPs: []string{"100.64.0.81"}, Online: true}
	tn = onlineTailnet(alpha(), owner)
	tn.suffix, tn.legacySuffix = "", ""
	f := fixture(tn)
	f.dns = map[string][]string{"web1.example.com": {"192.0.2.80"}}
	r := run(t, f, genCfg(node("a", "100.64.0.10"), node("web1", "web1.example.com")))
	if len(r.Proposed) != 0 || len(r.NotProposed) != 1 || r.NotProposed[0].HostName != "owner-shared" {
		t.Fatalf("proposed %v, not proposed %+v; want nothing proposed without a suffix", proposalNames(r), r.NotProposed)
	}
	if notSeen(r, "a") != "" || notSeen(r, "web1") != "not observable" {
		t.Fatalf("a %q web1 %q; want a seen and web1 not judged as missing", notSeen(r, "a"), notSeen(r, "web1"))
	}
}

func TestGenerateHostnameChoice(t *testing.T) {
	// MagicDNS off: tailnet IP.
	tn := onlineTailnet(alpha())
	tn.magicDNS = false
	f := fixture(tn)
	f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}}
	if p := proposal(t, run(t, f, nil), "alpha"); p.Hostname != "100.64.0.10" {
		t.Fatalf("MagicDNS off: hostname %q", p.Hostname)
	}
	// DNSName does not resolve here: tailnet IP, withheld flag.
	p := proposal(t, run(t, fixture(onlineTailnet(alpha())), nil), "alpha")
	if p.Hostname != "100.64.0.10" || !hasFlag(p, "hostname-withheld: DNSName does not resolve") {
		t.Fatalf("unresolvable DNSName: %+v", p)
	}
	// DNSName resolves elsewhere (split DNS): not used.
	f = fixture(onlineTailnet(alpha()))
	f.dns = map[string][]string{"alpha." + genSuffix: {"192.0.2.10"}}
	if p := proposal(t, run(t, f, nil), "alpha"); p.Hostname != "100.64.0.10" {
		t.Fatalf("DNSName resolving elsewhere was used: %+v", p)
	}
	// DNSName resolves to the peer: used.
	f = fixture(onlineTailnet(alpha()))
	f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}}
	if p := proposal(t, run(t, f, nil), "alpha"); p.Hostname != "alpha."+genSuffix || len(p.Flags) != 0 {
		t.Fatalf("resolving DNSName: %+v", p)
	}
}

func TestGenerateLocality(t *testing.T) {
	tn := onlineTailnet(fakePeer{HostName: "hub", DNSName: "hub." + genSuffix, IPs: []string{"100.64.0.90"}, Online: true})
	f := fixture(tn)
	f.localCfg = "hub"
	f.dns = map[string][]string{"self-alias": {"100.64.0.1"}, "hub." + genSuffix: {"100.64.0.90"}, "box.example.com": {"192.0.2.5"}}
	nat := config.NodeConfig{Name: "nat", Hostname: "203.0.113.200", SSHUser: "operator"}
	r := run(t, f, genCfg(
		node("me-alias", "self-alias"),
		node("me-localhost", "localhost"),
		config.NodeConfig{Name: "me-stable", Hostname: "box.example.com", SSHUser: "operator", StableID: "local-stable-id"},
		node("hub", "hub.example.com"),
		nat,
	))
	for _, name := range []string{"me-alias", "me-localhost", "me-stable"} {
		if s := notSeen(r, name); s != "" {
			t.Errorf("local node %s listed as %q", name, s)
		}
	}
	if notSeen(r, "nat") == "" {
		t.Error("a node at a NAT address was treated as local")
	}
	if notSeen(r, "hub") == "" {
		t.Error("a remote hub.example.com was treated as this machine by its first label")
	}
	p := proposal(t, r, "hub-2")
	if p.Hostname != "100.64.0.90" || !hasFlag(p, "hostname-withheld: DNSName would be read as this machine") {
		t.Fatalf("proposal %+v, want hub-2 at its tailnet IP with the IsLocal flag", p)
	}
	if f.deps().isLocalConfig(config.NodeConfig{Hostname: p.Hostname}) {
		t.Fatal("the proposed hostname is still read as this machine")
	}
}

func TestGenerateRedirects(t *testing.T) {
	base := func() *genFixture {
		f := fixture(onlineTailnet(alpha()))
		f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}, "elsewhere.example.com": {"192.0.2.66"}}
		return f
	}
	f := base()
	f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: {Hostname: "elsewhere.example.com"}}
	p := proposal(t, run(t, f, nil), "alpha")
	if p.Hostname != "100.64.0.10" || p.Unsafe || !hasFlag(p, "hostname-withheld: DNSName redirected by SSH config (HostName)") || p.YAML == "" {
		t.Fatalf("redirect 1: %+v", p)
	}
	f = base()
	f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: {Hostname: "alpha." + genSuffix, ProxyJump: "jump.example.com"}}
	p = proposal(t, run(t, f, nil), "alpha")
	if p.Hostname != "100.64.0.10" || p.Unsafe || !hasFlag(p, "hostname-withheld: DNSName redirected by SSH config (ProxyJump); Axis transport refuses it") {
		t.Fatalf("redirect 1 (ProxyJump): %+v", p)
	}
	for field, target := range map[string]transport.SSHTarget{
		"HostName":     {Hostname: "elsewhere.example.com"},
		"ProxyJump":    {Hostname: "100.64.0.10", ProxyJump: "jump.example.com"},
		"ProxyCommand": {Hostname: "100.64.0.10", ProxyCommand: "nc %h %p"},
		"User":         {Hostname: "100.64.0.10", User: "root"},
		"HostKeyAlias": {Hostname: "100.64.0.10", HostKeyAlias: "other"},
	} {
		f := base()
		f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: target, "100.64.0.10": target}
		p := proposal(t, run(t, f, nil), "alpha")
		want := "unsafe: redirected by SSH config (" + field + ")"
		if target.ProxyJump != "" || target.ProxyCommand != "" {
			want += "; Axis transport refuses it"
		}
		if p.Hostname != "" || !p.Unsafe || p.YAML != "" || !hasFlag(p, want) {
			t.Fatalf("redirect 2 (%s): %+v", field, p)
		}
	}
	f = base()
	f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: {Hostname: "alpha." + genSuffix, Port: 2222}}
	p = proposal(t, run(t, f, nil), "alpha")
	if p.Hostname != "alpha."+genSuffix || p.Unsafe || !hasFlag(p, "port: SSH config sets Port 2222") {
		t.Fatalf("port only flagged: %+v", p)
	}
}

func TestGenerateAddressForms(t *testing.T) {
	v6 := netip.AddrFrom16([16]byte{0xfd, 0x7a, 0x11, 0x5c, 0xa1, 0xe0, 15: 0x10})
	long := strings.ToUpper(v6.StringExpanded())
	peer := fakePeer{HostName: "six", IPs: []string{v6.String()}, Online: true}
	mapped := fakePeer{HostName: "four", IPs: []string{"100.64.0.7"}, Online: true}
	f := fixture(onlineTailnet(peer, mapped))
	f.dns = map[string][]string{"mapped-name": {"::ffff:100.64.0.7"}}
	r := run(t, f, genCfg(node("six", long), node("four-literal", "::ffff:100.64.0.7"), node("four-dns", "mapped-name")))
	if len(r.Proposed) != 0 || len(r.NotSeen) != 0 || len(r.NotObservable) != 0 {
		t.Fatalf("proposed %v, not seen %+v, not observable %v; want every address form matched", proposalNames(r), r.NotSeen, r.NotObservable)
	}
}

func TestGenerateNaming(t *testing.T) {
	noHost := fakePeer{DNSName: "from-dns." + genSuffix, IPs: []string{"100.64.0.5"}, Online: true}
	nothing := fakePeer{IPs: []string{"100.64.0.6"}, Online: true}
	r := run(t, fixture(onlineTailnet(noHost, nothing)), nil)
	got := proposalNames(r)
	if len(got) != 2 || got[0] != "from-dns" || got[1] != "100-64-0-6" {
		t.Fatalf("names %v, want from-dns and 100-64-0-6", got)
	}
}

func TestGenerateYAMLLoadsUnderNodes(t *testing.T) {
	f := fixture(onlineTailnet(alpha()))
	f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}}
	r := buildGenerateReport(context.Background(), f.deps(), nil, "deploy")
	p := proposal(t, r, "alpha")
	if !strings.Contains(p.YAML, "ssh_user: deploy") || !strings.HasPrefix(p.YAML, "  # proposed by axis init --generate") {
		t.Fatalf("yaml block:\n%s", p.YAML)
	}
	path := filepath.Join(t.TempDir(), "nodes.yaml")
	body := "nodes:\n  - name: existing\n    hostname: localhost\n    ssh_user: operator\n" + p.YAML
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil || len(cfg.Nodes) != 2 || cfg.Nodes[1].Hostname != "alpha."+genSuffix {
		t.Fatalf("config.Load of the pasted block: %v, %+v", err, cfg)
	}
}

func TestGenerateResolutionBudget(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the resolution budget")
	}
	f := fixture(onlineTailnet(alpha()))
	f.hang = map[string]bool{"alpha." + genSuffix: true}
	f.sshHang = map[string]bool{"100.64.0.10": true}
	for i := range 20 {
		f.hang[string(rune('a'+i))+".slow.example.com"] = true
	}
	var nodes []config.NodeConfig
	for name := range f.hang {
		if strings.HasSuffix(name, ".slow.example.com") {
			nodes = append(nodes, node(strings.TrimSuffix(name, ".slow.example.com"), name))
		}
	}
	start := time.Now()
	r := run(t, f, genCfg(nodes...))
	if elapsed := time.Since(start); elapsed > generateResolveBudget+3*time.Second {
		t.Fatalf("report took %s, budget is %s", elapsed, generateResolveBudget)
	}
	if notSeen(r, "a") != notSeenUnresolved {
		t.Fatalf("a slow name is %q, want unresolved", notSeen(r, "a"))
	}
	p := proposal(t, r, "alpha")
	if p.Hostname != "" || !p.Unresolved || p.YAML != "" {
		t.Fatalf("proposal after the budget ran out: %+v", p)
	}
}

func TestGenerateTimeoutFallsBackOnlyWhenTheIPPasses(t *testing.T) {
	f := fixture(onlineTailnet(alpha()))
	f.hang = map[string]bool{"alpha." + genSuffix: true}
	p := proposal(t, run(t, f, nil), "alpha")
	if p.Hostname != "100.64.0.10" || !hasFlag(p, "hostname-withheld: DNSName check did not finish") {
		t.Fatalf("DNSName timed out, IP passes: %+v", p)
	}
	f = fixture(onlineTailnet(alpha()))
	f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}, "elsewhere.example.com": {"192.0.2.66"}}
	f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: {Hostname: "elsewhere.example.com"}}
	f.sshHang = map[string]bool{"100.64.0.10": true}
	p = proposal(t, run(t, f, nil), "alpha")
	if !p.Unsafe || p.Unresolved || p.Hostname != "" {
		t.Fatalf("DNSName redirected and IP timed out: %+v, want unsafe", p)
	}
}

func TestGenerateEffectiveHostDecides(t *testing.T) {
	beta := fakePeer{HostName: "beta", DNSName: "beta." + genSuffix, IPs: []string{"100.64.0.20"}, Online: true}
	for name, setup := range map[string]func(*genFixture){
		"dns": func(f *genFixture) { f.dns["alpha."+genSuffix] = []string{"100.64.0.20"} },
		"ssh -G": func(f *genFixture) {
			f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: {Hostname: "100.64.0.20"}}
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := fixture(onlineTailnet(alpha(), beta))
			f.dns = map[string][]string{"beta." + genSuffix: {"100.64.0.20"}}
			setup(f)
			r := run(t, f, genCfg(node("alpha", "alpha."+genSuffix)))
			names := proposalNames(r)
			if len(names) != 1 || !strings.HasPrefix(names[0], "alpha") {
				t.Fatalf("proposals %v, want only alpha's peer (the node dials beta)", names)
			}
		})
	}
	tn := onlineTailnet(fakePeer{HostName: "x", DNSName: "alpha.example.com", IPs: []string{"100.64.0.10"}, Online: true})
	tn.suffix = "example.com."
	r := run(t, fixture(tn), genCfg(node("a", "Alpha.Example.com.")))
	if len(r.Proposed) != 0 {
		t.Fatalf("an unresolved FQDN with a trailing dot was proposed again: %v", proposalNames(r))
	}
}

func TestGenerateCommand(t *testing.T) {
	newCmd := func() (*bytes.Buffer, func(args ...string) error) {
		out := &bytes.Buffer{}
		return out, func(args ...string) error {
			cmd := initCmd()
			cmd.SetOut(out)
			cmd.SetErr(out)
			cmd.SetArgs(args)
			cmd.SetContext(context.Background())
			return cmd.Execute()
		}
	}
	for _, args := range [][]string{{"--ssh-user", "x"}, {"--format", "json"}} {
		_, exec := newCmd()
		if err := exec(args...); ExitCode(err) != ExitErrGeneric {
			t.Fatalf("%v without --generate: exit %d", args, ExitCode(err))
		}
	}
	dir := t.TempDir()
	bad := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(bad, []byte("nodes: [unknown_key: 1"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, exec := newCmd()
	if err := exec("--generate", "--config", bad); ExitCode(err) != ExitErrConfigLoad {
		t.Fatalf("unloadable nodes.yaml: exit %d (%v)", ExitCode(err), err)
	}
	_, exec = newCmd()
	if err := exec("--generate", "--ssh-user", "two words", "--config", filepath.Join(dir, "none.yaml")); ExitCode(err) != ExitErrGeneric {
		t.Fatalf("invalid --ssh-user: exit %d", ExitCode(err))
	}
}

func TestRunInitGenerateNeverWrites(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nodes.yaml")
	body := []byte("nodes:\n  - name: a\n    hostname: localhost\n    ssh_user: operator\n")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	before, _ := os.Stat(path)
	f := fixture(onlineTailnet(alpha()))
	cmd := initCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetContext(context.Background())
	if err := cmd.Flags().Set("config", path); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Flags().Set("format", "json"); err != nil {
		t.Fatal(err)
	}
	if err := runInitGenerate(cmd, f.deps(), func() string { return "operator" }); err != nil {
		t.Fatalf("runInitGenerate: %v", err)
	}
	after, _ := os.Stat(path)
	got, _ := os.ReadFile(path)
	if !bytes.Equal(got, body) || !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("nodes.yaml changed")
	}
	var r generateReport
	if err := json.Unmarshal(out.Bytes(), &r); err != nil || !r.Observed || len(r.Proposed) != 1 {
		t.Fatalf("json report: %v %+v", err, r)
	}
}

// A failed ssh -G leaves the redirect fields unchecked, so the candidate fails
// instead of passing as written.
func TestGenerateSSHConfigErrorWithholdsTheHostname(t *testing.T) {
	f := fixture(onlineTailnet(alpha()))
	f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}}
	f.sshFail = map[string]bool{"alpha." + genSuffix: true, "100.64.0.10": true}
	p := proposal(t, run(t, f, nil), "alpha")
	if p.Hostname != "" || !p.Unsafe || p.YAML != "" || !hasFlag(p, "unsafe: SSH config could not be checked") {
		t.Fatalf("ssh -G failed for every candidate: %+v, want unsafe with no YAML", p)
	}
	// A configured node keeps transport's fallback: it is still matched by its name.
	f = fixture(onlineTailnet(alpha()))
	f.dns = map[string][]string{"alpha." + genSuffix: {"100.64.0.10"}}
	f.sshFail = map[string]bool{"alpha." + genSuffix: true}
	if r := run(t, f, genCfg(node("a", "alpha."+genSuffix))); len(r.Proposed) != 0 || notSeen(r, "a") != "" {
		t.Fatalf("configured node with a failing ssh -G was not matched: %v %q", proposalNames(r), notSeen(r, "a"))
	}
}

func TestGenerateLocalHostnameWithTrailingDot(t *testing.T) {
	f := fixture(onlineTailnet())
	f.local["box.example.com"] = true
	r := run(t, f, genCfg(node("me", "box.example.com.")))
	if s := notSeen(r, "me"); s != "" {
		t.Fatalf("this machine's FQDN with a trailing dot was listed as %q", s)
	}
}

// A redirect seen in ssh -G stays a redirect when the DNS lookup after it times out.
func TestGenerateObservedRedirectSurvivesALookupTimeout(t *testing.T) {
	f := fixture(onlineTailnet(alpha()))
	f.ssh = map[string]transport.SSHTarget{"alpha." + genSuffix: {Hostname: "slow.example.com", ProxyJump: "jump.example.com"}}
	f.hang = map[string]bool{"slow.example.com": true}
	f.sshHang = map[string]bool{"100.64.0.10": true}
	p := proposal(t, run(t, f, nil), "alpha")
	if !p.Unsafe || p.Unresolved || !hasFlag(p, "unsafe: redirected by SSH config (ProxyJump)") {
		t.Fatalf("redirect then timeout: %+v, want unsafe (ProxyJump)", p)
	}
}

// Only an SSH-config cause makes a proposal unsafe; a DNSName that does not
// resolve plus a timed-out IP check is unresolved.
func TestGenerateUnresolvableDNSNameThenTimeoutIsUnresolved(t *testing.T) {
	f := fixture(onlineTailnet(alpha()))
	f.sshHang = map[string]bool{"100.64.0.10": true}
	p := proposal(t, run(t, f, nil), "alpha")
	if p.Unsafe || !p.Unresolved || p.YAML != "" {
		t.Fatalf("NXDOMAIN then timeout: %+v, want unresolved", p)
	}
}

func TestGenerateNoEndpointFlagForThisMachinesNode(t *testing.T) {
	remote := fakePeer{HostName: "remote", IPs: []string{"100.64.0.33"}, Online: true, Addrs: []string{"10.0.0.10:41641"}}
	f := fixture(onlineTailnet(remote))
	f.local["10.0.0.10"] = true
	r := run(t, f, genCfg(node("me", "10.0.0.10")))
	if p := proposal(t, r, "remote"); len(p.Flags) != 0 {
		t.Fatalf("remote peer flagged against this machine's own node: %+v", p)
	}
}

// generateCmd runs axis init --generate's handler with stubbed dependencies.
func generateCmd(t *testing.T, f *genFixture, defaultUser string, args map[string]string) (string, error) {
	t.Helper()
	cmd := initCmd()
	out := &bytes.Buffer{}
	cmd.SetOut(out)
	cmd.SetContext(context.Background())
	for k, v := range args {
		if err := cmd.Flags().Set(k, v); err != nil {
			t.Fatal(err)
		}
	}
	err := runInitGenerate(cmd, f.deps(), func() string { return defaultUser })
	return out.String(), err
}

func TestGenerateCommandOutputAndUser(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "none.yaml")
	withUser := filepath.Join(dir, "nodes.yaml")
	if err := os.WriteFile(withUser, []byte("nodes:\n  - name: a\n    hostname: localhost\n    ssh_user: fromconfig\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	safe := fakePeer{HostName: "ok", IPs: []string{"100.64.0.10"}, Online: true}
	bad := fakePeer{HostName: "bad", IPs: []string{"100.64.0.11"}, Online: true}
	f := fixture(onlineTailnet(safe, bad))
	f.ssh = map[string]transport.SSHTarget{"100.64.0.11": {Hostname: "100.64.0.11", ProxyJump: "jump.example.com"}}

	text, err := generateCmd(t, f, "fallback", map[string]string{"config": missing})
	if err != nil {
		t.Fatalf("no nodes.yaml: %v", err)
	}
	if !strings.Contains(text, "  - name: ok") || !strings.Contains(text, "ssh_user: fallback") ||
		!strings.Contains(text, "(no YAML: no hostname passed the checks)") || !strings.Contains(text, "unsafe: redirected by SSH config (ProxyJump)") {
		t.Fatalf("text report:\n%s", text)
	}
	if strings.Count(text, "  - name:") != 1 {
		t.Fatalf("an unsafe proposal printed a YAML block:\n%s", text)
	}
	for args, want := range map[[2]string]string{
		{withUser, ""}:       "ssh_user: fromconfig",
		{withUser, "deploy"}: "ssh_user: deploy",
	} {
		flags := map[string]string{"config": args[0]}
		if args[1] != "" {
			flags["ssh-user"] = args[1]
		}
		if text, err := generateCmd(t, f, "fallback", flags); err != nil || !strings.Contains(text, want) {
			t.Fatalf("user precedence %v: err %v, want %q in\n%s", args, err, want, text)
		}
	}

	down := &genFixture{status: func(context.Context) ([]byte, error) { return nil, errors.New("not running") }, local: map[string]bool{}}
	text, err = generateCmd(t, down, "fallback", map[string]string{"config": missing})
	if ExitCode(err) != ExitErrGeneric || !strings.Contains(text, "Tailscale not observed") {
		t.Fatalf("not observed: exit %d, text %q", ExitCode(err), text)
	}
}

// The wizard's Tailscale scan reads the same status through the same seam.
func TestDiscoverTailscalePeersUsesTheSharedStatus(t *testing.T) {
	tn := onlineTailnet(alpha(), fakePeer{HostName: "off", IPs: []string{"100.64.0.12"}, Online: false})
	prev := tailscaleStatusJSONFn
	tailscaleStatusJSONFn = func(context.Context) ([]byte, error) { return tn.json(), nil }
	t.Cleanup(func() { tailscaleStatusJSONFn = prev })
	peers, err := discoverTailscalePeers(context.Background())
	if err != nil || len(peers) != 1 || peers[0].Name != "alpha" || peers[0].Hostname != "100.64.0.10" {
		t.Fatalf("discoverTailscalePeers = %+v, %v; want only the online peer at its first tailnet IP", peers, err)
	}
}
