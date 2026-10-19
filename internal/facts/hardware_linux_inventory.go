package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func (c *HardwareCollector) linuxHardwareInventory(ctx context.Context, r *models.HardwareReport) {
	c.linuxCPUInventory(ctx, r)
	mem := hardwareDevice("memory", "System memory")
	mem.Specs = append(mem.Specs, c.reading("topology", "system RAM; discrete GPU memory reported separately", "", "/proc/meminfo"))
	hardwareSection(r, "Memory").Devices = append(hardwareSection(r, "Memory").Devices, mem)
	c.linuxDIMMs(ctx, r)
	c.linuxBlockInventory(ctx, r)
	c.linuxNetworkInventory(r)
	c.linuxSystemInventory(r)
	c.linuxPCIInventory(r)
	c.linuxPCINames(ctx, r)
	c.linuxSensorInventory(r)
}

func (c *HardwareCollector) linuxCPUInventory(ctx context.Context, r *models.HardwareReport) {
	d := hardwareDevice("cpu", "Processor")
	raw, err := c.run(ctx, "lscpu", "--json")

	if err == nil {
		var fields []models.HardwareReading
		fields, err = c.parseHardwareLscpu(raw)
		d.Specs = append(d.Specs, fields...)
	}

	if len(d.Specs) == 0 {
		d.Specs = append(d.Specs, c.unavailable("extended CPU specifications", "lscpu --json", err))
		raw, e := c.read("/proc/cpuinfo")
		if e == nil {
			fields := hardwareKeyValues(string(raw), ":")
			for _, key := range []string{"model name", "Hardware", "CPU architecture", "vendor_id", "flags", "Features"} {
				if v := fields[key]; v != "" {
					d.Specs = append(d.Specs, c.reading(key, v, "", "/proc/cpuinfo"))
				}
			}
		}
	}
	// Per-logical-CPU clocks and package/core IDs are independently observable.
	paths, _ := c.glob("/sys/devices/system/cpu/cpu[0-9]*")
	for _, p := range paths {
		core := hardwareDevice(filepath.Base(p), filepath.Base(p))
		for _, f := range []struct{ name, file string }{{"package", "topology/physical_package_id"}, {"core", "topology/core_id"}, {"thread siblings", "topology/thread_siblings_list"}, {"minimum frequency", "cpufreq/cpuinfo_min_freq"}, {"maximum frequency", "cpufreq/cpuinfo_max_freq"}} {
			unit, scale := "", 0.0
			if strings.Contains(f.name, "frequency") {
				unit, scale = "MHz", 0.001
			}
			core.Specs = append(core.Specs, c.fileReading(f.name, p+"/"+f.file, unit, scale))
		}
		hardwareSection(r, "CPU").Devices = append(hardwareSection(r, "CPU").Devices, core)
	}
	hardwareSection(r, "CPU").Devices = append([]models.HardwareDevice{d}, hardwareSection(r, "CPU").Devices...)
}

func (c *HardwareCollector) linuxDIMMs(ctx context.Context, r *models.HardwareReport) {
	raw, err := c.run(ctx, "dmidecode", "--type", "memory")
	if err != nil {
		d := hardwareDevice("dimms", "Memory modules")
		d.Specs = append(d.Specs, c.unavailable("DIMM inventory", "dmidecode --type memory", err))
		hardwareSection(r, "Memory").Devices = append(hardwareSection(r, "Memory").Devices, d)
		return
	}
	for _, block := range strings.Split("\n"+string(raw), "\nPhysical Memory Array\n")[1:] {
		if cut := strings.Index(block, "\nHandle "); cut >= 0 {
			block = block[:cut]
		}
		values := hardwareKeyValues(block, ":")
		for _, key := range []string{"Error Correction Type", "Maximum Capacity", "Number Of Devices"} {
			if value := values[key]; value != "" {
				hardwareSection(r, "Memory").Devices[0].Specs = append(hardwareSection(r, "Memory").Devices[0].Specs, c.reading(key, value, "", "dmidecode --type memory"))
			}
		}
	}

	for _, block := range strings.Split("\n"+string(raw), "\nMemory Device\n")[1:] {
		// Stop at the next SMBIOS structure; do not leak serials or asset tags.
		if cut := strings.Index(block, "\nHandle "); cut >= 0 {
			block = block[:cut]
		}
		v := hardwareKeyValues(block, ":")
		id := fmt.Sprintf("dimm%d", len(hardwareSection(r, "Memory").Devices))
		d := hardwareDevice(id, "Memory slot "+v["Locator"])
		for _, key := range []string{"Locator", "Bank Locator", "Size", "Form Factor", "Type", "Type Detail", "Speed", "Configured Memory Speed", "Manufacturer", "Part Number", "Data Width", "Total Width", "Rank", "Configured Voltage"} {
			if x := v[key]; x != "" {
				d.Specs = append(d.Specs, c.reading(key, x, "", "dmidecode --type memory"))
			}
		}
		hardwareSection(r, "Memory").Devices = append(hardwareSection(r, "Memory").Devices, d)
	}
}

