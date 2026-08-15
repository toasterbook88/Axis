package snapshot

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestJoinNetworkVolumeOwnersLinksShareToOwningNode(t *testing.T) {
	storage := models.NodeFacts{
		Name:     "storage",
		Hostname: "storage",
		Addresses: []models.NetworkAddress{
			{Kind: "ipv4", Address: "192.0.2.10"},
		},
		Resources: &models.Resources{
			Volumes: []models.Volume{
				{Device: "/dev/sda2", Mount: "/", TotalGB: 1000, FreeGB: 800, Kind: "local", Role: "root"},
			},
		},
	}
	viewer := models.NodeFacts{
		Name:     "viewer",
		Hostname: "viewer-host",
		Resources: &models.Resources{
			Volumes: []models.Volume{
				{Device: "/dev/disk3s1s1", Mount: "/", TotalGB: 200, FreeGB: 50, Kind: "local", Role: "root"},
				{Device: "//nas@STORAGE._smb._tcp.local/share", Mount: "/Volumes/share", Kind: "network", Bus: "cifs"},
				{Device: "192.0.2.10:/export", Mount: "/mnt/nfs", Kind: "network", Bus: "nfs"},
			},
		},
	}
	orphan := models.NodeFacts{
		Name: "orphan",
		Resources: &models.Resources{
			Volumes: []models.Volume{
				{Device: "//unknown-host/share", Mount: "/Volumes/x", Kind: "network", Bus: "cifs"},
			},
		},
	}

	JoinNetworkVolumeOwners([]models.NodeFacts{storage, viewer, orphan})

	cifs := viewer.Resources.Volumes[1]
	if cifs.Owner != "storage" || cifs.OwnerMount != "/" {
		t.Fatalf("cifs owner=%q mount=%q", cifs.Owner, cifs.OwnerMount)
	}
	if cifs.TotalGB != 0 || cifs.FreeGB != 0 {
		t.Fatalf("must not copy sizes onto the share row: %+v", cifs)
	}
	nfs := viewer.Resources.Volumes[2]
	if nfs.Owner != "storage" || nfs.OwnerMount != "/" {
		t.Fatalf("nfs owner=%q mount=%q", nfs.Owner, nfs.OwnerMount)
	}
	if orphan.Resources.Volumes[0].Owner != "" {
		t.Fatalf("unresolved share got owner %q", orphan.Resources.Volumes[0].Owner)
	}
}

func TestJoinNetworkVolumeOwnersSkipsAmbiguousHost(t *testing.T) {
	a := models.NodeFacts{
		Name:      "alpha",
		Hostname:  "shared",
		Resources: &models.Resources{Volumes: []models.Volume{{Mount: "/", Kind: "local", Role: "root"}}},
	}
	b := models.NodeFacts{
		Name:      "beta",
		Hostname:  "shared",
		Resources: &models.Resources{Volumes: []models.Volume{{Mount: "/", Kind: "local", Role: "root"}}},
	}
	viewer := models.NodeFacts{
		Name: "viewer",
		Resources: &models.Resources{
			Volumes: []models.Volume{{Device: "//shared/data", Mount: "/mnt/data", Kind: "network"}},
		},
	}
	JoinNetworkVolumeOwners([]models.NodeFacts{a, b, viewer})
	if viewer.Resources.Volumes[0].Owner != "" {
		t.Fatalf("ambiguous host must stay unresolved, got %q", viewer.Resources.Volumes[0].Owner)
	}
}
