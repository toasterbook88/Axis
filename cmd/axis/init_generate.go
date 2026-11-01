package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/netutil"
	"github.com/toasterbook88/axis/internal/transport"
	"gopkg.in/yaml.v3"
)

// Design: docs/superpowers/specs/2026-10-31-nodes-yaml-generate-design.md.
// axis init --generate reads Tailscale and nodes.yaml and reports; it never
// writes nodes.yaml.

const (
	generateStatusTimeout = 5 * time.Second
	generateResolveBudget = 10 * time.Second
	generateLookupTimeout = 2 * time.Second
	generateConcurrency   = 8
	generateDefaultPort   = 22
)

type generateDependencies struct {
	tailscaleStatus func(context.Context) ([]byte, error)
	resolveSSH      func(ctx context.Context, host string, port int, user string) (transport.SSHTarget, error)
	lookup          func(ctx context.Context, host string) ([]netip.Addr, error)
	isLocalHost     func(string) bool
	isLocalConfig   func(config.NodeConfig) bool
	localStableID   func() string
	now             func() time.Time
}

func defaultGenerateDependencies() generateDependencies {
	return generateDependencies{
		tailscaleStatus: tailscaleStatusJSON,
		resolveSSH:      transport.ResolveSSHTarget,
		lookup: func(ctx context.Context, host string) ([]netip.Addr, error) {
			return net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		},
		isLocalHost:   models.IsLocalHost,
		isLocalConfig: func(n config.NodeConfig) bool { return n.IsLocal() },
		localStableID: models.CurrentLocalStableID,
		now:           time.Now,
	}
}

func tailscaleStatusJSON(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "tailscale", "status", "--json").Output()
}

// tailscaleStatus holds the fields of `tailscale status --json` this report
// reads (ipnstate.Status and ipnstate.PeerStatus upstream).
type tailscaleStatus struct {
	BackendState   string
	Self           *tailscalePeer
	Peer           map[string]*tailscalePeer
	MagicDNSSuffix string
	CurrentTailnet *struct {
		MagicDNSSuffix  string
		MagicDNSEnabled bool
	}
}

type tailscalePeer struct {
	HostName        string
	DNSName         string
	TailscaleIPs    []string
	Online          *bool
	Addrs           []string
	CurAddr         string
	ShareeNode      bool
	AltSharerUserID int64
}

type generateReport struct {
	Observed          bool                 `json:"observed"`
	NotObservedReason string               `json:"not_observed_reason,omitempty"`
	TailnetSuffix     string               `json:"tailnet_suffix,omitempty"`
	MagicDNSEnabled   bool                 `json:"magicdns_enabled"`
	Proposed          []generateProposal   `json:"proposed"`
	NotProposed       []generatePeerNote   `json:"not_proposed"`
	OfflinePeers      []generatePeerNote   `json:"offline_peers"`
	SkippedPeers      []generatePeerNote   `json:"skipped_peers"`
	NotSeen           []generateNodeStatus `json:"not_seen"`
	NotObservable     []string             `json:"not_observable"`
}

type generateProposal struct {
	Name        string   `json:"name"`
	Hostname    string   `json:"hostname"`
	SSHUser     string   `json:"ssh_user"`
	TailscaleIP string   `json:"tailscale_ip"`
	Flags       []string `json:"flags"`
	Unsafe      bool     `json:"unsafe"`
	Unresolved  bool     `json:"unresolved"`
	YAML        string   `json:"yaml"`
}

type generatePeerNote struct {
	HostName    string `json:"hostname,omitempty"`
	DNSName     string `json:"dns_name,omitempty"`
	TailscaleIP string `json:"tailscale_ip,omitempty"`
	Reason      string `json:"reason"`
}

type generateNodeStatus struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Detail string `json:"detail,omitempty"`
}

const (
	notSeenOffline    = "offline"
	notSeenNotInMap   = "not in this machine's netmap"
	notSeenUnresolved = "unresolved"
)

type generatePeer struct {
	hostName  string
	dnsName   string
	ips       []netip.Addr
	endpoints []netip.Addr
	online    bool
	skip      string
	matched   bool
}

type generateNode struct {
	cfg             config.NodeConfig
	ips             map[netip.Addr]bool
	unresolvedNames map[string]bool
	unresolved      []string
	effectiveHosts  []string
	local           bool
	onlineMatch     bool
	offlineMatch    bool
}

// normalizeGenerateName compares names as DNS does: one trailing dot is the
// root, and case does not matter.
func normalizeGenerateName(name string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(name), "."))
}

