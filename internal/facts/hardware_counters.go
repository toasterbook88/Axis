package facts

import (
	"context"
	"fmt"
	"maps"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

type hardwareCounterSet struct {
	at     time.Time
	cpu    map[string][2]float64 // total and idle ticks; guest time is already in user/nice
	disk   map[string]map[string]float64
	net    map[string]map[string]float64
	swap   map[string]float64
	errors map[string]error
}

func (c *HardwareCollector) hardwareCounters(ctx context.Context) hardwareCounterSet {
	s := hardwareCounterSet{cpu: map[string][2]float64{}, disk: map[string]map[string]float64{}, net: map[string]map[string]float64{}, swap: map[string]float64{}, errors: map[string]error{}}
	if c.osName == "linux" {
		raw, err := c.read("/proc/stat")
		s.errors["CPU"] = err
		s.cpu = parseHardwareCPU(string(raw))
		raw, err = c.read("/proc/diskstats")
		s.errors["Storage"] = err
		s.disk = parseHardwareDiskstats(string(raw))
		raw, err = c.read("/proc/net/dev")
		s.errors["Network"] = err
		s.net = parseHardwareNetdev(string(raw))
		raw, err = c.read("/proc/vmstat")
		s.errors["Memory"] = err
		for k, v := range hardwareKeyValues(string(raw), " ") {
			if k == "pswpin" || k == "pswpout" {
				if n, ok := hardwareNumber(v); ok {
					s.swap[k] = n * float64(4096)
				}
			}
		}
		// Linux's pswpin/out counters are pages; use the actual page size.
		for k, v := range s.swap {
			s.swap[k] = v / 4096 * float64(os.Getpagesize())
		}
	} else {
		raw, err := c.run(ctx, "netstat", "-ibn")
		s.errors["Network"] = err
		s.net = parseMacHardwareNetwork(string(raw))
		raw, err = c.run(ctx, "ioreg", "-r", "-c", "IOBlockStorageDriver", "-d", "1", "-l", "-w", "0")
		s.errors["Storage"] = err
		s.disk = parseMacHardwareDisk(string(raw))
		raw, err = c.run(ctx, "vm_stat")
		s.errors["Memory"] = err
		for k, v := range parseMacVM(string(raw)) {
			if k == "Swapins" || k == "Swapouts" {
				s.swap[k] = v
			}
		}
	}
	s.at = c.now().UTC()
	return s
}

func parseHardwareCPU(raw string) map[string][2]float64 {
	result := map[string][2]float64{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var total, idle float64
		valid := true
		for i := 1; i < len(f) && i <= 8; i++ {
			n, err := strconv.ParseFloat(f[i], 64)
			if err != nil {
				valid = false
				break
			}
			total += n
			if i == 4 || i == 5 {
				idle += n
			}
		}
		if valid {
			result[f[0]] = [2]float64{total, idle}
		}
	}
	return result
}

func parseHardwareDiskstats(raw string) map[string]map[string]float64 {
	result := map[string]map[string]float64{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 14 {
			continue
		}
		v := map[string]float64{}
		valid := true
		for key, index := range map[string]int{"read operations": 3, "read bytes": 5, "read milliseconds": 6, "write operations": 7, "write bytes": 9, "write milliseconds": 10, "busy milliseconds": 12} {
			n, err := strconv.ParseFloat(f[index], 64)
			if err != nil {
				valid = false
				break
			}
			if strings.HasSuffix(key, "bytes") {
				n *= 512
			}
			v[key] = n
		}
		if valid {
			result[f[2]] = v
		}
	}
	return result
}

func parseHardwareNetdev(raw string) map[string]map[string]float64 {
	result := map[string]map[string]float64{}
	for _, line := range strings.Split(raw, "\n") {
		name, tail, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(tail)
		if len(f) < 16 {
			continue
		}
		v := map[string]float64{}
		valid := true
		for key, index := range map[string]int{"RX bytes": 0, "RX packets": 1, "RX errors": 2, "RX drops": 3, "TX bytes": 8, "TX packets": 9, "TX errors": 10, "TX drops": 11} {
			n, err := strconv.ParseFloat(f[index], 64)
			if err != nil {
				valid = false
				break
			}
			v[key] = n
		}
		if valid {
			result[strings.TrimSpace(name)] = v
		}
	}
	return result
}

func parseMacHardwareNetwork(raw string) map[string]map[string]float64 {
	result := map[string]map[string]float64{}
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return result
	}
	header := strings.Fields(lines[0])
	indices := map[string]int{}
	for i, k := range header {
		indices[k] = i
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < len(header)-1 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		// The link address may be absent. Locate counters by their distance from
		// the end, so both Ethernet and address-less tunnel rows parse correctly.
		v := map[string]float64{}
		valid := true
		for key, col := range map[string]string{"RX bytes": "Ibytes", "TX bytes": "Obytes", "RX errors": "Ierrs", "TX errors": "Oerrs", "RX packets": "Ipkts", "TX packets": "Opkts"} {
			h, ok := indices[col]
			if !ok {
				valid = false
				break
			}
			index := len(f) - (len(header) - h)
			if index < 3 || index >= len(f) {
				valid = false
				break
			}
			n, err := strconv.ParseFloat(f[index], 64)
			if err != nil {
				valid = false
				break
			}
			v[key] = n
		}
		if valid {
			result[f[0]] = v
		}
	}
	return result
}

