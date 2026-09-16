package facts

import (
	"context"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

func TestBashForcedExecutor_WrapsRun(t *testing.T) {
	inner := &stubExec{}
	ex := withBashForced(inner)
	if _, err := ex.Run(context.Background(), "uname -s"); err != nil {
		t.Fatal(err)
	}
	if len(inner.runs) != 1 {
		t.Fatalf("runs = %d", len(inner.runs))
	}
	if !strings.HasPrefix(inner.runs[0], "/usr/bin/env bash --noprofile --norc -c ") {
		t.Fatalf("command not env-bash-wrapped: %q", inner.runs[0])
	}
	ex2 := withBashForced(ex)
	if _, ok := ex2.(*bashForcedExecutor); !ok {
		t.Fatalf("expected bashForcedExecutor, got %T", ex2)
	}
	_ = transport.Executor(ex)
}

func TestWrapBash_MatchesViaWrapBashEquality(t *testing.T) {
	cmd := "uname -s"
	if WrapBash(cmd) == cmd {
		t.Fatal("WrapBash should transform command")
	}
	if WrapBash(WrapBash(cmd)) != WrapBash(cmd) {
		t.Fatal("WrapBash should be idempotent for already-wrapped commands")
	}
}

func TestLinuxThermalFromBundleTemps(t *testing.T) {
	st := linuxThermalStateFromTempLines("96000\n45000\n")
	if st != "critical" {
		t.Fatalf("state=%q want critical", st)
	}
	zones := parseLinuxThermalZonesBundle("85000\n", "x86_pkg_temp\n")
	if len(zones) != 1 || zones[0].State != "serious" {
		t.Fatalf("zones=%+v", zones)
	}
	if models.ThermalStateFromZones(zones) != "serious" {
		t.Fatalf("worst=%q", models.ThermalStateFromZones(zones))
	}
}

func TestParsePmsetThermalFromBundlePath(t *testing.T) {
	out := " - CPU_Speed_Limit               = 30\n"
	if got := parsePmsetThermal(out); got != "critical" {
		t.Fatalf("got %q", got)
	}
}