func parseUnmapped(s string) (netip.Addr, bool) {
	ip, err := netip.ParseAddr(strings.TrimSpace(s))
	if err != nil {
		return netip.Addr{}, false
	}
	return ip.Unmap(), true
}

// generateBudget limits concurrent resolution and bounds it in time. Every
// lookup, for configured nodes and proposals alike, runs under it.
type generateBudget struct {
	ctx context.Context
	sem chan struct{}
}

func (b generateBudget) run(jobs []func(context.Context)) {
	var wg sync.WaitGroup
	for _, job := range jobs {
		wg.Add(1)
		go func(job func(context.Context)) {
			defer wg.Done()
			select {
			case b.sem <- struct{}{}:
				defer func() { <-b.sem }()
			case <-b.ctx.Done():
			}
			job(b.ctx)
		}(job)
	}
	wg.Wait()
}

type lookupResult struct {
	effectiveHost string
	ips           []netip.Addr
	resolved      bool
	timedOut      bool
	target        transport.SSHTarget
	sshErr        error // ssh -G failed (not by timeout); target is empty
}

// resolveEffective resolves a name the way transport dials it: `ssh -G`, then
// Go's resolver on the resulting HostName (the resolver transport's dialer
// uses). An ssh -G failure leaves the name as written, as transport does.
func resolveEffective(ctx context.Context, deps generateDependencies, name string, port int, user string) lookupResult {
	res := lookupResult{effectiveHost: name}
	if ctx.Err() != nil {
		res.timedOut = true
		return res
	}
	target, err := deps.resolveSSH(ctx, name, port, user)
	if ctx.Err() != nil {
		res.timedOut = true
		return res
	}
	if err != nil {
		res.sshErr = err
	} else {
		res.target = target
		if h := strings.TrimSpace(target.Hostname); h != "" {
			res.effectiveHost = h
		}
	}
	if ip, ok := parseUnmapped(res.effectiveHost); ok {
		res.ips, res.resolved = []netip.Addr{ip}, true
		return res
	}
	lookupCtx, cancel := context.WithTimeout(ctx, generateLookupTimeout)
	defer cancel()
	addrs, err := deps.lookup(lookupCtx, res.effectiveHost)
	if lookupCtx.Err() != nil {
		res.timedOut = true
		return res
	}
	if err != nil || len(addrs) == 0 {
		return res
	}
	for _, a := range addrs {
		res.ips = append(res.ips, a.Unmap())
	}
	res.resolved = true
	return res
}

