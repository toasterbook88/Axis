package facts

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func testHardwareCollector() *HardwareCollector {
	return &HardwareCollector{osName: "linux", arch: "amd64", read: func(string) ([]byte, error) { return nil, fs.ErrNotExist }, glob: func(string) ([]string, error) { return nil, nil }, run: func(context.Context, string, ...string) ([]byte, error) { return nil, exec.ErrNotFound }, now: time.Now}
}

func testHardwareReport() *models.HardwareReport {
	r := &models.HardwareReport{}
	for _, name := range []string{"CPU", "Memory", "GPU", "Storage", "Network", "System", "Sensors", "Devices"} {
		r.Sections = append(r.Sections, models.HardwareSection{Name: name, Devices: []models.HardwareDevice{hardwareDevice(strings.ToLower(name), name)}})
	}
	return r
}

func findHardwareReading(t *testing.T, values []models.HardwareReading, name string) models.HardwareReading {
	t.Helper()
	for _, v := range values {
		if v.Name == name {
			return v
		}
	}
	t.Fatalf("reading %q absent: %+v", name, values)
	return models.HardwareReading{}
}

func TestHardwareCounterSamplingAndReset(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	start := time.Now()
	a := hardwareCounterSet{at: start, cpu: map[string][2]uint64{"cpu": {100, 50}}, net: map[string]map[string]uint64{"network": {"RX bytes": 10}}, disk: map[string]map[string]uint64{"storage": {"read bytes": 1024, "read operations": 1, "read milliseconds": 2, "busy milliseconds": 5}}}
	b := hardwareCounterSet{at: start.Add(2 * time.Second), cpu: map[string][2]uint64{"cpu": {200, 75}}, net: map[string]map[string]uint64{"network": {"RX bytes": 210}}, disk: map[string]map[string]uint64{"storage": {"read bytes": 3072, "read operations": 3, "read milliseconds": 8, "busy milliseconds": 25}}}
	c.applyHardwareCounters(r, a, b)
	for _, test := range []struct {
		section, name string
		want          float64
	}{{"CPU", "utilization", 75}, {"Network", "RX bytes rate", 100}, {"Storage", "read bytes rate", 1024}, {"Storage", "read average latency", 3}, {"Storage", "busy time", 1}} {
		v := findHardwareReading(t, hardwareSection(r, test.section).Devices[0].Metrics, test.name)
		if v.Value != test.want || v.Status != "derived" {
			t.Fatalf("%s: %+v want %v", test.name, v, test.want)
		}
	}
	if v := c.rate("rate", 20, 10, 1, "bytes/s", "test"); v.Value != nil || v.Status == "derived" {
		t.Fatalf("reset presented as measured: %+v", v)
	}
	if v := c.rate("rate", 10, 10, 1, "bytes/s", "test"); v.Value != float64(0) || v.Status != "derived" {
		t.Fatalf("measured zero lost: %+v", v)
	}
}

func TestHardwareNativeCounterParsers(t *testing.T) {
	// Guest/guest_nice are already included in user/nice; never double count.
	cpu := parseHardwareCPU("cpu 10 20 30 40 5 6 7 8 10 20\ncpu0 invalid\n")
	if cpu["cpu"] != [2]uint64{126, 45} || len(cpu) != 1 {
		t.Fatalf("CPU ticks: %+v", cpu)
	}
	disks := parseHardwareDiskstats("8 0 sda 1 0 4 6 3 0 8 10 0 12 0\ninvalid")
	if disks["sda"]["read bytes"] != 2048 || disks["sda"]["write bytes"] != 4096 {
		t.Fatalf("512-byte sectors: %+v", disks)
	}
	net := parseHardwareNetdev("eth0: 100 2 3 4 0 0 0 0 200 5 6 7 0 0 0 0\nbad: nan")
	if net["eth0"]["RX bytes"] != 100 || net["eth0"]["TX drops"] != 7 {
		t.Fatalf("network: %+v", net)
	}
}

