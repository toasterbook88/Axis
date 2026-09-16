package facts

import (
	"testing"
)

func TestParseLinuxMeminfoErrorsWithoutMemTotal(t *testing.T) {
	meminfo := `MemFree:        2048000 kB
MemAvailable:   12456780 kB
`

	if _, _, err := parseLinuxMeminfo(meminfo); err == nil {
		t.Fatal("expected error when MemTotal is missing")
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
