package facts

import (
	"context"
	"maps"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

type hardwareCounterSet struct {
	at       time.Time
	measured map[string]time.Time
	cpu      map[string][2]uint64 // total and idle ticks; guest time is already in user/nice
	disk     map[string]map[string]uint64
	net      map[string]map[string]uint64
	swap     map[string]uint64
	errors   map[string]error
}

func (c *HardwareCollector) hardwareCounters(ctx context.Context) hardwareCounterSet {
	s := hardwareCounterSet{cpu: map[string][2]uint64{}, disk: map[string]map[string]uint64{}, net: map[string]map[string]uint64{}, swap: map[string]uint64{}, errors: map[string]error{}, measured: map[string]time.Time{}}
	if c.osName == "linux" {
		raw, err := c.read("/proc/stat")
		s.errors["CPU"] = err
		if err != nil {
			raw = nil
		}
		s.cpu = parseHardwareCPU(string(raw))
		s.measured["CPU"] = c.now()
		raw, err = c.read("/proc/diskstats")
		s.errors["Storage"] = err
		if err != nil {
			raw = nil
		}
		s.disk = parseHardwareDiskstats(string(raw))
		s.measured["Storage"] = c.now()
		raw, err = c.read("/proc/net/dev")
		s.errors["Network"] = err
		if err != nil {
			raw = nil
		}
		s.net = parseHardwareNetdev(string(raw))
		s.measured["Network"] = c.now()
		raw, err = c.read("/proc/vmstat")
		s.errors["Memory"] = err
		if err != nil {
			raw = nil
		}
		for k, v := range hardwareKeyValues(string(raw), " ") {
			if k == "pswpin" || k == "pswpout" {
				if n, err := strconv.ParseUint(v, 10, 64); err == nil && n <= math.MaxUint64/uint64(os.Getpagesize()) {
					s.swap[k] = n * uint64(os.Getpagesize())
				}
			}
		}
	} else {
		raw, err := c.run(ctx, "netstat", "-ibn")
		s.errors["Network"] = err
		if err == nil {
			s.net = parseMacHardwareNetwork(string(raw))
		}
		s.measured["Network"] = c.now()
		raw, err = c.run(ctx, "ioreg", "-r", "-c", "IOBlockStorageDriver", "-d", "1", "-l", "-w", "0")
		s.errors["Storage"] = err
		if err == nil {
			s.disk = parseMacHardwareDisk(string(raw))
		}
		s.measured["Storage"] = c.now()
		raw, err = c.run(ctx, "vm_stat")
		s.errors["Memory"] = err
		if err != nil {
			raw = nil
		}
		for k, v := range parseMacVM(string(raw)) {
			if k == "Swapins" || k == "Swapouts" {
				s.swap[k] = v
			}
		}
	}
	s.measured["Memory"] = c.now()
	s.at = c.now()
	return s
}

func parseHardwareCPU(raw string) map[string][2]uint64 {
	result := map[string][2]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 5 || !strings.HasPrefix(f[0], "cpu") {
			continue
		}
		var total, idle uint64
		valid := true
		for i := 1; i < len(f) && i <= 8; i++ {
			n, err := strconv.ParseUint(f[i], 10, 64)
			if err != nil || n > math.MaxUint64-total {
				valid = false
				break
			}
			total += n
			if i == 4 || i == 5 {
				idle += n
			}
		}
		if valid {
			result[f[0]] = [2]uint64{total, idle}
		}
	}
	return result
}

