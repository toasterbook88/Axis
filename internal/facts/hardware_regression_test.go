package facts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func TestHardwareMalformedNativeOutputDoesNotPanic(t *testing.T) {
	for _, raw := range []string{"\n", "\nx", "Name\nx", "+-o ", "+-o \n\"Statistics\" = {}"} {
		t.Run(fmt.Sprintf("%q", raw), func(t *testing.T) {
			parseMacHardwareNetwork(raw)
			parseMacHardwareDisk(raw)
		})
	}
}

func TestHardwareCountersRejectNonfiniteAndNegative(t *testing.T) {
	for _, bad := range []string{"NaN", "Inf", "-1", "1.5"} {
		if got := parseHardwareCPU("cpu " + bad + " 0 0 0 0 0 0 0"); len(got) != 0 {
			t.Errorf("CPU accepted %q: %v", bad, got)
		}
		if got := parseHardwareDiskstats("8 0 sda " + bad + " 0 0 0 0 0 0 0 0 0 0"); len(got) != 0 {
			t.Errorf("disk accepted %q: %v", bad, got)
		}
		if got := parseHardwareNetdev("eth0: " + bad + " 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0"); len(got) != 0 {
			t.Errorf("network accepted %q: %v", bad, got)
		}
	}
}

func TestHardwareLargeCountersPreserveSingleByteDelta(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	now := time.Now()
	// Native cumulative counters can exceed float64's exact-integer range.
	a := hardwareCounterSet{at: now, net: parseHardwareNetdev("network: 9007199254740992 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")}
	b := hardwareCounterSet{at: now.Add(time.Second), net: parseHardwareNetdev("network: 9007199254740993 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")}
	c.applyHardwareCounters(r, a, b)
	v := findHardwareReading(t, hardwareSection(r, "Network").Devices[0].Metrics, "RX bytes rate")
	if v.Value != float64(1) {
		t.Fatalf("single-byte delta lost: %+v", v)
	}
}

func TestMacHardwareInventoryParentsExistAndNeverSelfReference(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	err := c.parseMacHardwareInventory([]byte(`{"SPPowerDataType":[{"_name":"Battery","sppower_battery_cycle_count":"5","health":{"sppower_battery_health":"Good"}}],"SPMemoryDataType":[{"_items":[{"_name":"DIMM0","dimm_size":"8 GB"}]}]}`), r)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range r.Sections {
		ids := map[string]bool{}
		for _, d := range s.Devices {
			if ids[d.ID] {
				t.Fatalf("duplicate ID %s", d.ID)
			}
			ids[d.ID] = true
		}
		for _, d := range s.Devices {
			for _, v := range d.Specs {
				if v.Name == "parent" {
					if v.Value == d.ID || !ids[fmt.Sprint(v.Value)] {
						t.Fatalf("invalid parent for %s: %+v", d.ID, v)
					}
				}
			}
		}
	}

	// Storage gets the new profiler-parent vocabulary; nested records in other
	// sections retain the established parent reading.
	sensors := hardwareSection(r, "Sensors").Devices
	devicesByID := make(map[string]models.HardwareDevice, len(sensors))
	for _, device := range sensors {
		devicesByID[device.ID] = device
	}
	if _, ok := devicesByID["SPPowerDataType-0"]; !ok {
		t.Fatalf("battery container missing: %+v", sensors)
	}
	var health *models.HardwareDevice
	for i := range sensors {
		for _, reading := range sensors[i].Specs {
			if reading.Name == "battery condition" {
				health = &sensors[i]
			}
		}
	}
	if health == nil {
		t.Fatalf("nested battery health record missing: %+v", sensors)
	}
	parent := findHardwareReading(t, health.Specs, "parent")
	if parent.Value != "SPPowerDataType-0" || parent.Status != "observed" || parent.Source != "system_profiler SPPowerDataType" {
		t.Fatalf("non-storage containment contract changed: %+v", parent)
	}
	if got := macHardwareReadingsNamed(health.Specs, "profiler parent"); len(got) != 0 {
		t.Fatalf("storage-only label leaked into Sensors: %+v", got)
	}
}

func TestHardwareDisappearingDeviceHasUnavailableActivity(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	now := time.Now()
	a := hardwareCounterSet{at: now, net: parseHardwareNetdev("network: 10 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")}
	b := hardwareCounterSet{at: now.Add(time.Second), net: parseHardwareNetdev("other: 20 0 0 0 0 0 0 0 0 0 0 0 0 0 0 0")}
	c.applyHardwareCounters(r, a, b)
	v := findHardwareReading(t, hardwareSection(r, "Network").Devices[0].Metrics, "sampled activity")
	if v.Value != nil || v.Status == "observed" {
		t.Fatalf("missing device looked observed: %+v", v)
	}
}

