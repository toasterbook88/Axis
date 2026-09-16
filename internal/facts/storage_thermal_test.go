package facts

import (
	"errors"
	"testing"
)

func neverUseLinuxStorageSlaves(t *testing.T) func(linuxBlockDeviceInfo) ([]string, error) {
	t.Helper()
	return func(linuxBlockDeviceInfo) ([]string, error) {
		t.Fatal("slaves lookup should not be used for this case")
		return nil, nil
	}
}

func neverUseLinuxStorageFallback(t *testing.T) func(string) (string, error) {
	t.Helper()
	return func(string) (string, error) {
		t.Fatal("fallback should not be used when ancestry resolution succeeds")
		return "", nil
	}
}

func TestParseDiskutilStorageClass_NVMe(t *testing.T) {
	input := `   Device Identifier:         disk3s1s1
   Device Node:               /dev/disk3s1s1
   Whole:                     No
   Part of Whole:             disk3
   Device / Media Name:       Macintosh HD
   Protocol:                  Apple Fabric (NVMe)
   Solid State:               Yes
   Virtual:                   Yes`

	got := parseDiskutilStorageClass(input)
	if got != "nvme" {
		t.Errorf("parseDiskutilStorageClass = %q, want nvme", got)
	}
}

func TestParseDiskutilStorageClass_SSD(t *testing.T) {
	input := `   Device Identifier:         disk0s1
   Protocol:                  SATA
   Solid State:               Yes`

	got := parseDiskutilStorageClass(input)
	if got != "ssd" {
		t.Errorf("parseDiskutilStorageClass = %q, want ssd", got)
	}
}

func TestParseDiskutilStorageClass_Unknown(t *testing.T) {
	input := `   Device Identifier:         disk0s1
   Protocol:                  USB`

	got := parseDiskutilStorageClass(input)
	if got != "unknown" {
		t.Errorf("parseDiskutilStorageClass = %q, want unknown", got)
	}
}

func TestResolveLinuxStorageClassFallsBackToSysfs(t *testing.T) {
	got := resolveLinuxStorageClass(
		"/dev/sda1",
		func(string) (linuxBlockDeviceInfo, error) {
			return linuxBlockDeviceInfo{}, errors.New("lsblk unavailable")
		},
		neverUseLinuxStorageSlaves(t),
		func(device string) (string, error) {
			if device != "sda" {
				t.Fatalf("fallback looked up %q, want sda", device)
			}
			return "1", nil
		},
	)

	if got != "hdd" {
		t.Fatalf("resolveLinuxStorageClass = %q, want hdd", got)
	}
}

func TestParseLinuxBlockDeviceInfoReadsKernelName(t *testing.T) {
	info, err := parseLinuxBlockDeviceInfo(`{"blockdevices":[{"name":"/dev/mapper/vg-root","kname":"dm-0","type":"lvm","rota":null}]}`)
	if err != nil {
		t.Fatalf("parseLinuxBlockDeviceInfo: %v", err)
	}
	if info.KName != "dm-0" {
		t.Fatalf("expected kname dm-0, got %q", info.KName)
	}
}