var macDiskRegistryID = regexp.MustCompile(`id (0x[0-9a-fA-F]+),`)
var macDiskCounter = regexp.MustCompile(`"(Bytes \(Read\)|Bytes \(Write\)|Operations \(Read\)|Operations \(Write\))"=(\d+)`)

func parseMacHardwareDisk(raw string) map[string]map[string]float64 {
	result := map[string]map[string]float64{}
	id := ""
	for _, line := range strings.Split(raw, "\n") {
		if _, tail, ok := strings.Cut(line, "+-o "); ok {
			id = strings.Fields(tail)[0]
			if match := macDiskRegistryID.FindStringSubmatch(tail); len(match) == 2 {
				id = "block-driver:" + match[1]
			}
		}
		if !strings.Contains(line, `"Statistics"`) || id == "" {
			continue
		}
		v := map[string]float64{}
		for _, m := range macDiskCounter.FindAllStringSubmatch(line, -1) {
			key := map[string]string{"Bytes (Read)": "read bytes", "Bytes (Write)": "write bytes", "Operations (Read)": "read operations", "Operations (Write)": "write operations"}[m[1]]
			n, _ := strconv.ParseFloat(m[2], 64)
			v[key] = n
		}
		if len(v) == 4 {
			result[id] = v
		}
	}
	return result
}

func hardwareDelta(before, after float64) (float64, bool) {
	if after < before {
		return 0, false
	}
	return after - before, true
}

func (c *HardwareCollector) rate(name string, before, after float64, seconds float64, unit, source string) models.HardwareReading {
	delta, ok := hardwareDelta(before, after)
	if !ok || seconds <= 0 {
		return c.unavailable(name, source, fmt.Errorf("counter reset"))
	}
	v := c.reading(name, delta/seconds, unit, source)
	v.Status = "derived"
	v.SampleSeconds = seconds
	return v
}

