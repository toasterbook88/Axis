package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

var macHardwareTypes = []string{"SPHardwareDataType", "SPMemoryDataType", "SPDisplaysDataType", "SPStorageDataType", "SPNetworkDataType", "SPPowerDataType", "SPUSBDataType", "SPPCIDataType", "SPAudioDataType"}

// Explicit field selection prevents profiler serials, UUIDs, addresses and
// battery identifiers from escaping into the report.
var macHardwareFields = map[string]string{
	"machine_name": "model", "machine_model": "model identifier", "model_number": "model number",
	"chip_type": "chip", "number_processors": "processor configuration", "physical_memory": "installed memory",
	"boot_rom_version": "boot firmware", "os_loader_version": "OS loader firmware",
	"dimm_manufacturer": "manufacturer", "dimm_type": "memory type", "dimm_size": "capacity", "dimm_speed": "speed", "dimm_status": "status", "dimm_part_number": "part number",
	"ecc_enabled": "ECC", "sppci_model": "model", "sppci_vendor": "vendor", "spdisplays_vendor": "vendor",
	"sppci_bus": "bus", "sppci_cores": "GPU cores", "spdisplays_vram": "dedicated VRAM", "spdisplays_vram_shared": "shared graphics memory", "spdisplays_mtlgpufamilysupport": "Metal support",
	"spdisplays_refresh_rate": "refresh rate", "spdisplays_pixelresolution": "display resolution", "_spdisplays_resolution": "display resolution", "spdisplays_connection_type": "connection", "spdisplays_display_type": "display type",
	"bsd_name": "BSD device", "file_system": "filesystem", "size_in_bytes": "capacity", "mount_point": "mount",
	"device_name": "device", "media_name": "media", "medium_type": "medium type", "protocol": "protocol", "smart_status": "SMART status", "partition_map_type": "partition table", "is_internal_disk": "internal",
	"interface": "interface", "hardware": "hardware", "type": "type", "sppci_link_width": "PCIe width", "sppci_link_speed": "PCIe speed", "sppci_driver_installed": "driver installed",
	"sppower_battery_cycle_count": "battery cycles", "sppower_battery_health": "battery condition", "sppower_battery_health_maximum_capacity": "battery health",
	"sppower_ac_charger_watts": "charger capacity",
	"manufacturer":             "manufacturer", "bcd_device": "device revision", "bus_power": "available bus current", "device_speed": "link speed", "extra_current_used": "extra current",
	"coreaudio_device_manufacturer": "manufacturer", "coreaudio_device_srate": "sample rate", "coreaudio_device_transport": "transport",
}

