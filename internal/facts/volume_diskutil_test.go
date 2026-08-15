package facts

import "testing"

func TestParseDiskutilVolumeNVMe(t *testing.T) {
	in := `   Device Identifier:         disk3s1s1
   Device Node:               /dev/disk3s1s1
   Protocol:                  Apple Fabric (NVMe)
   Solid State:               Yes
   Removable Media:           Fixed`
	got := ParseDiskutilVolume(in)
	if got.Bus != "nvme" || got.Class != "nvme" {
		t.Fatalf("nvme: %+v", got)
	}
	if got.Removable {
		t.Fatalf("internal nvme must not be removable: %+v", got)
	}
}

func TestParseDiskutilVolumeUSBSSD(t *testing.T) {
	in := `   Device Identifier:         disk6s2
   Device Node:               /dev/disk6s2
   Protocol:                  USB
   Solid State:               Yes
   Removable Media:           Yes
   Device Speed:              5.0 Gb/s`
	got := ParseDiskutilVolume(in)
	if got.Bus != "usb" || got.Class != "ssd" || !got.Removable {
		t.Fatalf("usb ssd: %+v", got)
	}
	if got.LinkMbit != 5000 {
		t.Fatalf("want 5000 Mbit, got %+v", got)
	}
}

func TestParseDiskutilVolumeSATAHDD(t *testing.T) {
	in := `   Device Identifier:         disk0s2
   Protocol:                  SATA
   Solid State:               No
   Removable Media:           No`
	got := ParseDiskutilVolume(in)
	if got.Bus != "sata" || got.Class != "hdd" || got.Removable {
		t.Fatalf("sata hdd: %+v", got)
	}
}

func TestParseDiskutilVolumeEmpty(t *testing.T) {
	got := ParseDiskutilVolume("")
	if got.Bus != "" || got.Class != "" {
		t.Fatalf("empty: %+v", got)
	}
}