func (c *HardwareCollector) linuxBlockInventory(ctx context.Context, r *models.HardwareReport) {
	// Explicit allowlist: no disk serials, UUIDs, filesystem labels, or WWNs.
	raw, err := c.run(ctx, "lsblk", "--json", "--bytes", "--output", "NAME,KNAME,TYPE,SIZE,MODEL,VENDOR,REV,TRAN,ROTA,RM,LOG-SEC,PHY-SEC,FSTYPE,MOUNTPOINT")
	var tree struct {
		Devices []map[string]any `json:"blockdevices"`
	}
	if err == nil {
		err = json.Unmarshal(raw, &tree)
	}
	if err != nil {
		d := hardwareDevice("storage", "Block inventory")
		d.Specs = append(d.Specs, c.unavailable("drives and partitions", "lsblk --json", err))
		hardwareSection(r, "Storage").Devices = append(hardwareSection(r, "Storage").Devices, d)
		return
	}
	var walk func([]map[string]any, string)
	walk = func(nodes []map[string]any, parent string) {
		for _, n := range nodes {
			id, _ := n["kname"].(string)
			if id == "" {
				continue
			}
			typ, _ := n["type"].(string)
			if typ == "loop" {
				continue
			}
			d := hardwareDevice(id, fmt.Sprint(n["name"]))
			for _, key := range []string{"type", "size", "model", "vendor", "rev", "tran", "rota", "rm", "log-sec", "phy-sec", "fstype", "mountpoint"} {
				if v, ok := n[key]; ok && v != nil && v != "" {
					unit := ""
					if key == "size" || key == "log-sec" || key == "phy-sec" {
						unit = "bytes"
					}
					d.Specs = append(d.Specs, c.reading(key, v, unit, "lsblk --json --bytes"))
				}
			}
			if parent != "" {
				d.Specs = append(d.Specs, c.reading("parent", parent, "", "lsblk --json"))
			}

			hardwareSection(r, "Storage").Devices = append(hardwareSection(r, "Storage").Devices, d)
			if children, ok := n["children"].([]any); ok {
				list := []map[string]any{}
				for _, ch := range children {
					if m, ok := ch.(map[string]any); ok {
						list = append(list, m)
					}
				}
				walk(list, id)
			}
		}
	}
	walk(tree.Devices, "")
}

func (c *HardwareCollector) linuxNetworkInventory(r *models.HardwareReport) {
	paths, _ := c.glob("/sys/class/net/*")
	for _, p := range paths {
		id := filepath.Base(p)
		d := hardwareDevice(id, id)
		// Virtual interfaces remain visible, without inventing a physical NIC model.
		for _, f := range []struct {
			name, file, unit string
			scale            float64
		}{{"MTU", "mtu", "bytes", 1}, {"link speed", "speed", "Mbit/s", 1}, {"duplex", "duplex", "", 0}, {"vendor ID", "device/vendor", "", 0}, {"device ID", "device/device", "", 0}} {
			d.Specs = append(d.Specs, c.fileReading(f.name, p+"/"+f.file, f.unit, f.scale))
		}
		hardwareSection(r, "Network").Devices = append(hardwareSection(r, "Network").Devices, d)
	}
}

func (c *HardwareCollector) linuxSystemInventory(r *models.HardwareReport) {
	d := hardwareDevice("system", "System and firmware")
	for _, f := range []string{"sys_vendor", "product_name", "product_version", "board_vendor", "board_name", "board_version", "bios_vendor", "bios_version", "bios_date", "chassis_type"} {
		d.Specs = append(d.Specs, c.fileReading(strings.ReplaceAll(f, "_", " "), "/sys/class/dmi/id/"+f, "", 0))
	}
	hardwareSection(r, "System").Devices = append(hardwareSection(r, "System").Devices, d)
}

func (c *HardwareCollector) linuxPCIInventory(r *models.HardwareReport) {
	paths, _ := c.glob("/sys/bus/pci/devices/*")
	for _, p := range paths {
		d := hardwareDevice(filepath.Base(p), "PCI "+filepath.Base(p))
		for _, f := range []string{"vendor", "device", "class", "revision", "numa_node", "max_link_speed", "max_link_width"} {
			d.Specs = append(d.Specs, c.fileReading(f, p+"/"+f, "", 0))
		}
		class, _ := c.read(p + "/class")
		section := "Devices"
		if strings.HasPrefix(strings.TrimSpace(string(class)), "0x03") {
			section = "GPU"
		}
		hardwareSection(r, section).Devices = append(hardwareSection(r, section).Devices, d)
	}
	usb, _ := c.glob("/sys/bus/usb/devices/*")
	for _, p := range usb {
		vendor, err := c.read(p + "/idVendor")
		if err != nil {
			continue
		}
		d := hardwareDevice(filepath.Base(p), "USB "+filepath.Base(p))
		d.Specs = append(d.Specs, c.reading("vendor ID", strings.TrimSpace(string(vendor)), "", p+"/idVendor"))
		for _, f := range []string{"idProduct", "manufacturer", "product", "version", "speed"} {
			unit, scale := "", 0.0
			if f == "speed" {
				unit, scale = "Mbit/s", 1
			}
			d.Specs = append(d.Specs, c.fileReading(f, p+"/"+f, unit, scale))
		}
		hardwareSection(r, "Devices").Devices = append(hardwareSection(r, "Devices").Devices, d)
	}
}