func (c *HardwareCollector) macHardwareInventory(ctx context.Context, r *models.HardwareReport) {
	args := append([]string{"-json", "-detailLevel", "mini"}, macHardwareTypes...)
	raw, err := c.run(ctx, "system_profiler", args...)
	if err == nil {
		err = c.parseMacHardwareInventory(raw, r)
	}
	if err != nil {
		for _, name := range []string{"Memory", "GPU", "Storage", "Network", "System", "Devices"} {
			d := hardwareDevice(strings.ToLower(name), "Hardware inventory")
			d.Specs = append(d.Specs, c.unavailable("inventory", "system_profiler -json", err))
			hardwareSection(r, name).Devices = append(hardwareSection(r, name).Devices, d)
		}
	}
	d := hardwareDevice("cpu", "Processor")
	keys := []string{"machdep.cpu.brand_string", "hw.model", "hw.physicalcpu", "hw.logicalcpu", "hw.packages", "hw.cachelinesize", "hw.l1icachesize", "hw.l1dcachesize", "hw.l2cachesize", "hw.l3cachesize", "hw.cpufrequency", "hw.cpufrequency_min", "hw.cpufrequency_max", "machdep.cpu.features", "machdep.cpu.leaf7_features", "hw.perflevel0.physicalcpu", "hw.perflevel1.physicalcpu"}
	raw, probeErr := c.run(ctx, "sysctl", keys...)
	values := hardwareKeyValues(string(raw), ":")
	for _, key := range keys {
		if v := values[key]; v != "" {
			unit := ""
			var value any = v
			if n, ok := hardwareNumber(v); ok {
				value = n
				if strings.Contains(key, "cachesize") || strings.HasSuffix(key, "cachelinesize") {
					unit = "bytes"
				}
				if strings.Contains(key, "cpufrequency") {
					unit = "Hz"
				}
			}
			d.Specs = append(d.Specs, c.reading(key, value, unit, "sysctl "+key))
		} else {
			d.Specs = append(d.Specs, c.unavailable(key, "sysctl "+key, probeErr))
		}
	}
	hardwareSection(r, "CPU").Devices = append(hardwareSection(r, "CPU").Devices, d)
	mem := hardwareDevice("memory", "System memory")
	raw, e := c.run(ctx, "sysctl", "-n", "hw.memsize")
	if n, ok := hardwareNumber(string(raw)); e == nil && ok {
		mem.Specs = append(mem.Specs, c.reading("capacity", n, "bytes", "sysctl hw.memsize"))
	} else {
		mem.Specs = append(mem.Specs, c.unavailable("capacity", "sysctl hw.memsize", e))
	}
	raw, e = c.run(ctx, "sysctl", "-n", "hw.optional.arm64")
	if e == nil && strings.TrimSpace(string(raw)) == "1" {
		mem.Specs = append(mem.Specs, c.reading("topology", "unified CPU/GPU memory; no separate dedicated VRAM total", "", "sysctl hw.optional.arm64"))
	}
	hardwareSection(r, "Memory").Devices = append([]models.HardwareDevice{mem}, hardwareSection(r, "Memory").Devices...)
	sensors := hardwareDevice("sensors", "Thermal and power sensors")
	sensors.Specs = append(sensors.Specs, c.unavailable("temperatures, fan RPM, CPU/GPU watts", "macOS unprivileged interfaces", nil))
	hardwareSection(r, "Sensors").Devices = append(hardwareSection(r, "Sensors").Devices, sensors)
}

func (c *HardwareCollector) parseMacHardwareInventory(raw []byte, r *models.HardwareReport) error {
	var tree map[string][]map[string]any
	if err := json.Unmarshal(raw, &tree); err != nil {
		return err
	}
	section := map[string]string{"SPHardwareDataType": "System", "SPMemoryDataType": "Memory", "SPDisplaysDataType": "GPU", "SPStorageDataType": "Storage", "SPNetworkDataType": "Network", "SPPowerDataType": "Sensors", "SPUSBDataType": "Devices", "SPPCIDataType": "Devices", "SPAudioDataType": "Devices"}
	for _, typ := range macHardwareTypes {
		var walk func(map[string]any, string)
		walk = func(n map[string]any, parent string) {
			name, _ := n["_name"].(string)
			if name == "" {
				name = typ
			}
			if model, ok := n["sppci_model"].(string); ok && model != "" {
				name = model
			}
			if model, ok := n["machine_name"].(string); ok && model != "" {
				name = model
			}
			s := hardwareSection(r, section[typ])
			d := hardwareDevice(fmt.Sprintf("%s-%d", typ, len(s.Devices)), name)
			if id, ok := n["bsd_name"].(string); ok {
				d.ID = id
			}
			// Network metrics join by the local interface name, not service order.
			if id, ok := n["interface"].(string); ok {
				d.ID = id
			}
			for _, key := range slices.Sorted(maps.Keys(n)) {
				value := n[key]
				if label, ok := macHardwareFields[key]; ok {
					switch value.(type) {
					case string, float64, bool:
						unit := ""
						if strings.HasSuffix(key, "_in_bytes") {
							unit = "bytes"
						}
						d.Specs = append(d.Specs, c.reading(label, value, unit, "system_profiler "+typ))
					}
				}
			}
			if len(d.Specs) > 0 {
				if parent != "" {
					d.Specs = append(d.Specs, c.reading("parent", parent, "", "system_profiler "+typ))
				}
				s.Devices = append(s.Devices, d)
			}
			for _, key := range slices.Sorted(maps.Keys(n)) {
				switch x := n[key].(type) {
				case map[string]any:
					walk(x, d.ID)
				case []any:
					for _, v := range x {
						if child, ok := v.(map[string]any); ok {
							walk(child, d.ID)
						}
					}
				}
			}
		}
		for _, n := range tree[typ] {
			walk(n, "")
		}
	}
	return nil
}