func TestHardwareNumericVendorFailuresAreNotMeasurements(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	hardwareSection(r, "GPU").Devices = []models.HardwareDevice{hardwareDevice("0000:01:00.0", "GPU")}
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte("00000000:01:00.0, Example, 123, 8192, NaN, garbage, 0, 40, 10, 100, 300, 400, N/A\n"), nil
	}
	c.linuxGPUMetrics(context.Background(), r)
	for _, name := range []string{"memory.used", "utilization.gpu"} {
		v := findHardwareReading(t, hardwareSection(r, "GPU").Devices[0].Metrics, name)
		if v.Value != nil || v.Status == "observed" {
			t.Errorf("invalid numeric telemetry accepted: %+v", v)
		}
	}
}

func TestHardwareMalformedAndFailedProbesAreVisible(t *testing.T) {
	for _, raw := range []string{"", "not csv", "malformed,\"quoted", "00000000:01:00.0, too few fields"} {
		t.Run(raw, func(t *testing.T) {
			c := testHardwareCollector()
			r := testHardwareReport()
			c.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), nil }
			c.linuxGPUMetrics(context.Background(), r)
			found := false
			for _, v := range hardwareSection(r, "GPU").Devices[0].Metrics {
				if v.Name == "NVIDIA telemetry" && v.Value == nil {
					found = true
				}
			}
			if !found {
				t.Fatal("malformed NVIDIA probe was silently dropped")
			}
		})
	}
	c := testHardwareCollector()
	r := testHardwareReport()
	c.osName = "darwin"
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`"PerformanceStatistics" = {"Device Utilization %"=50}`), context.DeadlineExceeded
	}
	c.macGPUMetrics(context.Background(), r)
	for _, d := range hardwareSection(r, "GPU").Devices {
		for _, v := range d.Metrics {
			if v.Status == "observed" {
				t.Fatalf("failed probe supplied observed telemetry: %+v", v)
			}
		}
	}
}

func TestHardwareEmptyFilesystemAndMemoryProbesAreExplicit(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	c.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	c.hardwareVolumes(context.Background(), r)
	v := findHardwareReading(t, hardwareSection(r, "Storage").Devices[0].Metrics, "filesystem capacity")
	if v.Value != nil {
		t.Fatal(v)
	}
	c.read = func(string) ([]byte, error) { return []byte("MemTotal: broken kB\n"), nil }
	c.linuxMemoryMetrics(r)
	v = findHardwareReading(t, hardwareSection(r, "Memory").Devices[0].Metrics, "MemTotal")
	if v.Value != nil {
		t.Fatal(v)
	}
}

func TestHardwareRateRejectsInvalidIntervals(t *testing.T) {
	c := testHardwareCollector()
	for _, n := range []float64{math.NaN(), math.Inf(1), 0, -1} {
		v := c.rate("rate", 1, 2, n, "bytes/s", "test")
		if v.Value != nil {
			t.Errorf("invalid interval %v produced %+v", n, v)
		}
	}
}

func TestHardwareNumericFileSentinelsAndOverflow(t *testing.T) {
	c := testHardwareCollector()
	c.read = func(string) ([]byte, error) { return []byte("-1"), nil }
	v := c.fileReading("online", "test", "", 1)
	if v.Value != nil {
		t.Fatalf("negative availability sentinel accepted: %+v", v)
	}
	c.read = func(string) ([]byte, error) { return []byte("1e308"), nil }
	v = c.fileReading("capacity", "test", "bytes", 1024)
	if _, err := json.Marshal(v); err != nil {
		t.Fatalf("numeric overflow poisoned JSON: %+v", v)
	}
}

