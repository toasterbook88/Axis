package facts

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func (c *HardwareCollector) linuxHardwareMetrics(ctx context.Context, r *models.HardwareReport) {
	cpu := hardwareSection(r, "CPU")
	for i := range cpu.Devices {
		d := &cpu.Devices[i]
		if d.ID == "cpu" {
			continue
		}
		base := "/sys/devices/system/cpu/" + d.ID + "/cpufreq/"
		d.Metrics = append(d.Metrics, c.fileReading("current frequency", base+"scaling_cur_freq", "MHz", 0.001))
	}
	raw, err := c.read("/proc/loadavg")
	if f := strings.Fields(string(raw)); err == nil && len(f) >= 3 {
		for i, key := range []string{"load average 1m", "load average 5m", "load average 15m"} {
			if n, ok := hardwareNumber(f[i]); ok {
				cpu.Devices[0].Metrics = append(cpu.Devices[0].Metrics, c.reading(key, n, "tasks", "/proc/loadavg"))
			}
		}
	}
	c.linuxMemoryMetrics(r)
	c.linuxPressureMetrics(r)
	c.linuxLinkMetrics(r)
	c.linuxGPUMetrics(ctx, r)
	c.linuxSensorMetrics(r)
	c.linuxStorageHealth(ctx, r)
	c.hardwareVolumes(ctx, r)
}

// PCI vendor identity determines whether NVIDIA telemetry is applicable.
func hardwareNVIDIA(d models.HardwareDevice) bool {
	for _, v := range d.Specs {
		if v.Name == "vendor" {
			return v.Value == "0x10de"
		}
	}
	return true // inaccessible PCI inventory: let the vendor query discover it
}

var hardwarePCIBus = regexp.MustCompile(`(?i)^([0-9a-f]{4}|[0-9a-f]{8}):[0-9a-f]{2}:[0-9a-f]{2}\.[0-7]$`)

func (c *HardwareCollector) linuxGPUMetrics(ctx context.Context, r *models.HardwareReport) {
	s := hardwareSection(r, "GPU")
	for i := range s.Devices {
		d := &s.Devices[i]
		if !hardwarePCIBus.MatchString(d.ID) {
			continue
		}
		base := "/sys/bus/pci/devices/" + d.ID + "/"
		amd := slices.ContainsFunc(d.Specs, func(v models.HardwareReading) bool { return v.Name == "vendor" && v.Value == "0x1002" })
		for _, f := range []struct {
			name, file, unit string
			scale            float64
		}{{"GPU utilization", "gpu_busy_percent", "%", 1}, {"VRAM capacity", "mem_info_vram_total", "bytes", 1}, {"VRAM used", "mem_info_vram_used", "bytes", 1}, {"current PCIe speed", "current_link_speed", "", 0}, {"current PCIe width", "current_link_width", "lanes", 1}} {
			if !amd && (f.file == "gpu_busy_percent" || strings.HasPrefix(f.file, "mem_info_vram_")) {
				continue
			}
			d.Metrics = append(d.Metrics, c.fileReading(f.name, base+f.file, f.unit, f.scale))
		}
	}
	if !slices.ContainsFunc(s.Devices, hardwareNVIDIA) {
		return
	}
	fields := []string{"pci.bus_id", "name", "driver_version", "memory.total", "memory.used", "utilization.gpu", "utilization.memory", "temperature.gpu", "power.draw", "power.limit", "clocks.current.graphics", "clocks.current.memory", "fan.speed"}
	raw, err := c.run(ctx, "nvidia-smi", "--query-gpu="+strings.Join(fields, ","), "--format=csv,noheader,nounits")
	if err != nil {
		c.nvidiaUnavailable(s, err)
		return
	}
	rows, err := csv.NewReader(strings.NewReader(string(raw))).ReadAll()
	if err != nil || len(rows) == 0 {
		c.nvidiaUnavailable(s, hardwareIssue("NVIDIA probe returned no valid CSV rows"))
		return
	}
	seen := map[string]bool{}
	for _, row := range rows {
		if len(row) != len(fields) || !hardwarePCIBus.MatchString(strings.TrimSpace(row[0])) {
			continue
		}
		id := strings.ToLower(strings.TrimSpace(row[0]))
		if len(id) > 12 {
			id = id[len(id)-12:]
		}
		if seen[id] {
			continue
		}
		seen[id] = true
		index := slices.IndexFunc(s.Devices, func(d models.HardwareDevice) bool { return strings.EqualFold(d.ID, id) })
		if index < 0 {
			s.Devices = append(s.Devices, hardwareDevice(id, strings.TrimSpace(row[1])))
			index = len(s.Devices) - 1
		}
		c.appendNVIDIATelemetry(&s.Devices[index], fields, row)
	}
	for i := range s.Devices {
		d := &s.Devices[i]
		if hardwareNVIDIA(*d) && !seen[d.ID] {
			// An inaccessible-inventory placeholder is superseded by discovered GPUs.
			if len(seen) > 0 && !hardwarePCIBus.MatchString(d.ID) {
				continue
			}
			d.Metrics = append(d.Metrics, c.unavailable("NVIDIA telemetry", "nvidia-smi", hardwareIssue("no valid NVIDIA row for this device")))
		}
	}
}