// buildGenerateReport runs the whole report against cfg (nil or empty when
// there is no nodes.yaml). It never writes.
func buildGenerateReport(ctx context.Context, deps generateDependencies, cfg *config.Config, sshUser string) generateReport {
	report := generateReport{Proposed: []generateProposal{}, NotProposed: []generatePeerNote{},
		OfflinePeers: []generatePeerNote{}, SkippedPeers: []generatePeerNote{}, NotSeen: []generateNodeStatus{},
		NotObservable: []string{}}

	statusCtx, cancelStatus := context.WithTimeout(ctx, generateStatusTimeout)
	raw, err := deps.tailscaleStatus(statusCtx)
	timedOut := statusCtx.Err() != nil
	cancelStatus()
	if timedOut {
		report.NotObservedReason = fmt.Sprintf("tailscale status did not answer within %s", generateStatusTimeout)
		return report
	}
	if err != nil {
		report.NotObservedReason = "tailscale status failed: " + err.Error()
		return report
	}
	var st tailscaleStatus
	if err := json.Unmarshal(raw, &st); err != nil {
		report.NotObservedReason = "tailscale status output did not parse: " + err.Error()
		return report
	}
	if st.BackendState != "Running" {
		report.NotObservedReason = fmt.Sprintf("Tailscale is not running (BackendState %q)", st.BackendState)
		return report
	}
	if st.Self == nil || st.Self.Online == nil || !*st.Self.Online {
		report.NotObservedReason = "this machine is not connected to the Tailscale control plane (Self.Online is not true)"
		return report
	}
	report.Observed = true

	suffix := normalizeGenerateName(st.MagicDNSSuffix)
	if st.CurrentTailnet != nil {
		if s := normalizeGenerateName(st.CurrentTailnet.MagicDNSSuffix); s != "" {
			suffix = s
		}
		report.MagicDNSEnabled = st.CurrentTailnet.MagicDNSEnabled
	}
	report.TailnetSuffix = suffix
	underSuffix := func(name string) bool {
		return suffix != "" && strings.HasSuffix(normalizeGenerateName(name), "."+suffix)
	}

	selfIPs := map[netip.Addr]bool{}
	for _, s := range st.Self.TailscaleIPs {
		if ip, ok := parseUnmapped(s); ok {
			selfIPs[ip] = true
		}
	}
	selfDNS := normalizeGenerateName(st.Self.DNSName)

	peers := make([]*generatePeer, 0, len(st.Peer))
	for _, p := range st.Peer {
		if p == nil {
			continue
		}
		gp := &generatePeer{hostName: p.HostName, dnsName: strings.TrimSuffix(strings.TrimSpace(p.DNSName), "."),
			online: p.Online != nil && *p.Online}
		for _, s := range p.TailscaleIPs {
			if ip, ok := parseUnmapped(s); ok {
				gp.ips = append(gp.ips, ip)
			}
		}
		seenEndpoint := map[netip.Addr]bool{}
		for _, s := range append(append([]string{}, p.Addrs...), p.CurAddr) {
			if ap, err := netip.ParseAddrPort(strings.TrimSpace(s)); err == nil {
				if ip := ap.Addr().Unmap(); !seenEndpoint[ip] {
					seenEndpoint[ip] = true
					gp.endpoints = append(gp.endpoints, ip)
				}
			}
		}
		switch {
		case p.ShareeNode:
			gp.skip = "owned by a user this tailnet shared a node to (ShareeNode)"
		case p.AltSharerUserID != 0:
			gp.skip = "shared into this tailnet by another user (AltSharerUserID)"
		case len(gp.ips) == 0:
			gp.skip = "no tailnet address"
		case suffix != "" && gp.dnsName != "" && !underSuffix(gp.dnsName):
			gp.skip = "DNS name outside this tailnet's suffix"
		}
		peers = append(peers, gp)
	}
	sort.SliceStable(peers, func(i, j int) bool { return firstIP(peers[i]).Less(firstIP(peers[j])) })

	budgetCtx, cancelBudget := context.WithTimeout(ctx, generateResolveBudget)
	defer cancelBudget()
	budget := generateBudget{ctx: budgetCtx, sem: make(chan struct{}, generateConcurrency)}

	var nodes []*generateNode
	if cfg != nil {
		for i := range cfg.Nodes {
			nodes = append(nodes, &generateNode{cfg: cfg.Nodes[i], ips: map[netip.Addr]bool{}, unresolvedNames: map[string]bool{}})
		}
	}
	var mu sync.Mutex
	var jobs []func(context.Context)
	for _, n := range nodes {
		port, user := n.cfg.EffectiveSSHPort(), n.cfg.SSHUser
		for _, name := range n.cfg.DialHostnames() {
			jobs = append(jobs, func(ctx context.Context) {
				res := resolveEffective(ctx, deps, name, port, user)
				mu.Lock()
				defer mu.Unlock()
				n.effectiveHosts = append(n.effectiveHosts, res.effectiveHost)
				if res.resolved {
					for _, ip := range res.ips {
						n.ips[ip] = true
					}
					return
				}
				n.unresolvedNames[normalizeGenerateName(res.effectiveHost)] = true
				n.unresolved = append(n.unresolved, name)
			})
		}
	}
	budget.run(jobs)

	localStable := models.NormalizeStableID(deps.localStableID())
	for _, n := range nodes {
		n.local = nodeIsLocal(n, deps, localStable, selfIPs, selfDNS)
		sort.Strings(n.unresolved)
	}

	for _, p := range peers {
		for _, n := range nodes {
			if !peerMatchesNode(p, n) {
				continue
			}
			p.matched = true
			if p.online {
				n.onlineMatch = true
			} else {
				n.offlineMatch = true
			}
		}
	}

	for _, n := range nodes {
		if n.local || n.onlineMatch {
			continue
		}
		judged := false
		for ip := range n.ips {
			if netutil.IsTailscaleAddr(ip) {
				judged = true
			}
		}
		for name := range n.unresolvedNames {
			if underSuffix(name) {
				judged = true
			}
		}
		switch {
		case n.offlineMatch:
			report.NotSeen = append(report.NotSeen, generateNodeStatus{Name: n.cfg.Name, Status: notSeenOffline})
		case len(n.unresolved) > 0:
			report.NotSeen = append(report.NotSeen, generateNodeStatus{Name: n.cfg.Name, Status: notSeenUnresolved,
				Detail: "no address for " + strings.Join(n.unresolved, ", ") + " within the resolution budget"})
		case judged:
			report.NotSeen = append(report.NotSeen, generateNodeStatus{Name: n.cfg.Name, Status: notSeenNotInMap,
				Detail: "Tailscale trims each device's netmap by ACL; the device may still exist"})
		default:
			report.NotObservable = append(report.NotObservable, n.cfg.Name)
		}
	}

	endpointPeers := map[netip.Addr]int{}
	for _, p := range peers {
		for _, ep := range p.endpoints {
			endpointPeers[ep]++
		}
	}

	var candidates []*generatePeer
	for _, p := range peers {
		note := generatePeerNote{HostName: p.hostName, DNSName: p.dnsName, TailscaleIP: firstIPString(p)}
		switch {
		case p.skip != "":
			note.Reason = p.skip
			report.SkippedPeers = append(report.SkippedPeers, note)
		case p.matched:
		case !p.online:
			note.Reason = "offline"
			report.OfflinePeers = append(report.OfflinePeers, note)
		case suffix == "":
			note.Reason = "not proposed: tailnet membership not established (no MagicDNS suffix)"
			report.NotProposed = append(report.NotProposed, note)
		default:
			candidates = append(candidates, p)
		}
	}

	taken := map[string]bool{}
	for _, n := range nodes {
		taken[strings.ToLower(n.cfg.Name)] = true
	}
	proposals := make([]generateProposal, len(candidates))
	var proposalJobs []func(context.Context)
	for i, p := range candidates {
		base := proposalBaseName(p)
		name, flags := base, []string{}
		for k := 2; taken[strings.ToLower(name)]; k++ {
			name = fmt.Sprintf("%s-%d", base, k)
		}
		if name != base {
			flags = append(flags, "possible duplicate of "+base)
		}
		taken[strings.ToLower(name)] = true
		flags = append(flags, endpointDuplicateFlags(p, nodes, endpointPeers)...)
		proposals[i] = generateProposal{Name: name, SSHUser: sshUser, TailscaleIP: firstIPString(p), Flags: flags}
		proposalJobs = append(proposalJobs, func(ctx context.Context) {
			chooseProposalHostname(ctx, deps, &proposals[i], p, report.MagicDNSEnabled, selfIPs)
		})
	}
	// Each proposal checks its candidates in order, so it takes one slot.
	budget.run(proposalJobs)

	date := deps.now().UTC().Format("2006-01-02")
	for i := range proposals {
		if proposals[i].Hostname != "" {
			proposals[i].YAML = proposalYAML(proposals[i], date)
		}
	}
	report.Proposed = proposals
	return report
}

