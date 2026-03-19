package facts

import (
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