func parseHardwareDiskstats(raw string) map[string]map[string]uint64 {
	result := map[string]map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		f := strings.Fields(line)
		if len(f) < 14 {
			continue
		}
		v := map[string]uint64{}
		valid := true
		for key, index := range map[string]int{"read operations": 3, "read bytes": 5, "read milliseconds": 6, "write operations": 7, "write bytes": 9, "write milliseconds": 10, "busy milliseconds": 12} {
			n, err := strconv.ParseUint(f[index], 10, 64)
			if err != nil {
				valid = false
				break
			}
			if strings.HasSuffix(key, "bytes") {
				if n > math.MaxUint64/512 {
					valid = false
					break
				}
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

func parseHardwareNetdev(raw string) map[string]map[string]uint64 {
	result := map[string]map[string]uint64{}
	for _, line := range strings.Split(raw, "\n") {
		name, tail, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		f := strings.Fields(tail)
		if len(f) < 16 {
			continue
		}
		v := map[string]uint64{}
		valid := true
		for key, index := range map[string]int{"RX bytes": 0, "RX packets": 1, "RX errors": 2, "RX drops": 3, "TX bytes": 8, "TX packets": 9, "TX errors": 10, "TX drops": 11} {
			n, err := strconv.ParseUint(f[index], 10, 64)
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

func parseMacHardwareNetwork(raw string) map[string]map[string]uint64 {
	result := map[string]map[string]uint64{}
	lines := strings.Split(raw, "\n")
	if len(lines) == 0 {
		return result
	}
	header := strings.Fields(lines[0])
	if len(header) < 9 {
		return result
	}
	indices := map[string]int{}
	for i, k := range header {
		indices[k] = i
	}
	for _, line := range lines[1:] {
		f := strings.Fields(line)
		if len(f) < 3 || len(f) < len(header)-1 || !strings.HasPrefix(f[2], "<Link#") {
			continue
		}
		// The link address may be absent. Locate counters by their distance from
		// the end, so both Ethernet and address-less tunnel rows parse correctly.
		v := map[string]uint64{}
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
			n, err := strconv.ParseUint(f[index], 10, 64)
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

func parseMacHardwareDisk(raw string) map[string]map[string]uint64 {
	result := map[string]map[string]uint64{}
	id := ""
	for _, line := range strings.Split(raw, "\n") {
		if _, tail, ok := strings.Cut(line, "+-o "); ok {
			fields := strings.Fields(tail)
			id = ""
			if len(fields) == 0 {
				continue
			}
			id = ""
			if match := macDiskRegistryID.FindStringSubmatch(tail); len(match) == 2 {
				id = "block-driver:" + match[1]
			}
		}
		if !strings.Contains(line, `"Statistics"`) || id == "" {
			continue
		}
		v := map[string]uint64{}
		for _, m := range macDiskCounter.FindAllStringSubmatch(line, -1) {
			key := map[string]string{"Bytes (Read)": "read bytes", "Bytes (Write)": "write bytes", "Operations (Read)": "read operations", "Operations (Write)": "write operations"}[m[1]]
			n, err := strconv.ParseUint(m[2], 10, 64)
			if err == nil {
				v[key] = n
			}
		}
		if len(v) == 4 {
			result[id] = v
		}
	}
	return result
}

func hardwareDelta(before, after uint64) (uint64, bool) {
	if after < before {
		return 0, false
	}
	return after - before, true
}

func (c *HardwareCollector) rate(name string, before, after uint64, seconds float64, unit, source string) models.HardwareReading {
	delta, ok := hardwareDelta(before, after)
	if !ok || seconds <= 0 || math.IsNaN(seconds) || math.IsInf(seconds, 0) {
		return c.unavailable(name, source, hardwareIssue("counter decreased, reset, or has no valid sampling interval"))
	}
	value := float64(delta) / seconds
	if math.IsNaN(value) || math.IsInf(value, 0) {
		return c.unavailable(name, source, hardwareIssue("rate exceeded the supported numeric range"))
	}
	v := c.reading(name, value, unit, source)
	v.Status = "derived"
	v.SampleSeconds = seconds
	return v
}

func (c *HardwareCollector) applyHardwareCounters(r *models.HardwareReport, a, b hardwareCounterSet) {
	r.SampleStart, r.SampleEnd, r.SampleSeconds = a.at.UTC(), b.at.UTC(), b.at.Sub(a.at).Seconds()
	if c.osName == "linux" {
		c.applyHardwareCPU(r, a, b)
	}
	for _, kind := range []string{"Network", "Storage"} {
		c.applyHardwareIO(r, a, b, kind)
	}
	c.applyHardwareSwap(r, a, b)
}

func (c *HardwareCollector) applyHardwareCPU(r *models.HardwareReport, a, b hardwareCounterSet) {

	cpuStart, cpuEnd, cpuSeconds := hardwareCounterWindow(a, b, "CPU")
	cpuSection := hardwareSection(r, "CPU")
	for i := range cpuSection.Devices {
		d := &cpuSection.Devices[i]
		before, okA := a.cpu[d.ID]
		after, okB := b.cpu[d.ID]
		total, valid := hardwareDelta(before[0], after[0])
		idle, idleOK := hardwareDelta(before[1], after[1])
		if okA && okB && valid && idleOK && total > 0 && idle <= total && cpuSeconds > 0 {
			v := c.reading("utilization", 100*float64(total-idle)/float64(total), "%", "/proc/stat sampled ticks (iowait counted idle)")
			v.Status = "derived"
			v.SampleSeconds = cpuSeconds
			v.ObservedAt = cpuEnd.UTC()
			startTime, endTime := cpuStart.UTC(), cpuEnd.UTC()
			v.SampleStart = &startTime
			v.SampleEnd = &endTime
			d.Metrics = append(d.Metrics, v)
		} else {
			d.Metrics = append(d.Metrics, c.unavailable("utilization", "/proc/stat", b.errors["CPU"]))
		}
	}

}

func (c *HardwareCollector) applyHardwareIO(r *models.HardwareReport, a, b hardwareCounterSet, kind string) {

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
	sourceStart, sourceEnd, sourceSeconds := hardwareCounterWindow(a, b, kind)
	matched := map[string]bool{}
	for i := range s.Devices {
		d := &s.Devices[i]
		if v, ok := current[d.ID]; ok {
			c.applyDeviceCounters(d, previous[d.ID], v, sourceSeconds, source)
			stampHardwareReadings(d.Metrics, sourceStart, sourceEnd)
			matched[d.ID] = true
		} else if _, known := previous[d.ID]; known {
			issue := b.errors[kind]
			if issue == nil {
				issue = hardwareIssue("component absent from current hardware counters")
			}
			d.Metrics = append(d.Metrics, c.unavailable("sampled activity", source, issue))
		}
	}
	// Mac controller counters are controller-scoped, not falsely attributed
	// to an APFS volume or one of its synthesized disks.
	for _, id := range slices.Sorted(maps.Keys(current)) {
		if !matched[id] && !(c.osName == "linux" && (strings.HasPrefix(id, "loop") || strings.HasPrefix(id, "ram"))) {
			d := hardwareDevice(id, id)
			c.applyDeviceCounters(&d, previous[id], current[id], sourceSeconds, source)
			stampHardwareReadings(d.Metrics, sourceStart, sourceEnd)
			s.Devices = append(s.Devices, d)
		}
	}
	if len(current) == 0 {
		s.Devices[0].Metrics = append(s.Devices[0].Metrics, c.unavailable("sampled activity", source, b.errors[kind]))
	}

}

func (c *HardwareCollector) applyHardwareSwap(r *models.HardwareReport, a, b hardwareCounterSet) {

	mem := &hardwareSection(r, "Memory").Devices[0]
	source := "/proc/vmstat"
	if c.osName == "darwin" {
		source = "vm_stat"
	}
	memoryStart, memoryEnd, memorySeconds := hardwareCounterWindow(a, b, "Memory")
	if len(b.swap) == 0 {
		mem.Metrics = append(mem.Metrics, c.unavailable("swap activity", source, b.errors["Memory"]))
	}
	for _, key := range slices.Sorted(maps.Keys(b.swap)) {
		before, ok := a.swap[key]
		if !ok {
			mem.Metrics = append(mem.Metrics, c.unavailable(key+" rate", source, nil))
			continue
		}
		mem.Metrics = append(mem.Metrics, c.rate(key+" rate", before, b.swap[key], memorySeconds, "bytes/s", source))
	}
	stampHardwareReadings(mem.Metrics, memoryStart, memoryEnd)

}

func (c *HardwareCollector) applyDeviceCounters(d *models.HardwareDevice, a, b map[string]uint64, seconds float64, source string) {
	for _, key := range slices.Sorted(maps.Keys(b)) {
		if !strings.HasSuffix(key, "bytes") && !strings.HasSuffix(key, "operations") && !strings.HasSuffix(key, "packets") {
			continue
		}
		before, ok := a[key]
		if !ok {
			d.Metrics = append(d.Metrics, c.unavailable(key+" rate", source, hardwareIssue("no previous counter for this component")))
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
			v := c.reading(op+" average latency", float64(elapsed)/float64(ops), "ms/operation", source)
			v.Status = "derived"
			d.Metrics = append(d.Metrics, v)
		}
	}
}

// Keep monotonic time until arithmetic finishes; UTC conversion is only for
// serialized evidence. Each source is timed separately because macOS probes
// run serially and may have materially different durations.
func hardwareCounterWindow(a, b hardwareCounterSet, kind string) (time.Time, time.Time, float64) {
	start, end := a.measured[kind], b.measured[kind]
	if start.IsZero() {
		start = a.at
	}
	if end.IsZero() {
		end = b.at
	}
	return start, end, end.Sub(start).Seconds()
}

func stampHardwareReadings(values []models.HardwareReading, start, end time.Time) {
	for i := range values {
		v := &values[i]
		if v.Status != "observed" && v.Status != "derived" {
			continue
		}
		v.ObservedAt = end.UTC()
		if v.Status == "derived" {
			s, e := start.UTC(), end.UTC()
			v.SampleStart = &s
			v.SampleEnd = &e
			v.SampleSeconds = end.Sub(start).Seconds()
		}
	}
}
