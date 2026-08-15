package snapshot

import (
	"net/netip"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// JoinNetworkVolumeOwners annotates network volumes with the owning cluster
// node when Device names a unique hostname or address. Sizes stay 0.
// Bus, class, removable, and link_mbit are copied from the owner's local row.
func JoinNetworkVolumeOwners(nodes []models.NodeFacts) {
	index := buildOwnerIndex(nodes)
	byName := make(map[string]*models.NodeFacts, len(nodes))
	for i := range nodes {
		byName[nodes[i].Name] = &nodes[i]
	}
	for i := range nodes {
		res := nodes[i].Resources
		if res == nil {
			continue
		}
		for j := range res.Volumes {
			v := &res.Volumes[j]
			if v.Kind != "network" {
				continue
			}
			host := shareHost(v.Device)
			if host == "" {
				continue
			}
			matches := index[host]
			if len(matches) != 1 {
				continue
			}
			v.Owner = matches[0].name
			v.OwnerMount = matches[0].root
			copyOwnerObservation(v, byName[v.Owner])
		}
	}
}

func copyOwnerObservation(dst *models.Volume, owner *models.NodeFacts) {
	if dst == nil || owner == nil || owner.Resources == nil {
		return
	}
	want := dst.OwnerMount
	if want == "" {
		want = "/"
	}
	for _, src := range owner.Resources.Volumes {
		if src.Kind == "network" || src.Mount != want {
			continue
		}
		if src.Bus != "" {
			dst.Bus = src.Bus
		}
		if src.Class != "" {
			dst.Class = src.Class
		}
		dst.Removable = src.Removable
		dst.LinkMbit = src.LinkMbit
		return
	}
}

type ownerHit struct {
	name string
	root string
}

func buildOwnerIndex(nodes []models.NodeFacts) map[string][]ownerHit {
	idx := make(map[string][]ownerHit)
	for _, n := range nodes {
		hit := ownerHit{name: n.Name, root: ownerRootMount(n)}
		addOwnerKey(idx, n.Name, hit)
		addOwnerKey(idx, n.Hostname, hit)
		addOwnerKey(idx, n.SSHTarget, hit)
		addOwnerKey(idx, n.ResolvedDialTarget, hit)
		for _, addr := range n.Addresses {
			addOwnerKey(idx, addr.Address, hit)
		}
	}
	return idx
}

func addOwnerKey(idx map[string][]ownerHit, raw string, hit ownerHit) {
	for _, key := range ownerKeys(raw) {
		list := idx[key]
		if len(list) > 0 && list[len(list)-1].name == hit.name {
			continue
		}
		idx[key] = append(list, hit)
	}
}

func ownerKeys(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if i := strings.LastIndex(raw, "@"); i >= 0 && !strings.Contains(raw, "/") {
		raw = raw[i+1:]
	}
	host := shareHost(raw)
	if host == "" {
		host = normalizeOwnerHost(raw)
	}
	if host == "" {
		return nil
	}
	keys := []string{host}
	if i := strings.Index(host, "."); i > 0 {
		if _, err := netip.ParseAddr(host); err != nil {
			keys = append(keys, host[:i])
		}
	}
	return keys
}

func ownerRootMount(n models.NodeFacts) string {
	if n.Resources == nil {
		return "/"
	}
	for _, v := range n.Resources.Volumes {
		if v.Role == "root" || v.Mount == "/" {
			return "/"
		}
	}
	return "/"
}

func shareHost(device string) string {
	device = strings.TrimSpace(device)
	if device == "" {
		return ""
	}
	if strings.HasPrefix(device, "//") {
		rest := strings.TrimPrefix(device, "//")
		if i := strings.Index(rest, "/"); i >= 0 {
			rest = rest[:i]
		}
		if i := strings.LastIndex(rest, "@"); i >= 0 {
			rest = rest[i+1:]
		}
		return normalizeOwnerHost(rest)
	}
	if i := strings.Index(device, ":/"); i > 0 {
		return normalizeOwnerHost(device[:i])
	}
	return normalizeOwnerHost(device)
}

func normalizeOwnerHost(host string) string {
	host = strings.TrimSpace(strings.ToLower(host))
	host = strings.TrimSuffix(host, ".")
	for _, suf := range []string{"._smb._tcp.local", "._afpovertcp._tcp.local", ".local"} {
		host = strings.TrimSuffix(host, suf)
	}

	return host
}