func (c *HardwareCollector) nvidiaUnavailable(s *models.HardwareSection, err error) {
	for i := range s.Devices {
		d := &s.Devices[i]
		if hardwareNVIDIA(*d) {
			d.Metrics = append(d.Metrics, c.unavailable("NVIDIA telemetry", "nvidia-smi", err))
		}
	}
}

func (c *HardwareCollector) appendNVIDIATelemetry(d *models.HardwareDevice, fields, row []string) {
	d.Name = strings.TrimSpace(row[1])
	units := []string{"", "", "", "MiB", "MiB", "%", "%", "C", "W", "W", "MHz", "MHz", "%"}
	fallback := map[string]string{"memory.total": "VRAM capacity", "memory.used": "VRAM used", "utilization.gpu": "GPU utilization"}
	for i := 1; i < len(fields); i++ {
		text := strings.TrimSpace(row[i])
		var v models.HardwareReading
		if i <= 2 && text != "" && text != "N/A" && text != "[N/A]" && !strings.Contains(text, "Not Supported") {
			v = c.reading(fields[i], text, units[i], "nvidia-smi")
		} else if n, ok := hardwareNumber(text); i > 2 && ok && n >= 0 && (units[i] != "%" || n <= 100) {
			v = c.reading(fields[i], n, units[i], "nvidia-smi")
		} else {
			v = c.unavailable(fields[i], "nvidia-smi", hardwareIssue("unsupported or invalid vendor reading"))
		}
		if v.Status == "observed" {
			if name := fallback[fields[i]]; name != "" {
				d.Metrics = slices.DeleteFunc(d.Metrics, func(existing models.HardwareReading) bool { return existing.Name == name })
			}
		}
		if i <= 3 {
			d.Specs = append(d.Specs, v)
		} else {
			d.Metrics = append(d.Metrics, v)
		}
	}
}

func (c *HardwareCollector) linuxSensorMetrics(r *models.HardwareReport) {
	s := hardwareSection(r, "Sensors")
	for i := range s.Devices {
		d := &s.Devices[i]
		if strings.HasPrefix(d.ID, "/sys/class/hwmon/") {
			for _, kind := range []struct {
				glob, unit string
				scale      float64
			}{{"temp*_input", "C", 0.001}, {"fan*_input", "RPM", 1}, {"power*_input", "W", 0.000001}, {"power*_average", "W", 0.000001}, {"in*_input", "V", 0.001}, {"curr*_input", "A", 0.001}} {
				paths, _ := c.glob(d.ID + "/" + kind.glob)
				for _, p := range paths {
					key := filepath.Base(p)
					labelPath := strings.TrimSuffix(strings.TrimSuffix(p, "_input"), "_average") + "_label"
					if label, e := c.read(labelPath); e == nil && strings.TrimSpace(string(label)) != "" {
						key = strings.TrimSpace(string(label)) + " (" + key + ")"
					}
					d.Metrics = append(d.Metrics, c.hardwareSensorReading(key, p, kind.unit, kind.scale))
				}
			}
		} else if strings.HasPrefix(d.ID, "/sys/class/thermal/") {
			d.Metrics = append(d.Metrics, c.fileReading("temperature", d.ID+"/temp", "C", 0.001))
		} else if strings.HasPrefix(d.ID, "/sys/class/power_supply/") {
			for _, f := range []struct {
				name, unit string
				scale      float64
			}{{"status", "", 0}, {"online", "", 1}, {"capacity", "%", 1}, {"cycle_count", "count", 1}, {"power_now", "W", 0.000001}, {"voltage_now", "V", 0.000001}, {"current_now", "A", 0.000001}, {"energy_now", "Wh", 0.000001}, {"energy_full", "Wh", 0.000001}, {"charge_full", "Ah", 0.000001}, {"temp", "C", 0.1}} {
				d.Metrics = append(d.Metrics, c.fileReading(f.name, d.ID+"/"+f.name, f.unit, f.scale))
			}
		}
		if len(d.Metrics) == 0 {
			d.Metrics = append(d.Metrics, c.unavailable("sensor readings", d.ID, nil))
		}
	}
}

