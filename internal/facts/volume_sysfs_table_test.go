package facts

import (
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

func TestParseSysfsBlockTable(t *testing.T) {
	const table = `nvme0n1 0 0 -
sda 0 1 5000
sdb 1 0 -
`
	got := ParseSysfsBlockTable(table)
	if got["nvme0n1"].Class != "nvme" || got["nvme0n1"].Bus != "nvme" {
		t.Fatalf("nvme: %+v", got["nvme0n1"])
	}
	if got["sda"].Bus != "usb" || got["sda"].Class != "ssd" || !got["sda"].Removable || got["sda"].LinkMbit != 5000 {
		t.Fatalf("usb ssd: %+v", got["sda"])
	}
	if got["sdb"].Bus != "sata" || got["sdb"].Class != "hdd" {
		t.Fatalf("hdd: %+v", got["sdb"])
	}
}

func TestApplySysfsBlockTableToVolumes(t *testing.T) {
	vols := []models.Volume{
		{Device: "/dev/sda2", Mount: "/mnt/models", Kind: "local"},
		{Device: "//nas/share", Mount: "/mnt/nas", Kind: "network", Bus: "cifs"},
	}
	ApplySysfsBlockTable(vols, "sda 0 1 5000\n")
	if vols[0].Bus != "usb" || vols[0].Class != "ssd" || vols[0].LinkMbit != 5000 {
		t.Fatalf("local: %+v", vols[0])
	}
	if vols[1].Bus != "cifs" || vols[1].LinkMbit != 0 {
		t.Fatalf("network must stay unobserved: %+v", vols[1])
	}
}
