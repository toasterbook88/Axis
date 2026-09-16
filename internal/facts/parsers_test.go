package facts

import (
	"fmt"
	"net"
	"testing"
)

func TestParseVMStatVal(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected int64
	}{
		{"normal", "Pages free:                              65457.", 65457},
		{"no dot", "Pages free:                              65457", 65457},
		{"no colon", "Pages free                              65457.", 0},
		{"empty value", "Pages free: ", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVMStatVal(tt.line); got != tt.expected {
				t.Errorf("parseVMStatVal() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestParseDarwinFreeRAM(t *testing.T) {
	vmstat := `Mach Virtual Memory Statistics: (page size of 16384 bytes)
Pages free:                              1000.
Pages active:                            2000.
Pages inactive:                          3000.
Pages speculative:                       1000.
Pages throttled:                            0.
`
	// free + inactive = 4000 pages
	// 4000 * 16384 = 65536000 bytes
	// 65536000 / (1024*1024) = 62 MB
	expected := int64(62)

	if got := parseDarwinFreeRAM(vmstat); got != expected {
		t.Errorf("parseDarwinFreeRAM() = %v, want %v", got, expected)
	}

	vmstatAmd64 := `Mach Virtual Memory Statistics: (page size of 4096 bytes)
Pages free:                              1000.
Pages active:                            2000.
Pages inactive:                          3000.
`
	// 4000 * 4096 = 16384000 bytes
	// 16384000 / (1024*1024) = 15 MB
	expectedAmd64 := int64(15)
	if got := parseDarwinFreeRAM(vmstatAmd64); got != expectedAmd64 {
		t.Errorf("parseDarwinFreeRAM() AMD64 = %v, want %v", got, expectedAmd64)
	}

	// Default fallback if page size is missing
	vmstatMissing := `Pages free:                              1000.
Pages inactive:                          3000.
`
	// 4000 * 16384 (fallback) = 62 MB
	expectedMissing := int64(62)
	if got := parseDarwinFreeRAM(vmstatMissing); got != expectedMissing {
		t.Errorf("parseDarwinFreeRAM() missing page size = %v, want %v", got, expectedMissing)
	}
}

func TestParseKBField(t *testing.T) {
	tests := []struct {
		name     string
		line     string
		expected int64
	}{
		{"normal", "MemTotal:       16301328 kB", 16301328},
		{"no units", "MemTotal:       16301328", 16301328},
		{"empty", "MemTotal:", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseKBField(tt.line); got != tt.expected {
				t.Errorf("parseKBField() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestParseLinuxMeminfo(t *testing.T) {
	meminfo := `MemTotal:       16301328 kB
MemFree:          523784 kB
MemAvailable:   12456780 kB
Buffers:          345000 kB
`
	// 16301328 / 1024 = 15919
	// 12456780 / 1024 = 12164
	total, avail, err := parseLinuxMeminfo(meminfo)
	if err != nil {
		t.Errorf("parseLinuxMeminfo() error = %v", err)
	}
	if total != 15919 {
		t.Errorf("parseLinuxMeminfo() total = %v, want 15919", total)
	}
	if avail != 12164 {
		t.Errorf("parseLinuxMeminfo() avail = %v, want 12164", avail)
	}
}

func TestParseLinuxMeminfoFallsBackToMemFree(t *testing.T) {
	meminfo := `MemTotal:       8192000 kB
MemFree:        2048000 kB
Buffers:          345000 kB
`

	total, avail, err := parseLinuxMeminfo(meminfo)
	if err != nil {
		t.Fatalf("parseLinuxMeminfo() error = %v", err)
	}
	if total != 8000 {
		t.Fatalf("parseLinuxMeminfo() total = %v, want 8000", total)
	}
	if avail != 2000 {
		t.Fatalf("parseLinuxMeminfo() avail = %v, want 2000", avail)
	}
}

func TestParseLinuxMeminfoErrorsWithoutMemTotal(t *testing.T) {
	meminfo := `MemFree:        2048000 kB
MemAvailable:   12456780 kB
`

	if _, _, err := parseLinuxMeminfo(meminfo); err == nil {
		t.Fatal("expected error when MemTotal is missing")
	}
}

func TestParseDFOutput(t *testing.T) {
	df := `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/disk3s1 3145728 1048576 2097152 34% /
`

	total, free, err := parseDFOutput(df)
	if err != nil {
		t.Fatalf("parseDFOutput() error = %v", err)
	}
	if total != 3 {
		t.Fatalf("parseDFOutput() total = %v, want 3", total)
	}
	if free != 2 {
		t.Fatalf("parseDFOutput() free = %v, want 2", free)
	}
}

func TestParseDFOutputErrorsOnMalformedFields(t *testing.T) {
	df := `Filesystem 1024-blocks Used Available Capacity Mounted on
/dev/disk3s1 nope 250000 750000 25% /
`

	if _, _, err := parseDFOutput(df); err == nil {
		t.Fatal("expected parseDFOutput to fail on malformed numbers")
	}
}

func TestParseLoadavgFields(t *testing.T) {
	load1, load5, load15, err := parseLoadavgFields("1.23 0.98 0.55 1/999 1234")
	if err != nil {
		t.Fatalf("parseLoadavgFields() error = %v", err)
	}
	if load1 != 1.23 || load5 != 0.98 || load15 != 0.55 {
		t.Fatalf("unexpected load averages: %.2f %.2f %.2f", load1, load5, load15)
	}
}

func TestParseDarwinLoadavg(t *testing.T) {
	load1, load5, load15, err := parseDarwinLoadavg("{ 3.14 2.72 1.62 }")
	if err != nil {
		t.Fatalf("parseDarwinLoadavg() error = %v", err)
	}
	if load1 != 3.14 || load5 != 2.72 || load15 != 1.62 {
		t.Fatalf("unexpected darwin load averages: %.2f %.2f %.2f", load1, load5, load15)
	}
}

func TestParseLoadavgFieldsErrorsOnMalformedInput(t *testing.T) {
	if _, _, _, err := parseLoadavgFields("nope nope nope"); err == nil {
		t.Fatal("expected parseLoadavgFields to fail on malformed values")
	}
}

func TestComputePressure(t *testing.T) {
	tests := []struct {
		name     string
		total    int64
		free     int64
		expected string
	}{
		{"high < 5%", 16000, 500, "high"},
		{"medium < 10%", 16000, 1000, "medium"},
		{"low < 20%", 16000, 3000, "low"},
		{"none > 20%", 16000, 8000, "none"},
		{"zero total", 0, 0, "none"},
		{"negative free", 16000, -100, "high"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computePressure(tt.total, tt.free); got != tt.expected {
				t.Errorf("computePressure() = %v, want %v", got, tt.expected)
			}
		})
	}
}

func TestDetectMemoryTopology(t *testing.T) {
	topology, class := detectMemoryTopology("darwin", "arm64", "Apple M3 Max")
	if topology != "unified" {
		t.Fatalf("expected unified topology, got %q", topology)
	}
	if class != 4 {
		t.Fatalf("expected memory class 4 for Apple M3 Max, got %d", class)
	}

	topology, class = detectMemoryTopology("linux", "amd64", "AMD Ryzen 9")
	if topology != "standard" {
		t.Fatalf("expected standard topology, got %q", topology)
	}
	if class != 0 {
		t.Fatalf("expected memory class 0, got %d", class)
	}
}

func TestParseLinuxPressureStall10(t *testing.T) {
	const psi = `some avg10=6.73 avg60=4.22 avg300=2.11 total=12345
full avg10=0.32 avg60=0.12 avg300=0.05 total=456
`
	stall10, ok := parseLinuxPressureStall10(psi)
	if !ok {
		t.Fatal("expected linux psi parse to succeed")
	}
	if stall10 != 6.73 {
		t.Fatalf("expected stall10 6.73, got %.2f", stall10)
	}
	if level := linuxPressureLevel(stall10); level != "medium" {
		t.Fatalf("expected medium linux psi pressure, got %q", level)
	}
}

func TestParseLinuxGPUUtilPercentUsesMaxAcrossDevices(t *testing.T) {
	util, ok := parseLinuxGPUUtilPercent("12\n0\n77\n31\n")
	if !ok {
		t.Fatal("expected parse to succeed")
	}
	if util != 77 {
		t.Fatalf("expected max GPU util 77, got %.0f", util)
	}
}

func TestParseLinuxGPUUtilPercentHandlesIdleZero(t *testing.T) {
	util, ok := parseLinuxGPUUtilPercent("0\n0\n")
	if !ok {
		t.Fatal("expected zero util to remain a valid reading")
	}
	if util != 0 {
		t.Fatalf("expected zero GPU util, got %.0f", util)
	}
}

func TestParseDarwinMemoryPressureLevel(t *testing.T) {
	level, ok := parseDarwinMemoryPressureLevel("4\n")
	if !ok {
		t.Fatal("expected darwin pressure parse to succeed")
	}
	if level != 4 {
		t.Fatalf("expected pressure level 4, got %d", level)
	}
	if got := darwinPressureLevel(level); got != "high" {
		t.Fatalf("expected high darwin pressure, got %q", got)
	}
}

func TestMergePressureLevelsKeepsWorstLevel(t *testing.T) {
	if got := mergePressureLevels("low", "medium", "none"); got != "medium" {
		t.Fatalf("expected medium, got %q", got)
	}
	if got := mergePressureLevels("none", "high"); got != "high" {
		t.Fatalf("expected high, got %q", got)
	}
}

func TestParseVersionString(t *testing.T) {
	tests := []struct {
		name     string
		raw      string
		expected string
	}{
		{"go version", "go version go1.22.1 darwin/arm64", "1.22.1"},
		{"python version", "Python 3.11.0", "3.11.0"},
		{"git version", "git version 2.39.3 (Apple Git-145)", "2.39.3"},
		{"node version", "v20.11.0", "20.11.0"},
		{"docker version", "Docker version 24.0.7, build afdd53b", "24.0.7"}, // Note: we can later improve to strip commas
		{"ollama version", "ollama version is 0.1.28", "0.1.28"},
		{"no version", "command not found", "command not found"},
		{"multiline", "git version 2.39.3\nsome other line\n", "2.39.3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseVersionString(tt.raw); got != tt.expected {
				t.Errorf("parseVersionString(%q) = %q, want %q", tt.raw, got, tt.expected)
			}
		})
	}
}

func TestParsePmsetPowerSource(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"ac_power", "Now drawing from 'AC Power'\n", "ac"},
		{"battery_power", "Now drawing from 'Battery Power'\n", "battery"},
		{"empty", "", ""},
		{"no_match", "Some unrelated output\n", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parsePmsetPowerSource(tt.input); got != tt.want {
				t.Errorf("parsePmsetPowerSource() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestParseCPUThermalLimit(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  int
	}{
		{"normal", " - CPU_Speed_Limit               = 100\n", 100},
		{"throttled", " - CPU_Speed_Limit               = 65\n", 65},
		{"zero", " - CPU_Speed_Limit               = 0\n", 0},
		{"empty", "", 0},
		{"no_number", " - CPU_Speed_Limit               = unknown\n", 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := parseCPUThermalLimit(tt.input); got != tt.want {
				t.Errorf("parseCPUThermalLimit() = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestThermalStateFromTempC(t *testing.T) {
	tests := []struct {
		tempC float64
		want  string
	}{
		{74.9, "nominal"},
		{75.0, "fair"},
		{84.9, "fair"},
		{85.0, "serious"},
		{94.9, "serious"},
		{95.0, "critical"},
		{100.0, "critical"},
		{0.0, "nominal"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%.1f", tt.tempC), func(t *testing.T) {
			if got := thermalStateFromTempC(tt.tempC); got != tt.want {
				t.Errorf("thermalStateFromTempC(%.1f) = %q, want %q", tt.tempC, got, tt.want)
			}
		})
	}
}

func TestBlockDeviceName(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"/dev/sda1", "sda1"},
		{"/dev/nvme0n1", "nvme0n1"},
		{"sda", "sda"},
		{"", ""},
		{"  /dev/mapper/vg-root  ", "vg-root"},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := blockDeviceName(tt.input); got != tt.want {
				t.Errorf("blockDeviceName(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

func TestHasOnlyDigits(t *testing.T) {
	tests := []struct {
		input string
		want  bool
	}{
		{"123", true},
		{"", false},
		{"abc", false},
		{"12a3", false},
		{"0", true},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			if got := hasOnlyDigits(tt.input); got != tt.want {
				t.Errorf("hasOnlyDigits(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

func TestLinuxSysfsBlockName(t *testing.T) {
	tests := []struct {
		name string
		info linuxBlockDeviceInfo
		want string
	}{
		{"kname", linuxBlockDeviceInfo{KName: "dm-0"}, "dm-0"},
		{"name_fallback", linuxBlockDeviceInfo{Name: "/dev/sda1"}, "sda1"},
		{"empty", linuxBlockDeviceInfo{}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := linuxSysfsBlockName(tt.info); got != tt.want {
				t.Errorf("linuxSysfsBlockName(%+v) = %q, want %q", tt.info, got, tt.want)
			}
		})
	}
}

func TestClassifyLinuxBlockDevice(t *testing.T) {
	zero := 0
	one := 1
	two := 2
	tests := []struct {
		name string
		info linuxBlockDeviceInfo
		want string
	}{
		{"nvme", linuxBlockDeviceInfo{Name: "/dev/nvme0n1"}, "nvme"},
		{"ssd", linuxBlockDeviceInfo{Name: "/dev/sda", ROTA: &zero}, "ssd"},
		{"hdd", linuxBlockDeviceInfo{Name: "/dev/sda", ROTA: &one}, "hdd"},
		{"nil_rota", linuxBlockDeviceInfo{Name: "/dev/sda", ROTA: nil}, "unknown"},
		{"unknown_rota", linuxBlockDeviceInfo{Name: "/dev/sda", ROTA: &two}, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classifyLinuxBlockDevice(tt.info); got != tt.want {
				t.Errorf("classifyLinuxBlockDevice(%+v) = %q, want %q", tt.info, got, tt.want)
			}
		})
	}
}

func TestFallbackLinuxStorageClass(t *testing.T) {
	tests := []struct {
		name string
		src  string
		rot  string
		err  error
		want string
	}{
		{"nvme_source", "/dev/nvme0n1p2", "", nil, "nvme"},
		{"ssd", "/dev/sda1", "0", nil, "ssd"},
		{"hdd", "/dev/sda1", "1", nil, "hdd"},
		{"unknown_rot", "/dev/sda1", "2", nil, "unknown"},
		{"rot_error", "/dev/sda1", "", fmt.Errorf("no sysfs"), "unknown"},
		{"empty_base", "", "", nil, "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fallbackLinuxStorageClass(tt.src, func(string) (string, error) {
				return tt.rot, tt.err
			})
			if got != tt.want {
				t.Errorf("fallbackLinuxStorageClass(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestParseNvidiaSMIOutput_Malformed(t *testing.T) {
	input := `NVIDIA GeForce RTX 4090
NVIDIA GeForce MX250, 2048`
	gpus := parseNvidiaSMIOutput(input)
	if len(gpus) != 2 {
		t.Fatalf("expected 2 GPUs, got %d", len(gpus))
	}
	if gpus[0].VRAMMB != 0 {
		t.Errorf("gpu[0].VRAM = %d, want 0 for malformed line", gpus[0].VRAMMB)
	}
	if gpus[1].VRAMMB != 2048 {
		t.Errorf("gpu[1].VRAM = %d, want 2048", gpus[1].VRAMMB)
	}
}

func TestSubnetFromIPNet(t *testing.T) {
	_, ipNet, _ := net.ParseCIDR("192.168.1.5/24")
	if got := subnetFromIPNet(ipNet); got != "192.168.1.0/24" {
		t.Errorf("subnetFromIPNet(192.168.1.5/24) = %q, want 192.168.1.0/24", got)
	}
	if got := subnetFromIPNet(nil); got != "" {
		t.Errorf("subnetFromIPNet(nil) = %q, want empty", got)
	}
}

func TestParseAddressWithOptionalCIDR(t *testing.T) {
	tests := []struct {
		input    string
		wantIP   string
		wantCIDR string
	}{
		{"192.168.1.5/24", "192.168.1.5", "192.168.1.0/24"},
		{"10.0.0.1", "10.0.0.1", ""},
		{"2001:db8::1/64", "2001:db8::1", "2001:db8::/64"},
		{"not-an-ip", "<nil>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			ip, cidr := parseAddressWithOptionalCIDR(tt.input)
			ipStr := "<nil>"
			if ip != nil {
				ipStr = ip.String()
			}
			if ipStr != tt.wantIP {
				t.Errorf("parseAddressWithOptionalCIDR(%q) ip = %s, want %s", tt.input, ipStr, tt.wantIP)
			}
			if cidr != tt.wantCIDR {
				t.Errorf("parseAddressWithOptionalCIDR(%q) cidr = %q, want %q", tt.input, cidr, tt.wantCIDR)
			}
		})
	}
}