func (c *HardwareCollector) linuxStorageHealth(ctx context.Context, r *models.HardwareReport) {
	s := hardwareSection(r, "Storage")
	for i := range s.Devices {
		d := &s.Devices[i]
		isDisk := false
		for _, v := range d.Specs {
			if v.Name == "type" && v.Value == "disk" {
				isDisk = true
			}
		}
		if !isDisk || strings.HasPrefix(d.ID, "zram") {
			continue
		}
		raw, err := c.run(ctx, "smartctl", "--json", "--all", "/dev/"+d.ID)
		var v map[string]any
		// smartctl exit bits also signal device health. Parse valid JSON even when
		// its process exits nonzero; never turn that into a healthy-device claim.
		if e := json.Unmarshal(raw, &v); e != nil {
			d.Metrics = append(d.Metrics, c.unavailable("SMART health", "smartctl --json --all", err))
			continue
		}

		err = hardwareSMARTError(v, err)
		smart, _ := v["smartctl"].(map[string]any)
		exitStatus, _ := smart["exit_status"].(float64)
		// Bits 0/1 mean command/device-open failure; higher bits can describe
		// real failing health. Failed-open output may still contain stub fields.
		failedOpen := exitStatus >= 1 && exitStatus <= 255 && int(exitStatus)&3 != 0
		if failedOpen || errors.Is(err, fs.ErrPermission) || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			if err == nil {
				err = hardwareIssue("SMART command failed before health could be read")
			}
			d.Metrics = append(d.Metrics, c.unavailable("SMART health", "smartctl --json --all", err))
			continue
		}
		before := len(d.Metrics)
		for _, key := range []string{"smart_status", "temperature", "power_on_time", "power_cycle_count", "nvme_smart_health_information_log"} {
			if x, ok := v[key]; ok {
				c.appendHardwareHealth(d, key, x)
			}
		}
		if len(d.Metrics) == before {
			d.Metrics = append(d.Metrics, c.unavailable("SMART health", "smartctl --json --all", err))
		}
	}
}

func (c *HardwareCollector) appendHardwareHealth(d *models.HardwareDevice, prefix string, value any) {
	switch v := value.(type) {
	case map[string]any:
		for _, key := range slices.Sorted(maps.Keys(v)) {
			c.appendHardwareHealth(d, prefix+" "+key, v[key])
		}
	case string, float64, bool:
		unit := ""
		switch {
		case strings.Contains(prefix, "temperature"):
			unit = "C"
		case strings.Contains(prefix, "percentage_used") || strings.Contains(prefix, "available_spare"):
			unit = "%"
		case strings.HasSuffix(prefix, "hours") || strings.HasSuffix(prefix, "power_on_time hours"):
			unit = "hours"
		case strings.Contains(prefix, "power_cycle") || strings.Contains(prefix, "commands") || strings.Contains(prefix, "errors") || strings.Contains(prefix, "shutdowns"):
			unit = "count"
		}
		d.Metrics = append(d.Metrics, c.reading(prefix, v, unit, "smartctl --json --all"))
	}
}

func (c *HardwareCollector) hardwareVolumes(ctx context.Context, r *models.HardwareReport) {
	raw, err := c.run(ctx, "df", "-kP")
	if err != nil {
		hardwareSection(r, "Storage").Devices[0].Metrics = append(hardwareSection(r, "Storage").Devices[0].Metrics, c.unavailable("filesystem capacity", "df -kP", err))
		return
	}
	found := false
	for _, line := range strings.Split(string(raw), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) < 6 || !strings.HasPrefix(f[0], "/dev/") {
			continue
		}
		mount := strings.Join(f[5:], " ")
		d := hardwareDevice("mount:"+mount, "Filesystem "+mount)
		d.Specs = append(d.Specs, c.reading("device", f[0], "", "df -kP"))
		for i, key := range []string{"capacity", "used", "available"} {
			n, ok := hardwareNumber(f[i+1])
			if ok && n >= 0 {
				d.Metrics = append(d.Metrics, c.reading(key, n*1024, "bytes", "df -kP"))
			} else {
				d.Metrics = append(d.Metrics, c.unavailable(key, "df -kP", fmt.Errorf("invalid capacity")))
			}
		}
		hardwareSection(r, "Storage").Devices = append(hardwareSection(r, "Storage").Devices, d)
		found = true
	}
	if !found {
		hardwareSection(r, "Storage").Devices[0].Metrics = append(hardwareSection(r, "Storage").Devices[0].Metrics, c.unavailable("filesystem capacity", "df -kP", hardwareIssue("no usable local filesystem rows")))
	}
}

