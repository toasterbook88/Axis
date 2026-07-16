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
	if !strings.Contains(got, "bash") || !strings.Contains(got, "uname -s") {
		t.Fatalf("wrap = %q", got)
	}
	again := WrapBash("/bin/bash --noprofile --norc -c 'true'")
	if strings.Count(again, "--noprofile") != 1 {
		t.Fatalf("double wrap? %q", again)
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
	if !strings.Contains(inner.runs[0], "uname -s") || !strings.Contains(inner.runs[0], "bash") {
		t.Fatalf("command not bash-wrapped: %q", inner.runs[0])
	}
	ex2 := withBashForced(ex)
	if _, ok := ex2.(*bashForcedExecutor); !ok {
		t.Fatalf("expected bashForcedExecutor, got %T", ex2)
	}
	_ = transport.Executor(ex)
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