func TestMacHardwareCounterParsers(t *testing.T) {
	raw := "Name Mtu Network Address Ipkts Ierrs Ibytes Opkts Oerrs Obytes Coll\nlo0 16384 <Link#1> 12 0 100 13 0 200 0\nen0 1500 <Link#2> 00:00:00:00:00:01 20 1 300 21 2 400 0\nen0 1500 192.0.2 192.0.2.1 20 - 300 21 - 400 -\n"
	net := parseMacHardwareNetwork(raw)
	if len(net) != 2 || net["lo0"]["RX bytes"] != 100 || net["en0"]["TX bytes"] != 400 {
		t.Fatalf("Mac counters: %+v", net)
	}
	disk := parseMacHardwareDisk("+-o IOBlockStorageDriver <class IOBlockStorageDriver, id 0x123, registered>\n\"Statistics\" = {\"Bytes (Read)\"=1024,\"Bytes (Write)\"=2048,\"Operations (Read)\"=1,\"Operations (Write)\"=2}\n+-o IOBlockStorageDriver <class IOBlockStorageDriver, id 0x456, registered>\n\"Statistics\" = {\"Bytes (Read)\"=3,\"Bytes (Write)\"=4,\"Operations (Read)\"=5,\"Operations (Write)\"=6}")
	if len(disk) != 2 || disk["block-driver:0x123"]["read bytes"] != 1024 {
		t.Fatalf("Mac disk identity: %+v", disk)
	}
	vm := parseMacVM("Mach Virtual Memory Statistics: (page size of 16384 bytes)\nPages free: 10.\nSwapins: 2.\n")
	if vm["Pages free"] != 163840 || vm["Swapins"] != 32768 {
		t.Fatalf("Apple Silicon page size: %+v", vm)
	}
	vm = parseMacVM("Mach Virtual Memory Statistics: (page size of 4096 bytes)\nPages free: 10.\n")
	if vm["Pages free"] != 40960 {
		t.Fatalf("Intel page size: %+v", vm)
	}
}

func TestMacHardwareInventoryFiltersIdentifiers(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	raw := []byte(`{"SPHardwareDataType":[{"_name":"hardware","machine_model":"Example1,1","serial_number":"PRIVATE-SERIAL","platform_UUID":"PRIVATE-UUID"}],"SPMemoryDataType":[{"_items":[{"_name":"slot0","dimm_size":"8 GB","dimm_serial_number":"PRIVATE-DIMM"}]}],"SPDisplaysDataType":[{"sppci_model":"Example GPU","spdisplays_ndrvs":[{"_name":"Display","_spdisplays_resolution":"1920 x 1080","_spdisplays_display-serial-number":"PRIVATE-DISPLAY"}]}]}`)
	if err := c.parseMacHardwareInventory(raw, r); err != nil {
		t.Fatal(err)
	}
	out, _ := json.Marshal(r)
	if strings.Contains(string(out), "PRIVATE-") {
		t.Fatalf("identifier leaked: %s", out)
	}
	for _, want := range []string{"Example1,1", "8 GB", "1920 x 1080"} {
		if !strings.Contains(string(out), want) {
			t.Fatalf("missing hardware %s", want)
		}
	}
}