func (c *HardwareCollector) linuxMemoryMetrics(r *models.HardwareReport) {

	raw, err := c.read("/proc/meminfo")
	mem := &hardwareSection(r, "Memory").Devices[0]
	if err != nil {
		mem.Metrics = append(mem.Metrics, c.unavailable("memory usage", "/proc/meminfo", err))
	} else {
		fields := hardwareKeyValues(string(raw), ":")
		for _, key := range []string{"MemTotal", "MemFree", "MemAvailable", "Buffers", "Cached", "SReclaimable", "Shmem", "SwapTotal", "SwapFree", "Dirty", "Writeback", "HugePages_Total", "HugePages_Free", "Hugepagesize"} {
			f := strings.Fields(fields[key])
			if len(f) == 0 {
				mem.Metrics = append(mem.Metrics, c.unavailable(key, "/proc/meminfo", nil))
				continue
			}
			if n, ok := hardwareNumber(f[0]); ok && n >= 0 {
				unit := "count"
				if len(f) > 1 && f[1] == "kB" {
					n *= 1024
					unit = "bytes"
				}
				mem.Metrics = append(mem.Metrics, c.reading(key, n, unit, "/proc/meminfo"))
			} else {
				mem.Metrics = append(mem.Metrics, c.unavailable(key, "/proc/meminfo", hardwareIssue("invalid memory counter")))
			}
		}
	}
}

func (c *HardwareCollector) linuxPressureMetrics(r *models.HardwareReport) {
	mem := &hardwareSection(r, "Memory").Devices[0]

	for _, resource := range []string{"cpu", "memory", "io"} {
		path := "/proc/pressure/" + resource
		raw, err := c.read(path)
		if err != nil {
			mem.Metrics = append(mem.Metrics, c.unavailable(resource+" pressure", path, err))
			continue
		}
		for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
			f := strings.Fields(line)
			if len(f) < 2 {
				continue
			}
			for _, token := range f[1:] {
				k, v, ok := strings.Cut(token, "=")
				if !ok {
					continue
				}
				if n, ok := hardwareNumber(v); ok {
					unit := "%"
					if k == "total" {
						unit = "microseconds"
					}
					mem.Metrics = append(mem.Metrics, c.reading(resource+" pressure "+f[0]+" "+k, n, unit, path))
				}
			}
		}
	}
}

func (c *HardwareCollector) linuxLinkMetrics(r *models.HardwareReport) {
	s := hardwareSection(r, "Network")
	for i := range s.Devices {
		d := &s.Devices[i]
		base := "/sys/class/net/" + d.ID + "/"
		for _, field := range []struct {
			name, file, unit string
			scale            float64
		}{{"link state", "operstate", "", 0}, {"MTU", "mtu", "bytes", 1}, {"link speed", "speed", "Mbit/s", 1}, {"duplex", "duplex", "", 0}} {
			d.Metrics = append(d.Metrics, c.fileReading(field.name, base+field.file, field.unit, field.scale))
		}
	}
	devices := hardwareSection(r, "Devices")
	for i := range devices.Devices {
		d := &devices.Devices[i]
		if hardwarePCIBus.MatchString(d.ID) {
			continue
		}
		for _, v := range d.Specs {
			if v.Source == "/sys/bus/usb/devices/"+d.ID+"/idVendor" {
				d.Metrics = append(d.Metrics, c.fileReading("link speed", "/sys/bus/usb/devices/"+d.ID+"/speed", "Mbit/s", 1))
				break
			}
		}
	}
}

func (c *HardwareCollector) hardwareSensorReading(name, path, unit string, scale float64) models.HardwareReading {
	stem := strings.TrimSuffix(strings.TrimSuffix(path, "_input"), "_average")
	if flag, err := c.read(stem + "_fault"); err == nil && strings.TrimSpace(string(flag)) == "1" {
		return c.unavailable(name, path, hardwareIssue("sensor reports a hardware fault"))
	}
	if flag, err := c.read(stem + "_enable"); err == nil && strings.TrimSpace(string(flag)) == "0" {
		return c.unavailable(name, path, hardwareIssue("sensor is disabled"))
	}
	return c.fileReading(name, path, unit, scale)
}

func hardwareSMARTError(data map[string]any, err error) error {
	smart, _ := data["smartctl"].(map[string]any)
	messages, _ := smart["messages"].([]any)
	for _, item := range messages {
		message, _ := item.(map[string]any)
		text, _ := message["string"].(string)
		if hardwarePermissionDenied(text) {
			return fs.ErrPermission
		}
	}
	return err
}