func FuzzHardwareNativeParsers(f *testing.F) {
	for _, fixture := range []string{"smart-permission.json", "mac-profiler.json", "mac-profiler-nesting.json", "pci-nvidia.json"} {
		f.Add(string(hardwareReviewFixture(f, fixture)))
	}
	for _, seed := range []string{"\n", "Name\nx", "+-o ", "cpu NaN 0 0 0 0 0 0 0", "Mach Virtual Memory Statistics: (page size of 0 bytes)\nPages free: 12.",
		`{"smartctl":{"exit_status":2},"smart_status":{"passed":false}}`,
		`{"SPNetworkDataType":[{"_name":"VPN1","interface":""},{"_name":"VPN2","interface":""}]}`} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		c, report := testHardwareCollector(), testHardwareReport()
		_ = c.parseMacHardwareInventory([]byte(raw), report)
		c.run = func(context.Context, string, ...string) ([]byte, error) { return []byte(raw), fs.ErrPermission }
		d := hardwareDevice("sda", "disk")
		d.Specs = append(d.Specs, c.reading("type", "disk", "", "fixture"))
		hardwareSection(report, "Storage").Devices = []models.HardwareDevice{d}
		c.linuxStorageHealth(context.Background(), report)
		for _, v := range hardwareSection(report, "Storage").Devices[0].Metrics {
			if v.Status == "observed" {
				t.Fatalf("permission-denied probe emitted an observed reading: %+v", v)
			}
		}
		values := []any{parseHardwareCPU(raw), parseHardwareDiskstats(raw), parseHardwareNetdev(raw), parseMacHardwareNetwork(raw), parseMacHardwareDisk(raw), parseMacVM(raw)}
		for _, v := range values {
			if _, err := json.Marshal(v); err != nil {
				t.Fatalf("unserializable native reading: %v", err)
			}
		}
	})
}

func TestHardwareSourceWindowsAndObservationTimes(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	now := time.Now()
	a := hardwareCounterSet{at: now, measured: map[string]time.Time{"Network": now, "Storage": now}, net: map[string]map[string]uint64{"network": {"RX bytes": 100}}, disk: map[string]map[string]uint64{"storage": {"read bytes": 100}}}
	b := hardwareCounterSet{at: now.Add(5 * time.Second), measured: map[string]time.Time{"Network": now.Add(time.Second), "Storage": now.Add(2 * time.Second)}, net: map[string]map[string]uint64{"network": {"RX bytes": 200}}, disk: map[string]map[string]uint64{"storage": {"read bytes": 300}}}
	c.applyHardwareCounters(r, a, b)
	for _, tc := range []struct {
		section, name string
		seconds       float64
	}{{"Network", "RX bytes rate", 1}, {"Storage", "read bytes rate", 2}} {
		v := findHardwareReading(t, hardwareSection(r, tc.section).Devices[0].Metrics, tc.name)
		if v.Value != float64(100) || v.SampleSeconds != tc.seconds {
			t.Fatalf("wrong source sampling window: %+v", v)
		}
		if v.SampleStart == nil || v.SampleEnd == nil || v.SampleEnd.Sub(*v.SampleStart).Seconds() != tc.seconds || !v.ObservedAt.Equal(*v.SampleEnd) {
			t.Fatalf("missing sampling evidence: %+v", v)
		}
	}
}

func TestHardwareWatchRefreshesLinkStateAndSpeed(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	hardwareSection(r, "Network").Devices = nil
	speed := "1000"
	c.glob = func(string) ([]string, error) { return []string{"/sys/class/net/eth0"}, nil }
	c.read = func(path string) ([]byte, error) {
		switch filepath.Base(path) {
		case "speed":
			return []byte(speed), nil
		case "operstate":
			return []byte("up"), nil
		case "mtu":
			return []byte("1500"), nil
		case "duplex":
			return []byte("full"), nil
		}
		return nil, fs.ErrNotExist
	}
	c.linuxNetworkInventory(r)
	first := cloneHardwareInventory(r)
	c.linuxLinkMetrics(first)
	speed = "2500"
	second := cloneHardwareInventory(r)
	c.linuxLinkMetrics(second)
	for _, tc := range []struct {
		r     *models.HardwareReport
		speed float64
	}{{first, 1000}, {second, 2500}} {
		d := hardwareSection(tc.r, "Network").Devices[0]
		if v := findHardwareReading(t, d.Metrics, "link speed"); v.Value != tc.speed {
			t.Fatalf("stale link speed: %+v", v)
		}
		for _, v := range d.Specs {
			if v.Name == "link speed" || v.Name == "MTU" || v.Name == "duplex" {
				t.Fatal("live link field stored in cached inventory")
			}
		}
	}
}

func TestHardwareCPUThreadsSortNumerically(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	hardwareSection(r, "CPU").Devices = nil
	c.glob = func(string) ([]string, error) {
		return []string{"/sys/devices/system/cpu/cpu0", "/sys/devices/system/cpu/cpu1", "/sys/devices/system/cpu/cpu10", "/sys/devices/system/cpu/cpu2"}, nil
	}
	c.linuxCPUInventory(context.Background(), r)
	got := []string{}
	for _, d := range hardwareSection(r, "CPU").Devices {
		got = append(got, d.ID)
	}
	if strings.Join(got, ",") != "cpu,cpu0,cpu1,cpu2,cpu10" {
		t.Fatalf("CPU order: %v", got)
	}
}

