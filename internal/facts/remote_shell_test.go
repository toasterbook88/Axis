package facts

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

func TestWrapBash_QuotesAndForcesBash(t *testing.T) {
	got := WrapBash("uname -s")
	if !strings.Contains(got, "axis_bash_launcher") || !strings.Contains(got, "uname -s") {
		t.Fatalf("wrap = %q", got)
	}
	// Portable: must not require only /bin/bash (NixOS has no FHS bash).
	if !strings.Contains(got, "command -v bash") || !strings.Contains(got, "/run/current-system/sw/bin/bash") {
		t.Fatalf("expected PATH + NixOS bash candidates, got %q", got)
	}
	again := WrapBash(got)
	if !strings.Contains(again, "axis_bash_launcher") {
		t.Fatalf("idempotent wrap lost launcher: %q", again)
	}
	// Missing bash surfaces as remote exit 127 — launcher always includes explicit error.
	if !strings.Contains(got, "bash not found") {
		t.Fatalf("expected missing-bash error path, got %q", got)
	}
}

type stubExec struct {
	runs []string
}

func (s *stubExec) Connect(context.Context) error { return nil }
func (s *stubExec) Close() error                  { return nil }
func (s *stubExec) Run(_ context.Context, cmd string) (string, error) {
	s.runs = append(s.runs, cmd)
	return "Linux\n", nil
}

func TestBashForcedExecutor_WrapsRun(t *testing.T) {
	inner := &stubExec{}
	ex := withBashForced(inner)
	if _, err := ex.Run(context.Background(), "uname -s"); err != nil {
		t.Fatal(err)
	}
	if len(inner.runs) != 1 {
		t.Fatalf("runs = %d", len(inner.runs))
	}
	if !strings.Contains(inner.runs[0], "uname -s") || !strings.Contains(inner.runs[0], "axis_bash_launcher") {
		t.Fatalf("command not bash-wrapped: %q", inner.runs[0])
	}
	// Not hard-coded to only /bin/bash (NixOS non-FHS).
	if strings.HasPrefix(strings.TrimSpace(inner.runs[0]), "/bin/bash --noprofile") {
		t.Fatalf("hard-coded /bin/bash breaks NixOS: %q", inner.runs[0])
	}
	ex2 := withBashForced(ex)
	if _, ok := ex2.(*bashForcedExecutor); !ok {
		t.Fatalf("expected bashForcedExecutor, got %T", ex2)
	}
	_ = transport.Executor(ex)
}

func TestWrapBash_MatchesViaWrapBashEquality(t *testing.T) {
	// Fake executors match via WrapBash(key) == cmd.
	cmd := "uname -s"
	if WrapBash(cmd) == cmd {
		t.Fatal("WrapBash should transform command")
	}
	if WrapBash(WrapBash(cmd)) != WrapBash(cmd) {
		t.Fatal("WrapBash should be idempotent for already-wrapped commands")
	}
}

func TestParseRemoteFactBundle_LinuxCore(t *testing.T) {
	mem := base64.StdEncoding.EncodeToString([]byte("MemTotal:       16384000 kB\nMemAvailable:   8192000 kB\nMemFree:        4096000 kB\n"))
	df := base64.StdEncoding.EncodeToString([]byte("Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/root 104857600 52428800 52428800 50% /\n"))
	out := `__AXIS_BUNDLE_V1__
os=Linux
arch=x86_64
hostname=cachyos
os_version=6.1.0
cpu_cores=8
cpu_model=Test CPU
meminfo_b64=` + mem + `
loadavg=0.10 0.20 0.30 1/100 1
df_b64=` + df + `
__AXIS_BUNDLE_END__
`
	kv, err := parseRemoteFactBundle(out)
	if err != nil {
		t.Fatal(err)
	}
	if kv["os"] != "Linux" || kv["hostname"] != "cachyos" {
		t.Fatalf("kv = %#v", kv)
	}

	// Full apply through tryBundleCollect
	c := &RemoteCollector{NodeName: "cachyos", Role: "agent", Hostname: "100.1.2.3", Exec: &stubExec{}}
	// Force path: call parse + apply via try with fake exec that returns bundle
	fe := &fixedExec{out: out}
	c.Exec = fe
	nf := &models.NodeFacts{Name: "cachyos", Hostname: "100.1.2.3", Status: models.StatusComplete}
	if !c.tryBundleCollect(context.Background(), nf) {
		t.Fatalf("bundle collect failed reasons=%v", nf.PartialReasons)
	}
	if strings.ToLower(nf.OS) != "linux" {
		t.Fatalf("os=%q", nf.OS)
	}
	if nf.Resources == nil || nf.Resources.RAMTotalMB <= 0 {
		t.Fatalf("resources=%+v", nf.Resources)
	}
	if nf.Resources.DiskTotalGB <= 0 {
		t.Fatalf("disk=%+v", nf.Resources)
	}
}

type fixedExec struct {
	out string
}

func (f *fixedExec) Connect(context.Context) error { return nil }
func (f *fixedExec) Close() error                  { return nil }
func (f *fixedExec) Run(context.Context, string) (string, error) {
	return f.out, nil
}

func TestLinuxThermalFromBundleTemps(t *testing.T) {
	// 96000 milli-C => 96C => critical
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
	// Bundle must use CPU_Speed_Limit parser, not free-text "critical".
	out := " - CPU_Speed_Limit               = 30\n"
	if got := parsePmsetThermal(out); got != "critical" {
		t.Fatalf("got %q", got)
	}
}