func TestHardwareUnavailableReasonsAndZero(t *testing.T) {
	c := testHardwareCollector()
	for _, tc := range []struct {
		err    error
		status string
	}{{fs.ErrPermission, "permission_denied"}, {exec.ErrNotFound, "tool_missing"}, {context.DeadlineExceeded, "timeout"}, {context.Canceled, "cancelled"}, {nil, "unavailable"}} {
		v := c.unavailable("test", "source", tc.err)
		if v.Status != tc.status || v.Value != nil || v.Reason == "" {
			t.Fatalf("%+v", v)
		}
	}
	v := c.reading("idle", 0, "%", "fixture")
	raw, _ := json.Marshal(v)
	if !strings.Contains(string(raw), `"value":0`) {
		t.Fatalf("zero omitted: %s", raw)
	}
	for _, bad := range []string{"NaN", "Inf", "bad"} {
		if _, ok := hardwareNumber(bad); ok {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestHardwareInventoryIndependentAndWatchRetainsSpecs(t *testing.T) {
	c := testHardwareCollector()
	calls := map[string]int{}
	c.run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		calls[name]++
		return nil, exec.ErrNotFound
	}
	first, err := c.Sample(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	before := calls["lscpu"]
	second, err := c.Sample(context.Background(), 100*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if calls["lscpu"] != before || before != 1 {
		t.Fatalf("inventory repeated: %+v", calls)
	}
	if first.Status != "partial" || second.Status != "partial" {
		t.Fatal("missing tools must yield partial reports")
	}
	for _, s := range second.Sections {
		if len(s.Devices) == 0 {
			t.Fatalf("section %s lacks coverage explanation", s.Name)
		}
	}
	for _, name := range []string{"ollama", "curl", "ssh", "swift"} {
		if calls[name] != 0 {
			t.Fatalf("nonhardware probe: %s", name)
		}
	}
	first.Sections[0].Devices[0].Specs[0].Name = "mutated"
	if second.Sections[0].Devices[0].Specs[0].Name == "mutated" {
		t.Fatal("report aliased inventory")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Sample(ctx, 100*time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel: %v", err)
	}
	if _, err := c.Sample(context.Background(), time.Nanosecond); err == nil {
		t.Fatal("accepted invalid sample interval")
	}
}

func TestLinuxDIMMAndStorageIdentifierFiltering(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	c.run = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		switch name {
		case "dmidecode":
			return []byte("Memory Device\n\tSize: 8 GB\n\tLocator: DIMM0\n\tManufacturer: Example\n\tSerial Number: PRIVATE-DIMM\n"), nil
		case "lsblk":
			return []byte(`{"blockdevices":[{"name":"sda","kname":"sda","type":"disk","size":1000,"serial":"PRIVATE-DISK","children":[{"name":"sda1","kname":"sda1","type":"part","size":900}]}]}`), nil
		}
		return nil, exec.ErrNotFound
	}
	c.linuxDIMMs(context.Background(), r)
	c.linuxBlockInventory(context.Background(), r)
	out, _ := json.Marshal(r)
	if strings.Contains(string(out), "PRIVATE-") {
		t.Fatalf("identifiers leaked: %s", out)
	}
	if !strings.Contains(string(out), `"parent","value":"sda"`) {
		t.Fatalf("partition relationship absent: %s", out)
	}
}

func TestHardwareLscpuNestedAndFlat(t *testing.T) {
	c := testHardwareCollector()
	for _, raw := range []string{`{"lscpu":[{"field":"Model name:","data":"Example CPU"}]}`, `{"lscpu":[{"field":"Vendor ID:","data":"Example","children":[{"field":"Model name:","data":"Example CPU"}]}]}`} {
		values, err := c.parseHardwareLscpu([]byte(raw))
		if err != nil {
			t.Fatal(err)
		}
		if v := findHardwareReading(t, values, "Model name"); v.Value != "Example CPU" {
			t.Fatal(v)
		}
	}
}

func TestHardwareVendorGPUMetricsAndMemoryDomains(t *testing.T) {
	c := testHardwareCollector()
	r := testHardwareReport()
	hardwareSection(r, "GPU").Devices = []models.HardwareDevice{hardwareDevice("0000:01:00.0", "GPU")}
	c.run = func(_ context.Context, name string, _ ...string) ([]byte, error) {
		if name != "nvidia-smi" {
			return nil, exec.ErrNotFound
		}
		return []byte("00000000:01:00.0, Example GPU, 123.4, 8192, 0, 0, 0, 40, 10, 100, 300, 400, N/A\n"), nil
	}
	c.linuxGPUMetrics(context.Background(), r)
	d := hardwareSection(r, "GPU").Devices[0]
	if len(hardwareSection(r, "GPU").Devices) != 1 || d.Name != "Example GPU" {
		t.Fatalf("PCI identity not joined: %+v", d)
	}
	if v := findHardwareReading(t, d.Metrics, "memory.used"); v.Value != float64(0) || v.Unit != "MiB" {
		t.Fatal(v)
	}
	if v := findHardwareReading(t, d.Metrics, "fan.speed"); v.Value != nil || v.Status != "unavailable" {
		t.Fatal(v)
	}
	c.osName = "darwin"
	c.run = func(context.Context, string, ...string) ([]byte, error) {
		return []byte(`"PerformanceStatistics" = {"Device Utilization %"=0,"In use system memory"=1024}`), nil
	}
	c.macGPUMetrics(context.Background(), r)
	accelerator := hardwareSection(r, "GPU").Devices[1]
	if v := findHardwareReading(t, accelerator.Metrics, "In use system memory"); v.Value != uint64(1024) || v.Unit != "bytes" {
		t.Fatal(v)
	}
}