func TestHardwareSensorFaultAndDisabledReadings(t *testing.T) {
	for _, tc := range []struct {
		fault, enabled string
		observed       bool
	}{{"0", "1", true}, {"1", "1", false}, {"0", "0", false}} {
		c := testHardwareCollector()
		r := testHardwareReport()
		hardwareSection(r, "Sensors").Devices = []models.HardwareDevice{hardwareDevice("/sys/class/hwmon/hwmon0", "sensor")}
		c.glob = func(pattern string) ([]string, error) {
			if strings.HasSuffix(pattern, "temp*_input") {
				return []string{"/sys/class/hwmon/hwmon0/temp1_input"}, nil
			}
			return nil, nil
		}
		c.read = func(path string) ([]byte, error) {
			switch filepath.Base(path) {
			case "temp1_input":
				return []byte("0"), nil
			case "temp1_fault":
				return []byte(tc.fault), nil
			case "temp1_enable":
				return []byte(tc.enabled), nil
			}
			return nil, fs.ErrNotExist
		}
		c.linuxSensorMetrics(r)
		v := hardwareSection(r, "Sensors").Devices[0].Metrics[0]
		if (v.Status == "observed") != tc.observed {
			t.Fatalf("sensor status: %+v", v)
		}
	}
}

func TestHardwareMacInvalidPercentagesAndFailedMemory(t *testing.T) {
	c := testHardwareCollector()
	c.osName = "darwin"
	r := testHardwareReport()
	c.run = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		switch name {
		case "top":
			return []byte("CPU usage: 1% user, 1% sys, 98% idle\nCPU usage: 10..5% user, 5% sys, 85% idle"), nil
		case "vm_stat":
			return []byte("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 9."), context.DeadlineExceeded
		case "sysctl":
			return []byte("vm.swapusage: total = 1.00T used = 512.00G free = 512.00G"), nil
		case "pmset":
			return []byte("Now drawing from 'Battery Power'\n150%; discharging"), nil
		}
		return nil, exec.ErrNotFound
	}
	c.macCPUMetrics(context.Background(), r)
	c.macMemoryMetrics(context.Background(), r)
	c.macPowerMetrics(context.Background(), r)
	if v := findHardwareReading(t, hardwareSection(r, "CPU").Devices[0].Metrics, "CPU utilization"); v.Value != nil {
		t.Fatal(v)
	}
	if v := findHardwareReading(t, hardwareSection(r, "Memory").Devices[0].Metrics, "Pages free"); v.Status != "timeout" || v.Value != nil {
		t.Fatal(v)
	}
	if v := findHardwareReading(t, hardwareSection(r, "Memory").Devices[0].Metrics, "swap total"); v.Value != float64(1<<40) {
		t.Fatal(v)
	}
	sensors := hardwareSection(r, "Sensors").Devices
	if v := findHardwareReading(t, sensors[len(sensors)-1].Metrics, "charge"); v.Value != nil {
		t.Fatal(v)
	}
}

func TestHardwareAMDDoesNotRequireNVIDIATooling(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	d := hardwareDevice("0000:01:00.0", "AMD GPU")
	d.Specs = append(d.Specs, c.reading("vendor", "0x1002", "", "sysfs"))
	hardwareSection(r, "GPU").Devices = []models.HardwareDevice{d}
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		t.Fatal("queried NVIDIA utility for an AMD GPU")
		return nil, nil
	}
	c.linuxGPUMetrics(context.Background(), r)
}