var macCPUUsage = regexp.MustCompile(`CPU usage:\s*([\d.]+)% user,\s*([\d.]+)% sys,\s*([\d.]+)% idle`)
var macVMPageSize = regexp.MustCompile(`page size of (\d+) bytes`)

func parseMacVM(raw string) map[string]float64 {
	result := map[string]float64{}
	match := macVMPageSize.FindStringSubmatch(raw)
	if len(match) != 2 {
		return result
	}
	size, _ := strconv.ParseFloat(match[1], 64)
	for key, v := range hardwareKeyValues(raw, ":") {
		n, ok := hardwareNumber(strings.TrimSuffix(v, "."))
		if ok {
			result[key] = n * size
		}
	}
	return result
}

func (c *HardwareCollector) macHardwareMetrics(ctx context.Context, r *models.HardwareReport) {
	// top's first frame is not a one-second utilization sample; use only frame 2.
	raw, err := c.run(ctx, "top", "-l", "2", "-s", "1", "-n", "0")
	cpu := &hardwareSection(r, "CPU").Devices[0]
	matches := macCPUUsage.FindAllStringSubmatch(string(raw), -1)
	if err == nil && len(matches) >= 2 {
		m := matches[len(matches)-1]
		for i, key := range []string{"user", "system", "idle"} {
			n, _ := strconv.ParseFloat(m[i+1], 64)
			v := c.reading(key, n, "%", "top -l 2 -s 1 (second frame, 1s sample)")
			v.SampleSeconds = 1
			cpu.Metrics = append(cpu.Metrics, v)
		}
	} else {
		cpu.Metrics = append(cpu.Metrics, c.unavailable("CPU utilization", "top -l 2 -s 1", err))
	}
	cpu.Metrics = append(cpu.Metrics, c.unavailable("per-core utilization and current clocks", "macOS unprivileged interfaces", nil))
	mem := &hardwareSection(r, "Memory").Devices[0]
	raw, err = c.run(ctx, "vm_stat")
	values := parseMacVM(string(raw))
	if len(values) == 0 {
		mem.Metrics = append(mem.Metrics, c.unavailable("memory usage", "vm_stat", err))
	}
	for _, key := range []string{"Pages free", "Pages active", "Pages inactive", "Pages speculative", "Pages wired down", "Pages purgeable", "Pages occupied by compressor"} {
		if v, ok := values[key]; ok {
			mem.Metrics = append(mem.Metrics, c.reading(key, v, "bytes", "vm_stat"))
		}
	}
	raw, err = c.run(ctx, "sysctl", "vm.swapusage")
	swap := regexp.MustCompile(`(total|used|free) =\s*([\d.]+)([MG])`).FindAllStringSubmatch(string(raw), -1)
	if err == nil && len(swap) > 0 {
		for _, m := range swap {
			n, _ := strconv.ParseFloat(m[2], 64)
			scale := float64(1 << 20)
			if m[3] == "G" {
				scale = 1 << 30
			}
			mem.Metrics = append(mem.Metrics, c.reading("swap "+m[1], n*scale, "bytes", "sysctl vm.swapusage"))
		}
	} else {
		mem.Metrics = append(mem.Metrics, c.unavailable("swap", "sysctl vm.swapusage", err))
	}
	raw, err = c.run(ctx, "pmset", "-g", "batt")
	power := hardwareDevice("power", "Power source")
	if err == nil {
		source := "unknown"
		if strings.Contains(string(raw), "'AC Power'") {
			source = "AC"
		}
		if strings.Contains(string(raw), "'Battery Power'") {
			source = "battery"
		}
		power.Metrics = append(power.Metrics, c.reading("source", source, "", "pmset -g batt"))
		if m := regexp.MustCompile(`(\d+)%`).FindStringSubmatch(string(raw)); len(m) == 2 {
			n, _ := strconv.Atoi(m[1])
			power.Metrics = append(power.Metrics, c.reading("charge", n, "%", "pmset -g batt"))
		}
	} else {
		power.Metrics = append(power.Metrics, c.unavailable("power source", "pmset -g batt", err))
	}
	hardwareSection(r, "Sensors").Devices = append(hardwareSection(r, "Sensors").Devices, power)
	c.hardwareVolumes(ctx, r)
	c.macNetworkMetrics(ctx, r)
	c.macGPUMetrics(ctx, r)
}

