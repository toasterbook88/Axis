package facts

import (
	"os"
	"path/filepath"
	"testing"
)

func writeSys(t *testing.T, root, rel, body string) {
	t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestObserveLinuxBlockNVMe(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, "class/block/nvme0n1/queue/rotational", "0\n")
	writeSys(t, root, "class/block/nvme0n1/removable", "0\n")

	got := ObserveLinuxBlock("/dev/nvme0n1p1", root)
	if got.Bus != "nvme" || got.Class != "nvme" {
		t.Fatalf("nvme: %+v", got)
	}
	if got.Removable {
		t.Fatalf("nvme must not be removable: %+v", got)
	}
	if got.LinkMbit != 0 {
		t.Fatalf("nvme link unmeasured: %+v", got)
	}
}

func TestObserveLinuxBlockUSBSSD(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, "class/block/sda/queue/rotational", "0\n")
	writeSys(t, root, "class/block/sda/removable", "1\n")
	writeSys(t, root, "class/block/sda/device/speed", "5000\n")

	got := ObserveLinuxBlock("/dev/sda2", root)
	if got.Bus != "usb" || got.Class != "ssd" {
		t.Fatalf("usb ssd: %+v", got)
	}
	if !got.Removable {
		t.Fatalf("want removable: %+v", got)
	}
	if got.LinkMbit != 5000 {
		t.Fatalf("want 5000 Mbit: %+v", got)
	}
}

func TestObserveLinuxBlockSATAHDD(t *testing.T) {
	root := t.TempDir()
	writeSys(t, root, "class/block/sdb/queue/rotational", "1\n")
	writeSys(t, root, "class/block/sdb/removable", "0\n")

	got := ObserveLinuxBlock("/dev/sdb1", root)
	if got.Bus != "sata" || got.Class != "hdd" {
		t.Fatalf("sata hdd: %+v", got)
	}
	if got.Removable || got.LinkMbit != 0 {
		t.Fatalf("unexpected: %+v", got)
	}
}

func TestObserveLinuxBlockSkipsNetworkDevice(t *testing.T) {
	got := ObserveLinuxBlock("//nas@storage._smb._tcp.local/share", t.TempDir())
	if got.Bus != "" || got.Class != "" || got.LinkMbit != 0 {
		t.Fatalf("network device must stay unobserved: %+v", got)
	}
}