func TestHardwareEmptySMARTObjectIsUnavailable(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	d := hardwareDevice("sda", "disk")
	d.Specs = append(d.Specs, c.reading("type", "disk", "", "fixture"))
	hardwareSection(r, "Storage").Devices = []models.HardwareDevice{d}
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"smart_status":null,"temperature":{}}`), nil
	}
	c.linuxStorageHealth(context.Background(), r)
	if v := findHardwareReading(t, hardwareSection(r, "Storage").Devices[0].Metrics, "SMART health"); v.Value != nil {
		t.Fatal(v)
	}
}

func TestHardwarePermissionDiagnosticsNeverLeakStderr(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable")
	}
	_, err = runHardwareProbe(context.Background(), shell, "-c", "printf 'permission denied PRIVATE-IDENTIFIER' >&2; exit 1")
	if !errors.Is(err, fs.ErrPermission) {
		t.Fatalf("permission error not classified: %v", err)
	}
	c := testHardwareCollector()
	v := c.unavailable("probe", "fixture", err)
	raw, _ := json.Marshal(v)
	if strings.Contains(string(raw), "PRIVATE-") || v.Status != "permission_denied" {
		t.Fatalf("unsafe diagnostic: %s", raw)
	}
}

func TestHardwareProbeDoesNotHangOnInheritedPipe(t *testing.T) {
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("shell unavailable")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err = runHardwareProbe(ctx, shell, "-c", "sleep 3 & printf ready")
	if !errors.Is(err, exec.ErrWaitDelay) {
		t.Fatalf("expected bounded pipe close: %v", err)
	}
}

func TestLocalFactsDoesNotDuplicateOllama(t *testing.T) {
	t.Setenv("AXIS_HOME", t.TempDir())
	t.Setenv("AXIS_ENDPOINT_SWEEP", "off")
	bin := t.TempDir()
	writeFactStub(t, bin, "ollama", "exit 0")
	t.Setenv("PATH", bin)
	oldOllama, oldLlama, oldMLX := runOllamaDiscoveryFn, runLlamaServerDiscoveryFn, runMLXDiscoveryFn
	t.Cleanup(func() {
		runOllamaDiscoveryFn = oldOllama
		runLlamaServerDiscoveryFn = oldLlama
		runMLXDiscoveryFn = oldMLX
	})
	runOllamaDiscoveryFn = func(context.Context) ([]byte, error) {
		return []byte(`{"installed":true,"path":"` + filepath.Join(bin, "ollama") + `","version":"1"}`), nil
	}
	runLlamaServerDiscoveryFn = func(context.Context) ([]byte, error) { return nil, fs.ErrNotExist }
	runMLXDiscoveryFn = runLlamaServerDiscoveryFn
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	nf, err := NewLocalCollector("fixture", "", nil).Collect(ctx)
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, tool := range nf.Tools {
		if tool.Name == "ollama" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("Ollama count: %d", count)
	}
}

func TestHardwareOutputLimits(t *testing.T) {
	var stdout hardwareOutput
	if _, err := stdout.Write(make([]byte, 4<<20)); err != nil {
		t.Fatal(err)
	}
	if _, err := stdout.Write([]byte("x")); err == nil {
		t.Fatal("accepted oversized output")
	}
	var stderr hardwareStderr
	raw := make([]byte, 8192)
	if n, err := stderr.Write(raw); n != len(raw) || err != nil || len(stderr.data) != 4096 {
		t.Fatalf("stderr bound: %d %v %d", n, err, len(stderr.data))
	}
}

func TestMacVMRejectsInvalidPageSizesAndOverflow(t *testing.T) {
	for _, raw := range []string{"Mach Virtual Memory Statistics: (page size of 0 bytes)\nPages free: 1.", "Mach Virtual Memory Statistics: (page size of 4095 bytes)\nPages free: 1.", "Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 18446744073709551615."} {
		if v := parseMacVM(raw); len(v) != 0 {
			t.Fatalf("invalid VM counter accepted: %v", v)
		}
	}
}

func TestHardwareRejectsNestedScalarValues(t *testing.T) {
	c := testHardwareCollector()
	v := c.reading("capacity", map[string]any{"private": "untrusted"}, "bytes", "fixture")
	if v.Value != nil {
		t.Fatalf("nested probe value accepted: %+v", v)
	}
}

func TestMacHardwareDiskRequiresDistinctRegistryIdentity(t *testing.T) {
	raw := "+-o IOBlockStorageDriver <class IOBlockStorageDriver>\n\"Statistics\" = {\"Bytes (Read)\"=1,\"Bytes (Write)\"=2,\"Operations (Read)\"=3,\"Operations (Write)\"=4}"
	if v := parseMacHardwareDisk(raw); len(v) != 0 {
		t.Fatalf("invented disk identity: %v", v)
	}
}

func TestHardwareSMARTPermissionIsClassifiedWithoutExposingMessages(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	d := hardwareDevice("sda", "disk")
	d.Specs = append(d.Specs, c.reading("type", "disk", "", "fixture"))
	hardwareSection(r, "Storage").Devices = []models.HardwareDevice{d}
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`{"smartctl":{"messages":[{"string":"Permission denied PRIVATE-DEVICE-IDENTIFIER"}]}}`), errors.New("exit status 1")
	}
	c.linuxStorageHealth(context.Background(), r)
	v := findHardwareReading(t, hardwareSection(r, "Storage").Devices[0].Metrics, "SMART health")
	raw, _ := json.Marshal(v)
	if v.Status != "permission_denied" || strings.Contains(string(raw), "PRIVATE-") {
		t.Fatalf("incorrect SMART permission diagnostic: %s", raw)
	}
}
