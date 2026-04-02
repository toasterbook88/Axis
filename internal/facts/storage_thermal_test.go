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

func TestResolveLinuxStorageClassUsesParentDiskInfo(t *testing.T) {
	zero := 0
	infoByDevice := map[string]linuxBlockDeviceInfo{
		"/dev/mmcblk0p2": {
			Name:   "/dev/mmcblk0p2",
			PKName: "mmcblk0",
			Type:   "part",
		},
		"/dev/mmcblk0": {
			Name: "/dev/mmcblk0",
			Type: "disk",
			ROTA: &zero,
		},
	}

	got := resolveLinuxStorageClass(
		"/dev/mmcblk0p2",
		func(device string) (linuxBlockDeviceInfo, error) {
			info, ok := infoByDevice[device]
			if !ok {
				return linuxBlockDeviceInfo{}, errors.New("unexpected device lookup")
			}
			return info, nil
		},
		neverUseLinuxStorageSlaves(t),
		neverUseLinuxStorageFallback(t),
	)

	if got != "ssd" {
		t.Fatalf("resolveLinuxStorageClass = %q, want ssd", got)
	}
}

func TestResolveLinuxStorageClassRecognizesNVMeParent(t *testing.T) {
	zero := 0
	infoByDevice := map[string]linuxBlockDeviceInfo{
		"/dev/nvme0n1p2": {
			Name:   "/dev/nvme0n1p2",
			PKName: "nvme0n1",
			Type:   "part",
		},
		"/dev/nvme0n1": {
			Name: "/dev/nvme0n1",
			Type: "disk",
			ROTA: &zero,
		},
	}

	got := resolveLinuxStorageClass(
		"/dev/nvme0n1p2",
		func(device string) (linuxBlockDeviceInfo, error) {
			info, ok := infoByDevice[device]
			if !ok {
				return linuxBlockDeviceInfo{}, errors.New("unexpected device lookup")
			}
			return info, nil
		},
		neverUseLinuxStorageSlaves(t),
		neverUseLinuxStorageFallback(t),
	)

	if got != "nvme" {
		t.Fatalf("resolveLinuxStorageClass = %q, want nvme", got)
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

func TestResolveLinuxStorageClassWalksMapperAndCryptSlaves(t *testing.T) {
	one := 1
	infoByDevice := map[string]linuxBlockDeviceInfo{
		"/dev/mapper/vg-root": {
			Name:  "/dev/mapper/vg-root",
			KName: "dm-0",
			Type:  "lvm",
		},
		"/dev/dm-1": {
			Name:  "/dev/dm-1",
			KName: "dm-1",
			Type:  "crypt",
		},
		"/dev/sda2": {
			Name:   "/dev/sda2",
			KName:  "sda2",
			PKName: "sda",
			Type:   "part",
		},
		"/dev/sda": {
			Name:  "/dev/sda",
			KName: "sda",
			Type:  "disk",
			ROTA:  &one,
		},
	}
	slavesByKernelName := map[string][]string{
		"dm-0": {"/dev/dm-1"},
		"dm-1": {"/dev/sda2"},
	}

	got := resolveLinuxStorageClass(
		"/dev/mapper/vg-root",
		func(device string) (linuxBlockDeviceInfo, error) {
			info, ok := infoByDevice[device]
			if !ok {
				return linuxBlockDeviceInfo{}, errors.New("unexpected device lookup")
			}
			return info, nil
		},
		func(info linuxBlockDeviceInfo) ([]string, error) {
			return slavesByKernelName[linuxSysfsBlockName(info)], nil
		},
		neverUseLinuxStorageFallback(t),
	)

	if got != "hdd" {
		t.Fatalf("resolveLinuxStorageClass = %q, want hdd", got)
	}
}

func TestResolveLinuxStorageClassUsesConservativeWorstBackingDisk(t *testing.T) {
	zero := 0
	one := 1
	infoByDevice := map[string]linuxBlockDeviceInfo{
		"/dev/mapper/vg-root": {
			Name:  "/dev/mapper/vg-root",
			KName: "dm-0",
			Type:  "lvm",
		},
		"/dev/sda1": {
			Name:   "/dev/sda1",
			KName:  "sda1",
			PKName: "sda",
			Type:   "part",
		},
		"/dev/sda": {
			Name:  "/dev/sda",
			KName: "sda",
			Type:  "disk",
			ROTA:  &one,
		},
		"/dev/nvme0n1p3": {
			Name:   "/dev/nvme0n1p3",
			KName:  "nvme0n1p3",
			PKName: "nvme0n1",
			Type:   "part",
		},
		"/dev/nvme0n1": {
			Name:  "/dev/nvme0n1",
			KName: "nvme0n1",
			Type:  "disk",
			ROTA:  &zero,
		},
	}

	got := resolveLinuxStorageClass(
		"/dev/mapper/vg-root",
		func(device string) (linuxBlockDeviceInfo, error) {
			info, ok := infoByDevice[device]
			if !ok {
				return linuxBlockDeviceInfo{}, errors.New("unexpected device lookup")
			}
			return info, nil
		},
		func(info linuxBlockDeviceInfo) ([]string, error) {
			if linuxSysfsBlockName(info) != "dm-0" {
				return nil, nil
			}
			return []string{"/dev/sda1", "/dev/nvme0n1p3"}, nil
		},
		neverUseLinuxStorageFallback(t),
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

func TestFallbackLinuxBlockBaseHandlesPartitionStyles(t *testing.T) {
	tests := []struct {
		device string
		want   string
	}{
		{device: "/dev/nvme0n1p2", want: "nvme0n1"},
		{device: "/dev/mmcblk0p2", want: "mmcblk0"},
		{device: "/dev/sda1", want: "sda"},
		{device: "/dev/loop0", want: "loop0"},
		{device: "/dev/dm-0", want: "dm-0"},
	}

	for _, tt := range tests {
		t.Run(tt.device, func(t *testing.T) {
			if got := fallbackLinuxBlockBase(tt.device); got != tt.want {
				t.Fatalf("fallbackLinuxBlockBase(%q) = %q, want %q", tt.device, got, tt.want)
			}
		})
	}
}

func TestParsePmsetBattery(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantPct int
		wantOK  bool
	}{
		{
			"charging",
			`Now drawing from 'AC Power'
 -InternalBattery-0 (id=1234)	85%; charging; 0:45 remaining present: true`,
			85, true,
		},
		{
			"discharging",
			`Now drawing from 'Battery Power'
 -InternalBattery-0 (id=5678)	12%; discharging; 1:30 remaining present: true`,
			12, true,
		},
		{
			"full",
			`Now drawing from 'AC Power'
 -InternalBattery-0 (id=9012)	100%; charged; present: true`,
			100, true,
		},
		{
			"no_battery",
			`Now drawing from 'AC Power'`,
			0, false,
		},
		{
			"desktop",
			"",
			0, false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pct, ok := parsePmsetBattery(tt.input)
			if ok != tt.wantOK {
				t.Errorf("ok = %v, want %v", ok, tt.wantOK)
			}
			if pct != tt.wantPct {
				t.Errorf("pct = %d, want %d", pct, tt.wantPct)
			}
		})
	}
}

func TestParsePmsetThermal(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			"nominal",
			` - CPU_Scheduler_Limit          = 100
 - CPU_Available_CPUs            = 10
 - CPU_Speed_Limit               = 100`,
			"nominal",
		},
		{
			"fair_throttle",
			` - CPU_Speed_Limit               = 85`,
			"fair",
		},
		{
			"serious_throttle",
			` - CPU_Speed_Limit               = 60`,
			"serious",
		},
		{
			"critical_throttle",
			` - CPU_Speed_Limit               = 30`,
			"critical",
		},
		{
			"empty",
			"",
			"",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := parsePmsetThermal(tt.input)
			if got != tt.want {
				t.Errorf("parsePmsetThermal = %q, want %q", got, tt.want)
			}
		})
	}
}