func firstIP(p *generatePeer) netip.Addr {
	if len(p.ips) == 0 {
		return netip.Addr{}
	}
	return p.ips[0]
}

func firstIPString(p *generatePeer) string {
	if len(p.ips) == 0 {
		return ""
	}
	return p.ips[0].String()
}

func nodeIsLocal(n *generateNode, deps generateDependencies, localStable string, selfIPs map[netip.Addr]bool, selfDNS string) bool {
	if localStable != "" && models.NormalizeStableID(n.cfg.StableID) == localStable {
		return true
	}
	for ip := range n.ips {
		if selfIPs[ip] {
			return true
		}
	}
	if selfDNS != "" && n.unresolvedNames[selfDNS] {
		return true
	}
	for _, name := range append(n.cfg.DialHostnames(), n.effectiveHosts...) {
		if deps.isLocalHost(normalizeGenerateName(name)) {
			return true
		}
	}
	return false
}

func peerMatchesNode(p *generatePeer, n *generateNode) bool {
	for _, ip := range p.ips {
		if n.ips[ip] {
			return true
		}
	}
	return p.dnsName != "" && n.unresolvedNames[normalizeGenerateName(p.dnsName)]
}

func proposalBaseName(p *generatePeer) string {
	if label, _, _ := strings.Cut(p.dnsName, "."); label != "" {
		if name := normalizeSuggestedName(label); name != "" {
			return name
		}
	}
	if name := normalizeSuggestedName(p.hostName); name != "" {
		return name
	}
	return strings.NewReplacer(".", "-", ":", "-").Replace(firstIPString(p))
}