func (c *HardwareCollector) linuxSensorInventory(r *models.HardwareReport) {
	paths, _ := c.glob("/sys/class/hwmon/hwmon*")
	for _, p := range paths {
		d := hardwareDevice(p, "Hardware monitor "+filepath.Base(p))
		d.Specs = append(d.Specs, c.fileReading("chip", p+"/name", "", 0))
		hardwareSection(r, "Sensors").Devices = append(hardwareSection(r, "Sensors").Devices, d)
	}
	zones, _ := c.glob("/sys/class/thermal/thermal_zone*")
	for _, path := range zones {
		d := hardwareDevice(path, "Thermal zone "+filepath.Base(path))
		d.Specs = append(d.Specs, c.fileReading("sensor", path+"/type", "", 0))
		hardwareSection(r, "Sensors").Devices = append(hardwareSection(r, "Sensors").Devices, d)
	}

	paths, _ = c.glob("/sys/class/power_supply/*")
	for _, p := range paths {
		d := hardwareDevice(p, "Power supply "+filepath.Base(p))
		for _, f := range []string{"type", "manufacturer", "model_name", "technology", "energy_full_design", "energy_full", "charge_full_design", "charge_full"} {
			unit, scale := "", 0.0
			if strings.HasPrefix(f, "energy_") {
				unit, scale = "Wh", 0.000001
			}
			if strings.HasPrefix(f, "charge_") {
				unit, scale = "Ah", 0.000001
			}
			d.Specs = append(d.Specs, c.fileReading(f, p+"/"+f, unit, scale))
		}
		hardwareSection(r, "Sensors").Devices = append(hardwareSection(r, "Sensors").Devices, d)
	}
}

// lspci's machine-readable blocks enrich numeric sysfs IDs without copying
// serial/VPD fields. The bus address also joins a physical NIC to its device.
func (c *HardwareCollector) linuxPCINames(ctx context.Context, r *models.HardwareReport) {
	raw, err := c.run(ctx, "lspci", "-D", "-vmm", "-k")
	if err != nil {
		s := hardwareSection(r, "Devices")
		if len(s.Devices) > 0 {
			s.Devices[0].Specs = append(s.Devices[0].Specs, c.unavailable("PCI device names and drivers", "lspci -D -vmm -k", err))
		}
		return
	}
	names := map[string]map[string]string{}
	for _, block := range strings.Split(string(raw), "\n\n") {
		fields := hardwareKeyValues(block, ":")
		if slot := fields["Slot"]; slot != "" {
			names[slot] = fields
		}
	}
	for _, section := range []string{"GPU", "Devices", "Network"} {
		s := hardwareSection(r, section)
		for i := range s.Devices {
			d := &s.Devices[i]
			slot := d.ID
			if section == "Network" {
				path := "/sys/class/net/" + d.ID + "/device/uevent"
				raw, e := c.read(path)
				if e != nil {
					continue
				}
				values := hardwareKeyValues(string(raw), "=")
				slot = values["PCI_SLOT_NAME"]
				if driver := values["DRIVER"]; driver != "" {
					d.Specs = append(d.Specs, c.reading("driver", driver, "", path))
				}
			}
			fields, ok := names[slot]
			if !ok {
				continue
			}
			if model := fields["Device"]; model != "" {
				d.Name = model
			}
			for _, key := range []string{"Vendor", "Device", "Class", "SVendor", "SDevice", "Rev", "Driver", "Module"} {
				if value := fields[key]; value != "" {
					d.Specs = append(d.Specs, c.reading(key, value, "", "lspci -D -vmm -k"))
				}
			}
		}
	}
}

type hardwareCPUField struct {
	Field    string             `json:"field"`
	Data     any                `json:"data"`
	Children []hardwareCPUField `json:"children"`
}

func (c *HardwareCollector) parseHardwareLscpu(raw []byte) ([]models.HardwareReading, error) {
	var parsed struct {
		CPU []hardwareCPUField `json:"lscpu"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return nil, err
	}
	readings := []models.HardwareReading{}
	var walk func([]hardwareCPUField)
	walk = func(fields []hardwareCPUField) {
		for _, v := range fields {
			key := strings.TrimSuffix(v.Field, ":")
			if strings.Contains(strings.ToLower(key), "vulnerabil") {
				continue
			}
			if text, ok := v.Data.(string); ok && text != "" {
				readings = append(readings, c.reading(key, text, "", "lscpu --json"))
			}
			walk(v.Children)
		}
	}
	walk(parsed.CPU)
	return readings, nil
}