func (c *HardwareCollector) applyHardwareCounters(r *models.HardwareReport, a, b hardwareCounterSet) {
	seconds := b.at.Sub(a.at).Seconds()
	r.SampleStart, r.SampleEnd, r.SampleSeconds = a.at, b.at, seconds
	cpuSection := hardwareSection(r, "CPU")
	if c.osName == "linux" {
		for i := range cpuSection.Devices {
			d := &cpuSection.Devices[i]
			before, okA := a.cpu[d.ID]
			after, okB := b.cpu[d.ID]
			total, valid := hardwareDelta(before[0], after[0])
			idle, idleOK := hardwareDelta(before[1], after[1])
			if okA && okB && valid && idleOK && total > 0 && idle <= total {
				v := c.reading("utilization", 100*(total-idle)/total, "%", "/proc/stat sampled ticks (iowait counted idle)")
				v.Status = "derived"
				v.SampleSeconds = seconds
				v.ObservedAt = r.SampleEnd
				d.Metrics = append(d.Metrics, v)
			} else {
				d.Metrics = append(d.Metrics, c.unavailable("utilization", "/proc/stat", b.errors["CPU"]))
			}
		}
	}
	for _, kind := range []string{"Network", "Storage"} {
		previous, current := a.net, b.net
		source := "/proc/net/dev"
		if kind == "Storage" {
			previous, current = a.disk, b.disk
			source = "/proc/diskstats"
		}
		if c.osName == "darwin" {
			source = "netstat -ibn"
			if kind == "Storage" {
				source = "ioreg IOBlockStorageDriver"
			}
		}
		s := hardwareSection(r, kind)
		matched := map[string]bool{}
		for i := range s.Devices {
			d := &s.Devices[i]
			if v, ok := current[d.ID]; ok {
				c.applyDeviceCounters(d, previous[d.ID], v, seconds, source)
				matched[d.ID] = true
			}
		}
		// Mac controller counters are controller-scoped, not falsely attributed
		// to an APFS volume or one of its synthesized disks.
		for _, id := range slices.Sorted(maps.Keys(current)) {
			if !matched[id] && !(c.osName == "linux" && (strings.HasPrefix(id, "loop") || strings.HasPrefix(id, "ram"))) {
				d := hardwareDevice(id, id)
				c.applyDeviceCounters(&d, previous[id], current[id], seconds, source)
				s.Devices = append(s.Devices, d)
			}
		}
		if len(current) == 0 {
			s.Devices[0].Metrics = append(s.Devices[0].Metrics, c.unavailable("sampled activity", source, b.errors[kind]))
		}
	}
	mem := &hardwareSection(r, "Memory").Devices[0]
	for _, key := range slices.Sorted(maps.Keys(b.swap)) {
		before, ok := a.swap[key]
		if !ok {
			mem.Metrics = append(mem.Metrics, c.unavailable(key+" rate", "VM counters", nil))
			continue
		}
		mem.Metrics = append(mem.Metrics, c.rate(key+" rate", before, b.swap[key], seconds, "bytes/s", "VM counters"))
	}
}

func (c *HardwareCollector) applyDeviceCounters(d *models.HardwareDevice, a, b map[string]float64, seconds float64, source string) {
	for _, key := range slices.Sorted(maps.Keys(b)) {
		if !strings.HasSuffix(key, "bytes") && !strings.HasSuffix(key, "operations") && !strings.HasSuffix(key, "packets") {
			continue
		}
		before, ok := a[key]
		if !ok {
			d.Metrics = append(d.Metrics, c.unavailable(key+" rate", source, nil))
			continue
		}
		unit := "operations/s"
		if strings.HasSuffix(key, "bytes") {
			unit = "bytes/s"
		}
		if strings.HasSuffix(key, "packets") {
			unit = "packets/s"
		}
		d.Metrics = append(d.Metrics, c.rate(key+" rate", before, b[key], seconds, unit, source))
	}
	for _, key := range []string{"RX errors", "TX errors", "RX drops", "TX drops"} {
		if v, ok := b[key]; ok {
			d.Metrics = append(d.Metrics, c.reading(key, v, "count", source))
		}
	}
	if busy, ok := b["busy milliseconds"]; ok {
		if before, ok := a["busy milliseconds"]; ok {
			v := c.rate("busy time", before, busy, seconds, "ms/s", source)
			if n, ok := v.Value.(float64); ok {
				v.Value = n / 10
				v.Unit = "%"
			}
			d.Metrics = append(d.Metrics, v)
		}
	}
	for _, op := range []string{"read", "write"} {
		ms, ok := b[op+" milliseconds"]
		if !ok {
			continue
		}
		priorMS, okMS := a[op+" milliseconds"]
		priorOps, okOps := a[op+" operations"]
		elapsed, valid := hardwareDelta(priorMS, ms)
		ops, validOps := hardwareDelta(priorOps, b[op+" operations"])
		if okMS && okOps && valid && validOps && ops > 0 {
			v := c.reading(op+" average latency", elapsed/ops, "ms/operation", source)
			v.Status = "derived"
			d.Metrics = append(d.Metrics, v)
		}
	}
}