func endpointDuplicateFlags(p *generatePeer, nodes []*generateNode, endpointPeers map[netip.Addr]int) []string {
	var flags []string
	seen := map[string]bool{}
	for _, ep := range p.endpoints {
		if !ep.IsPrivate() || netutil.IsTailscaleAddr(ep) || endpointPeers[ep] != 1 {
			continue
		}
		for _, n := range nodes {
			if n.ips[ep] && !seen[n.cfg.Name] {
				seen[n.cfg.Name] = true
				flags = append(flags, "possible duplicate of "+n.cfg.Name+" (by LAN endpoint)")
			}
		}
	}
	return flags
}

type hostnameCheck struct {
	ok       bool
	timedOut bool
	reason   string // why a completed check failed
	port     int
}

// checkProposalHostname resolves cand as transport would dial it for this
// proposal and accepts it only when the dial host is one of the peer's own
// tailnet IPs and no ssh_config rewrite sends transport elsewhere.
func checkProposalHostname(ctx context.Context, deps generateDependencies, cand string, user string, peer *generatePeer) hostnameCheck {
	res := resolveEffective(ctx, deps, cand, generateDefaultPort, user)
	check := hostnameCheck{}
	if res.sshErr != nil {
		check.reason = "SSH config could not be checked (ssh -G failed: " + res.sshErr.Error() + ")"
		return check
	}
	if res.target.Port != 0 && res.target.Port != generateDefaultPort {
		check.port = res.target.Port
	}
	// Redirect fields come from ssh -G, so they hold even when the DNS lookup
	// after it timed out.
	t := res.target
	switch {
	case t.ProxyJump != "":
		check.reason = "redirected by SSH config (ProxyJump)"
		return check
	case t.ProxyCommand != "":
		check.reason = "redirected by SSH config (ProxyCommand)"
		return check
	case t.User != "" && t.User != user:
		check.reason = "redirected by SSH config (User)"
		return check
	case t.HostKeyAlias != "":
		check.reason = "redirected by SSH config (HostKeyAlias)"
		return check
	}
	if res.timedOut {
		return hostnameCheck{timedOut: true, port: check.port}
	}
	rewritten := normalizeGenerateName(res.effectiveHost) != normalizeGenerateName(cand)
	if !res.resolved {
		if rewritten {
			check.reason = "redirected by SSH config (HostName)"
		} else {
			check.reason = "does not resolve from this machine"
		}
		return check
	}
	own := map[netip.Addr]bool{}
	for _, ip := range peer.ips {
		own[ip] = true
	}
	for _, ip := range res.ips {
		if !own[ip] {
			if rewritten {
				check.reason = "redirected by SSH config (HostName)"
			} else {
				check.reason = "resolves to an address that is not this peer's"
			}
			return check
		}
	}
	check.ok = true
	return check
}

func chooseProposalHostname(ctx context.Context, deps generateDependencies, prop *generateProposal, peer *generatePeer, magicDNS bool, selfIPs map[netip.Addr]bool) {
	var candidates []string
	if peer.dnsName != "" && magicDNS {
		peerIsSelf := false
		for _, ip := range peer.ips {
			if selfIPs[ip] {
				peerIsSelf = true
			}
		}
		if !peerIsSelf && deps.isLocalConfig(config.NodeConfig{Name: prop.Name, Hostname: peer.dnsName}) {
			prop.Flags = append(prop.Flags, "hostname-withheld: DNSName would be read as this machine by NodeConfig.IsLocal()")
		} else {
			candidates = append(candidates, peer.dnsName)
		}
	}
	candidates = append(candidates, firstIPString(peer))

	var redirect string
	for i, cand := range candidates {
		check := checkProposalHostname(ctx, deps, cand, prop.SSHUser, peer)
		if check.port != 0 {
			prop.Flags = append(prop.Flags, fmt.Sprintf("port: SSH config sets Port %d for %s", check.port, cand))
		}
		if check.ok {
			prop.Hostname = cand
			return
		}
		last := i == len(candidates)-1
		if check.timedOut {
			if !last {
				prop.Flags = append(prop.Flags, "hostname-withheld: DNSName check did not finish within the budget")
			}
			continue
		}
		if redirect == "" {
			redirect = check.reason
		}
		if !last {
			prop.Flags = append(prop.Flags, "hostname-withheld: DNSName "+check.reason)
		}
	}
	if redirect != "" {
		prop.Unsafe = true
		prop.Flags = append(prop.Flags, "unsafe: "+redirect)
		return
	}
	prop.Unresolved = true
	prop.Flags = append(prop.Flags, "unresolved: SSH-config and DNS check did not finish within the budget")
}

