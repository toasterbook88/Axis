package facts

import (
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
)

// Fixture is POSIX `df -kP` (no fstype). Virtual mounts must be dropped;
// real block and network mounts must appear with size, mount, and bus hint.
const sampleDFKP = `Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/nvme0n1p2   460800000 230400000 207360000      53% /
tmpfs              8388608         0   8388608       0% /tmp
devtmpfs           4096000         0   4096000       0% /dev
overlay           46080000  20000000  24000000      46% /var/lib/docker/overlay2/abc
/dev/sda1        104857600  10485760  89128960      11% /mnt/models
/dev/disk4s1     104857600  10485760  89128960      11% /Volumes/My Passport

//nas/share       524288000  52428800 440401920      11% /mnt/nas
192.168.1.105:/srv/nas 2097152000 10485760 1981808640       1% /mnt/ubuntu-nas
`

func TestParseDFVolumesKeepsRealMountsDropsVirtual(t *testing.T) {
	vols, err := ParseDFVolumes(sampleDFKP)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 5 {
		t.Fatalf("got %d volumes, want 5 (root, sda1, passport, cifs, nfs); %#v", len(vols), vols)
	}

	byMount := map[string]struct {
		device string
		kind   string
		bus    string
		role   string
		class  string
	}{}
	for _, v := range vols {
		byMount[v.Mount] = struct {
			device, kind, bus, role, class string
		}{v.Device, v.Kind, v.Bus, v.Role, v.Class}
		if v.TotalGB <= 0 || v.FreeGB < 0 {
			t.Fatalf("volume %s missing sizes: %+v", v.Mount, v)
		}
	}

	root := byMount["/"]
	if root.device != "/dev/nvme0n1p2" || root.role != "root" || root.class != "nvme" || root.bus != "nvme" {
		t.Fatalf("root = %+v", root)
	}
	other := byMount["/mnt/models"]
	if other.device != "/dev/sda1" || other.role != "other" {
		t.Fatalf("/mnt/models = %+v", other)
	}
	space := byMount["/Volumes/My Passport"]
	if space.device != "/dev/disk4s1" || space.role != "other" {
		t.Fatalf("space mount = %+v want /Volumes/My Passport", space)
	}

	cifs := byMount["/mnt/nas"]
	if cifs.kind != "network" || !strings.HasPrefix(cifs.device, "//") {
		t.Fatalf("cifs = %+v", cifs)
	}
	nfs := byMount["/mnt/ubuntu-nas"]
	if nfs.kind != "network" {
		t.Fatalf("nfs = %+v", nfs)
	}
	for _, virt := range []string{"/tmp", "/dev", "/var/lib/docker/overlay2/abc"} {
		if _, ok := byMount[virt]; ok {
			t.Fatalf("virtual mount %s must be omitted", virt)
		}
	}
}

func TestParseDFVolumesDropsSnapAndLoop(t *testing.T) {
	const out = `Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/nvme0n1p2   460800000 230400000 207360000      53% /
/dev/loop0         65536       65536         0     100% /snap/core22/1380
/dev/loop1        131072      131072         0     100% /snap/lxd/29351
`
	vols, err := ParseDFVolumes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 || vols[0].Mount != "/" {
		t.Fatalf("got %#v, want only root", vols)
	}
}

func TestParseDFVolumesDropsDarwinAndSysSynthetic(t *testing.T) {
	const out = `Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/disk3s1s1   239075328  52428800 186646528      22% /
devfs                  123         0       123       0% /dev
/dev/disk3s5     239075328  52428800 186646528      22% /System/Volumes/Data
/dev/disk3s6     239075328  52428800 186646528      22% /System/Volumes/VM
map auto_home            0         0         0     100% /System/Volumes/Data/home
/dev/disk5s1      16777216         0  16777216       0% /Library/Developer/CoreSimulator/Volumes/iOS_23F77
efivarfs              123        12       111       1% /sys/firmware/efi/efivars
/dev/disk4s1     104857600  10485760  89128960      11% /Volumes/Seagate
`
	vols, err := ParseDFVolumes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("got %#v, want root + Seagate", vols)
	}
	if vols[0].Mount != "/" || vols[1].Mount != "/Volumes/Seagate" {
		t.Fatalf("got %#v", vols)
	}
}

func TestParseMountNetworkVolumesDarwinAndLinux(t *testing.T) {
	const out = `//nas@UBUNTU-HP._smb._tcp.local/nas on /Volumes/nas (smbfs, nodev, nosuid, mounted by jeksam)
//smithanator@169.254.168.1/Macintosh%20HD on /Volumes/Macintosh HD-1 (smbfs, nodev, nosuid, mounted by jeksam)
/dev/disk3s1s1 on / (apfs, local, journaled)
//nas/share /mnt/nas cifs rw,relatime 0 0
192.168.1.105:/srv/nas /mnt/ubuntu-nas nfs4 rw,relatime 0 0
//host/Macintosh\040HD /Volumes/Macintosh\040HD-1 cifs rw 0 0
`
	vols := ParseMountNetworkVolumes(out)
	if len(vols) != 5 {
		t.Fatalf("got %d %#v", len(vols), vols)
	}
	by := map[string]models.Volume{}
	for _, v := range vols {
		if v.Kind != "network" || v.TotalGB != 0 || v.FreeGB != 0 {
			t.Fatalf("network row must be unsized: %+v", v)
		}
		by[v.Mount] = v
	}
	if by["/Volumes/nas"].Bus != "cifs" {
		t.Fatalf("darwin smbfs: %+v", by["/Volumes/nas"])
	}
	if by["/Volumes/Macintosh HD-1"].Device == "" {
		t.Fatalf("space mount missing: %#v", by)
	}
	if by["/mnt/ubuntu-nas"].Bus != "nfs" {
		t.Fatalf("nfs: %+v", by["/mnt/ubuntu-nas"])
	}
	if by[`/Volumes/Macintosh HD-1`].Mount != "/Volumes/Macintosh HD-1" && by["/Volumes/Macintosh HD-1"].Device == "" {
		t.Fatalf("octal unescape: %#v", by)
	}
}

func TestParseDFVolumesDropsZeroSizeLocal(t *testing.T) {
	const out = `Filesystem     1024-blocks     Used Available Capacity Mounted on
/dev/nvme0n1p2   460800000 230400000 207360000      53% /
/dev/disk6s1             0         0         0     100% /Volumes/Unsloth
`
	vols, err := ParseDFVolumes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 1 || vols[0].Mount != "/" {
		t.Fatalf("got %#v", vols)
	}
}

func TestParseMountNetworkVolumesDropsFilteredPrefixes(t *testing.T) {
	const out = `192.168.1.1:/snap /snap/remote nfs4 rw 0 0
//host/share /mnt/ok cifs rw 0 0
`
	vols := ParseMountNetworkVolumes(out)
	if len(vols) != 1 || vols[0].Mount != "/mnt/ok" {
		t.Fatalf("got %#v", vols)
	}
}