var macGPUCounter = regexp.MustCompile(`"(Device Utilization %|Renderer Utilization %|Tiler Utilization %|In use system memory|Alloc system memory)"=(\d+)`)

func (c *HardwareCollector) macGPUMetrics(ctx context.Context, r *models.HardwareReport) {
	raw, err := c.run(ctx, "ioreg", "-r", "-c", "IOAccelerator", "-d", "1", "-l", "-w", "0")
	found := false
	for _, line := range strings.Split(string(raw), "\n") {
		if !strings.Contains(line, `"PerformanceStatistics"`) {
			continue
		}
		matches := macGPUCounter.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			continue
		}
		found = true
		d := hardwareDevice(fmt.Sprintf("accelerator-%d", len(hardwareSection(r, "GPU").Devices)), "GPU accelerator (driver counters)")
		for _, m := range matches {
			n, _ := strconv.ParseFloat(m[2], 64)
			unit := "bytes"
			if strings.HasSuffix(m[1], "%") {
				unit = "%"
			}
			d.Metrics = append(d.Metrics, c.reading(m[1], n, unit, "ioreg IOAccelerator PerformanceStatistics"))
		}
		d.Metrics = append(d.Metrics, c.unavailable("GPU clocks and power", "macOS unprivileged interfaces", nil))
		hardwareSection(r, "GPU").Devices = append(hardwareSection(r, "GPU").Devices, d)
	}
	if !found {
		hardwareSection(r, "GPU").Devices[0].Metrics = append(hardwareSection(r, "GPU").Devices[0].Metrics, c.unavailable("GPU utilization and memory activity", "ioreg IOAccelerator", err))
	}
}

// Only link characteristics are retained from ifconfig; hardware addresses and
// IPv4/IPv6 addresses are deliberately excluded.
func (c *HardwareCollector) macNetworkMetrics(ctx context.Context, r *models.HardwareReport) {
	raw, err := c.run(ctx, "ifconfig", "-a")
	if err != nil {
		hardwareSection(r, "Network").Devices[0].Metrics = append(hardwareSection(r, "Network").Devices[0].Metrics, c.unavailable("link characteristics", "ifconfig -a", err))
		return
	}
	links := map[string]map[string]string{}
	current := ""
	for _, line := range strings.Split(string(raw), "\n") {
		if line != "" && !strings.HasPrefix(line, "\t") && !strings.HasPrefix(line, " ") {
			name, _, ok := strings.Cut(line, ":")
			if !ok {
				continue
			}
			current = name
			links[current] = map[string]string{}
			f := strings.Fields(line)
			for i := range f {
				if f[i] == "mtu" && i+1 < len(f) {
					links[current]["MTU"] = f[i+1]
				}
			}
			continue
		}
		if current == "" {
			continue
		}
		k, v, ok := strings.Cut(strings.TrimSpace(line), ":")
		if ok && (k == "media" || k == "status") {
			links[current][k] = strings.TrimSpace(v)
		}
	}
	s := hardwareSection(r, "Network")
	for i := range s.Devices {
		d := &s.Devices[i]
		for _, key := range []string{"MTU", "media", "status"} {
			if value := links[d.ID][key]; value != "" {
				d.Metrics = append(d.Metrics, c.reading(key, value, "", "ifconfig -a"))
			}
		}
	}
}
