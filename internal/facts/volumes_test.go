package facts

import (
	"testing"
)

// Fixture is POSIX `df -kP` (no fstype). Virtual mounts must be dropped;
// real block and network mounts must appear with size, mount, and bus hint.
func TestDFSourceIsNetwork(t *testing.T) {
	cases := []struct {
		device string
		want   bool
	}{
		{"/dev/nvme0n1p2", false},
		{"/dev/sda1", false},
		{"//nas/share", true},
		{"192.0.2.10:/export", true},
		{"foundry:/mnt/data", true},
		{"10.0.0.1:/nfs", true},
		{"fuse.sshfs", true},
		{"sshfs#user@host", true},
		{"nfs4", true},
		{"overlay", false},
	}
	for _, tc := range cases {
		if got := DFSourceIsNetwork(tc.device); got != tc.want {
			t.Errorf("DFSourceIsNetwork(%q) = %v, want %v", tc.device, got, tc.want)
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
/dev/disk4s1     104857600  10485760  89128960      11% /Volumes/External
`
	vols, err := ParseDFVolumes(out)
	if err != nil {
		t.Fatal(err)
	}
	if len(vols) != 2 {
		t.Fatalf("got %#v, want root + External", vols)
	}
	if vols[0].Mount != "/" || vols[1].Mount != "/Volumes/External" {
		t.Fatalf("got %#v", vols)
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