// proposalYAML encodes one proposal as a nodes: item, indented to paste under
// an existing nodes: key.
func proposalYAML(p generateProposal, date string) string {
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	_ = enc.Encode([]config.NodeConfig{{Name: p.Name, Hostname: p.Hostname, SSHUser: p.SSHUser}})
	_ = enc.Close()
	var out strings.Builder
	fmt.Fprintf(&out, "  # proposed by axis init --generate from Tailscale on %s\n", date)
	for _, line := range strings.Split(strings.TrimRight(buf.String(), "\n"), "\n") {
		out.WriteString("  " + line + "\n")
	}
	return out.String()
}

func writeGenerateReport(out io.Writer, r generateReport, format string) error {
	if format == "json" {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	if !r.Observed {
		_, err := fmt.Fprintf(out, "Tailscale not observed: %s\nNo node is reported as offline or missing.\n", r.NotObservedReason)
		return err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Tailnet suffix: %s (MagicDNS %s)\n\n", orNone(r.TailnetSuffix), enabledLabel(r.MagicDNSEnabled))
	fmt.Fprintf(&b, "Proposed entries (%d)\n", len(r.Proposed))
	for _, p := range r.Proposed {
		fmt.Fprintf(&b, "- %s (tailnet IP %s)\n", p.Name, p.TailscaleIP)
		for _, f := range p.Flags {
			fmt.Fprintf(&b, "    ! %s\n", f)
		}
		if p.YAML != "" {
			b.WriteString(p.YAML)
		} else {
			b.WriteString("    (no YAML: no hostname passed the checks)\n")
		}
	}
	writeNotes := func(title string, notes []generatePeerNote) {
		fmt.Fprintf(&b, "\n%s (%d)\n", title, len(notes))
		for _, n := range notes {
			fmt.Fprintf(&b, "- %s %s %s: %s\n", orNone(n.HostName), orNone(n.DNSName), orNone(n.TailscaleIP), n.Reason)
		}
	}
	writeNotes("Not proposed", r.NotProposed)
	writeNotes("Offline peers", r.OfflinePeers)
	writeNotes("Skipped peers", r.SkippedPeers)
	fmt.Fprintf(&b, "\nConfigured nodes not seen (%d)\n", len(r.NotSeen))
	for _, n := range r.NotSeen {
		if n.Detail != "" {
			fmt.Fprintf(&b, "- %s: %s (%s)\n", n.Name, n.Status, n.Detail)
		} else {
			fmt.Fprintf(&b, "- %s: %s\n", n.Name, n.Status)
		}
	}
	fmt.Fprintf(&b, "\nConfigured nodes not observable by Tailscale (%d)\n", len(r.NotObservable))
	for _, name := range r.NotObservable {
		fmt.Fprintf(&b, "- %s\n", name)
	}
	b.WriteString("\nNothing was written. Paste a YAML block under nodes: in nodes.yaml to add it.\n")
	_, err := io.WriteString(out, b.String())
	return err
}

func orNone(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// runInitGenerate is axis init --generate. Exit codes follow the spec's
// order: flag errors, then an unloadable nodes.yaml (2), then Tailscale not
// observed (1).
func runInitGenerate(cmd *cobra.Command, deps generateDependencies, defaultUser func() string) error {
	format, _ := cmd.Flags().GetString("format")
	if format != "text" && format != "json" {
		return ExitCodeError{Code: ExitErrGeneric, Message: fmt.Sprintf("--format must be text or json, got %q", format)}
	}
	sshUser, _ := cmd.Flags().GetString("ssh-user")
	if cmd.Flags().Changed("ssh-user") {
		if err := validateSSHUser(sshUser); err != nil {
			return ExitCodeError{Code: ExitErrGeneric, Message: "--ssh-user: " + err.Error()}
		}
	}
	cfgPath, _ := cmd.Flags().GetString("config")
	if strings.TrimSpace(cfgPath) == "" {
		cfgPath = config.DefaultConfigPath()
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return ExitCodeError{Code: ExitErrConfigLoad, Message: err.Error()}
		}
		cfg = &config.Config{}
	}
	if !cmd.Flags().Changed("ssh-user") {
		sshUser = defaultNodeUser(cfg, defaultUser())
	}
	report := buildGenerateReport(cmd.Context(), deps, cfg, sshUser)
	if err := writeGenerateReport(cmd.OutOrStdout(), report, format); err != nil {
		return err
	}
	if !report.Observed {
		return ExitCodeError{Code: ExitErrGeneric, Message: "Tailscale not observed: " + report.NotObservedReason}
	}
	return nil
}
