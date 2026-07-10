package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

// RemoteCollector collects facts from a remote node via SSH.
// The Executor interface keeps the collection path decoupled from the current
// transport implementation.
type RemoteCollector struct {
	NodeName string
	Role     string
	Hostname string
	Exec     transport.Executor
}

// NewRemoteCollector creates a remote fact collector.
func NewRemoteCollector(name, role, hostname string, exec transport.Executor) *RemoteCollector {
	return &RemoteCollector{NodeName: name, Role: role, Hostname: hostname, Exec: exec}
}

// Collect gathers facts from the remote node.
// Maps failures precisely:
//   - connect/first-command fail → unreachable
//   - subsequent command fail → partial
func (c *RemoteCollector) Collect(ctx context.Context) (*models.NodeFacts, error) {
	facts := &models.NodeFacts{
		Name: c.NodeName,
		Role: c.Role,
		// SSHTarget is the configured dial address. Hostname is overwritten below
		// with the observed machine hostname after connect; classification must
		// keep using SSHTarget for the route in use.
		SSHTarget:   c.Hostname,
		Hostname:    c.Hostname, // fallback until observed hostname is collected
		Status:      models.StatusComplete,
		CollectedAt: time.Now().UTC(),
	}

	if err := c.Exec.Connect(ctx); err != nil {
		facts.Status = models.StatusUnreachable
		facts.Error = err.Error()
		return facts, nil
	}
	defer c.Exec.Close()

	if exposer, ok := c.Exec.(interface{ HandshakeLatencyMs() int64 }); ok {
		facts.SSHHandshakeLatencyMs = exposer.HandshakeLatencyMs()
	}

	partial := false

	// Detect OS
	osOut, err := c.Exec.Run(ctx, "uname -s")
	if err != nil {
		partial = true
	}
	osName := strings.ToLower(strings.TrimSpace(osOut))
	facts.OS = osName
	if hostname, err := detectRemoteHostname(ctx, c.Exec); err != nil {
		partial = true
		facts.Hostname = c.Hostname
	} else {
		facts.Hostname = hostname
	}
	facts.Identity = detectRemoteNodeIdentity(ctx, c.Exec, osName)

	// Arch
	if archOut, err := c.Exec.Run(ctx, "uname -m"); err != nil {
		partial = true
	} else {
		facts.Arch = strings.TrimSpace(archOut)
	}

	// OS version
	var verCmd string
	if osName == "darwin" {
		verCmd = "sw_vers -productVersion"
	} else {
		verCmd = "uname -r"
	}
	if verOut, err := c.Exec.Run(ctx, verCmd); err != nil {
		partial = true
	} else {
		facts.OSVersion = strings.TrimSpace(verOut)
	}

	// Resources
	res, resPartial := c.remoteResources(ctx, osName, facts.Arch)
	facts.Resources = res
	if resPartial {
		partial = true
	}

	// Network addresses
	facts.Addresses = c.remoteAddresses(ctx)

	// Tools
	facts.Tools = c.remoteTools(ctx)

	ollamaInfo, residentModels := c.discoverOllamaRobust(ctx)
	if ollamaInfo.Installed {
		facts.Ollama = &ollamaInfo
		facts.ResidentModels = residentModels
		facts.Tools = append(facts.Tools, models.ToolInfo{
			Name:    "ollama",
			Path:    ollamaInfo.Path,
			Version: ollamaInfo.Version,
			Class:   models.ToolClassAICLI,
		})
	}
	// Merge llama-server resident models (runtime="llama.cpp") so empirical
	// placement can prefer nodes with the right model already loaded.
	if llamaResidents := c.discoverLlamaServerRobust(ctx); len(llamaResidents) > 0 {
		facts.ResidentModels = append(facts.ResidentModels, llamaResidents...)
	}
	// Merge MLX resident models (runtime="mlx") from the remote mlx_lm.server API.
	if mlxResidents := c.discoverMLXRobust(ctx); len(mlxResidents) > 0 {
		facts.ResidentModels = append(facts.ResidentModels, mlxResidents...)
	}
	facts.TurboQuant = detectTurboQuantSupport(ctx, facts.OS, facts.Arch, facts.Tools, facts.Resources, facts.Ollama, func(ctx context.Context, cmd string) (string, error) {
		return c.Exec.Run(ctx, cmd)
	})

	if partial {
		facts.Status = models.StatusPartial
	}
	facts.PopulateMemoryMetrics()
	return facts, nil
}

func (c *RemoteCollector) remoteResources(ctx context.Context, osName, arch string) (*models.Resources, bool) {
	r := &models.Resources{Pressure: "none"}
	partial := false

	// CPU cores
	var coresCmd, modelCmd string
	if osName == "darwin" {
		coresCmd = "sysctl -n hw.ncpu"
		modelCmd = "sysctl -n machdep.cpu.brand_string 2>/dev/null || sysctl -n hw.model"
	} else {
		coresCmd = "nproc"
		modelCmd = "grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2"
	}

	if out, err := c.Exec.Run(ctx, coresCmd); err != nil {
		partial = true
	} else {
		r.CPUCores, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	if out, err := c.Exec.Run(ctx, modelCmd); err == nil {
		r.CPUModel = strings.TrimSpace(out)
	}
	r.MemoryTopology, r.MemoryClass = detectMemoryTopology(osName, arch, r.CPUModel)

	// RAM
	if osName == "darwin" {
		if out, err := c.Exec.Run(ctx, "sysctl -n hw.memsize"); err != nil {
			partial = true
		} else {
			totalBytes, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
			r.RAMTotalMB = totalBytes / (1024 * 1024)
		}
		if out, err := c.Exec.Run(ctx, "vm_stat"); err != nil {
			partial = true
		} else {
			r.RAMFreeMB = parseDarwinFreeRAM(out)
		}
	} else {
		if out, err := c.Exec.Run(ctx, `grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo`); err != nil {
			partial = true
		} else {
			total, avail, err := parseLinuxMeminfo(out)
			if err != nil {
				partial = true
			} else {
				r.RAMTotalMB = total
				r.RAMFreeMB = avail
			}
		}
	}

	if r.RAMTotalMB > 0 {
		r.Pressure = computePressure(r.RAMTotalMB, r.RAMFreeMB)
		r.PressureSource = "free-ram"
	}

	if source, level, stall10, someAvg10, fullAvg10, ok := c.remotePressureSignal(ctx, osName); ok {
		r.Pressure = mergePressureLevels(r.Pressure, level)
		r.PressureSource = source
		r.PressureStall10 = stall10
		r.MemoryPSISomeAvg10 = someAvg10
		r.MemoryPSIFullAvg10 = fullAvg10
	}

	var loadCmd string
	if osName == "darwin" {
		loadCmd = "sysctl -n vm.loadavg"
	} else {
		loadCmd = "cat /proc/loadavg"
	}
	if out, err := c.Exec.Run(ctx, loadCmd); err != nil {
		partial = true
	} else {
		var load1, load5, load15 float64
		var parseErr error
		if osName == "darwin" {
			load1, load5, load15, parseErr = parseDarwinLoadavg(out)
		} else {
			load1, load5, load15, parseErr = parseLoadavgFields(out)
		}
		if parseErr != nil {
			partial = true
		} else {
			r.Load1M = load1
			r.Load5M = load5
			r.Load15M = load15
		}
	}

	// Disk
	if out, err := c.Exec.Run(ctx, "df -kP /"); err != nil {
		partial = true
	} else {
		total, free, err := parseDFOutput(out)
		if err != nil {
			partial = true
		} else {
			r.DiskTotalGB = total
			r.DiskFreeGB = free
		}
	}

	// GPU (best-effort)
	var gpuCmd string
	if osName == "darwin" {
		gpuCmd = `system_profiler SPDisplaysDataType 2>/dev/null | grep -E 'Chipset Model:|VRAM|Metal' | sed 's/^ *//'`
	} else {
		// Try nvidia-smi first, fall back to lspci
		gpuCmd = `nvidia-smi --query-gpu=name,memory.total --format=csv,noheader,nounits 2>/dev/null || lspci 2>/dev/null | grep -iE 'vga|3d' | sed 's/.*: //'`
	}
	if out, err := c.Exec.Run(ctx, gpuCmd); err == nil {
		out = strings.TrimSpace(out)
		if out != "" {
			// Detect format: nvidia-smi CSV has commas, lspci/system_profiler does not
			if strings.Contains(out, ", ") && !strings.Contains(out, "Chipset Model") {
				r.GPUs = parseNvidiaSMIOutput(out)
			} else if strings.Contains(out, "Chipset Model") {
				r.GPUs = parseSystemProfilerGPUs(out)
			} else {
				for _, line := range strings.Split(out, "\n") {
					if line = strings.TrimSpace(line); line != "" {
						r.GPUs = append(r.GPUs, models.GPUFromString(line))
					}
				}
			}
		}
	}

	// Storage class (best-effort)
	r.StorageClass = c.remoteStorageClass(ctx, osName)

	// Battery and thermal (best-effort)
	if pct, ok := c.remoteBatteryPercent(ctx, osName); ok {
		r.BatteryPercent = &pct
	}
	r.PowerSource = c.remotePowerSource(ctx, osName)
	r.ThermalState = c.remoteThermalState(ctx, osName)
	r.ThermalZones = c.remoteThermalZones(ctx, osName)

	return r, partial
}

func (c *RemoteCollector) remotePressureSignal(ctx context.Context, osName string) (source string, level string, stall10 float64, someAvg float64, fullAvg float64, ok bool) {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "linux":
		out, err := c.Exec.Run(ctx, "cat /proc/pressure/memory 2>/dev/null")
		if err != nil || strings.TrimSpace(out) == "" {
			return "", "", 0, 0, 0, false
		}
		stall10, ok := parseLinuxPressureStall10(out)
		if !ok {
			return "", "", 0, 0, 0, false
		}
		someAvg, fullAvg, _ := parseLinuxPSI(out)
		return "linux-psi", linuxPressureLevel(stall10), stall10, someAvg, fullAvg, true
	case "darwin":
		out, err := c.Exec.Run(ctx, "sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null")
		if err != nil || strings.TrimSpace(out) == "" {
			return "", "", 0, 0, 0, false
		}
		level, ok := parseDarwinMemoryPressureLevel(out)
		if !ok {
			return "", "", 0, 0, 0, false
		}
		someAvg, fullAvg := MapDarwinPressureToPSI(level)
		return "darwin-vm-pressure", darwinPressureLevel(level), 0, someAvg, fullAvg, true
	default:
		return "", "", 0, 0, 0, false
	}
}

func (c *RemoteCollector) remoteAddresses(ctx context.Context) []models.NetworkAddress {
	var addrs []models.NetworkAddress
	// Try `ip -o addr` first (outputs: "2: eth0 inet 192.168.1.5/24 ..."), fallback to basic ip/ifconfig
	cmd := `if command -v ip >/dev/null 2>&1; then ip -o addr show scope global 2>/dev/null || ip addr show scope global | awk '/inet/ {print $2}'; else ifconfig 2>/dev/null | awk '/^[a-z]/ {iface=$1} /inet / && !/127.0.0.1/ {print iface, $2}; /inet6 / && !/::1/ && !/fe80/ {print iface, $2}' | sed 's/://'; fi`

	out, err := c.Exec.Run(ctx, cmd)
	if err != nil {
		return addrs
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		addr := parseRemoteAddrLine(line)
		if addr.Address != "" {
			addrs = append(addrs, addr)
		}
	}
	return addrs
}

// parseRemoteAddrLine parses an address line from `ip -o addr` or fallback output.
// ip -o format: "2: eth0    inet 192.168.1.5/24 brd ..."
// fallback: "eth0 192.168.1.5" or just "192.168.1.5"
func parseRemoteAddrLine(line string) models.NetworkAddress {
	fields := strings.Fields(line)

	var addrField, ifName string
	for i, f := range fields {
		if f == "inet" || f == "inet6" {
			if i+1 < len(fields) {
				addrField = fields[i+1]
			}
			if i >= 2 {
				ifName = strings.TrimSuffix(fields[1], ":")
			}
			break
		}
	}

	// Fallback: might be "ifname 1.2.3.4" or just "1.2.3.4"
	if addrField == "" {
		switch len(fields) {
		case 1:
			addrField = fields[0]
		case 2:
			ifName = fields[0]
			addrField = fields[1]
		}
	}

	ip, subnet := parseAddressWithOptionalCIDR(addrField)
	if ip == nil {
		return models.NetworkAddress{}
	}

	kind := "ipv4"
	if ip.To4() == nil {
		kind = "ipv6"
	}
	return models.NetworkAddress{
		Kind:       kind,
		Address:    ip.String(),
		Interface:  ifName,
		Subnet:     subnet,
		SpeedClass: classifyInterfaceSpeed(ifName, ip),
	}
}

func (c *RemoteCollector) remoteTools(ctx context.Context) []models.ToolInfo {
	toolDefs := defaultToolDefs()
	var tools []models.ToolInfo

	for _, td := range toolDefs {
		pathOut, err := c.Exec.Run(ctx, fmt.Sprintf("command -v %s 2>/dev/null", td.name))
		if err != nil {
			continue
		}
		path := strings.TrimSpace(pathOut)
		if path == "" {
			continue
		}

		ti := models.ToolInfo{
			Name:  td.name,
			Path:  path,
			Class: td.class,
		}

		if td.versionCmd != "" {
			if vOut, err := c.Exec.Run(ctx, td.versionCmd+" 2>/dev/null"); err == nil {
				ti.Version = parseVersionString(vOut)
			}
		}
		tools = append(tools, ti)
	}
	return tools
}

// discoverOllamaRobust does ONE SSH command that gathers everything robustly.
func (c *RemoteCollector) discoverOllamaRobust(ctx context.Context) (models.OllamaInfo, []models.ResidentModel) {
	info := models.OllamaInfo{Installed: false}

	out, err := c.Exec.Run(ctx, OllamaDiscoveryScript)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}

	// parse the JSON blob
	var parsed ollamaDiscoveryPayload
	if json.Unmarshal([]byte(out), &parsed) == nil {
		return parsed.OllamaInfo, parsed.ResidentModels
	}
	return info, nil
}

// discoverLlamaServerRobust probes for a running llama-server process on the
// remote node via a single SSH command and returns its resident models.
func (c *RemoteCollector) discoverLlamaServerRobust(ctx context.Context) []models.ResidentModel {
	out, err := c.Exec.Run(ctx, LlamaServerDiscoveryScript)
	if err != nil {
		return nil
	}
	var parsed llamaServerDiscoveryPayload
	if json.Unmarshal([]byte(out), &parsed) == nil && parsed.Installed {
		return withResidentPort(parsed.ResidentModels, parsed.Port)
	}
	return nil
}

// discoverMLXRobust probes for a running mlx_lm.server process on the remote
// node via a single SSH command and queries its /v1/models endpoint to
// enumerate resident models.
func (c *RemoteCollector) discoverMLXRobust(ctx context.Context) []models.ResidentModel {
	out, err := c.Exec.Run(ctx, MLXDiscoveryScript)
	if err != nil {
		return nil
	}
	var parsed mlxDiscoveryPayload
	if json.Unmarshal([]byte(out), &parsed) == nil && parsed.Installed {
		return withResidentPort(parsed.ResidentModels, parsed.Port)
	}
	return nil
}

func (c *RemoteCollector) remoteStorageClass(ctx context.Context, osName string) string {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "darwin":
		out, err := c.Exec.Run(ctx, "diskutil info / 2>/dev/null")
		if err != nil {
			return "unknown"
		}
		return parseDiskutilStorageClass(out)
	case "linux":
		out, err := c.Exec.Run(ctx, `findmnt -n -o SOURCE / 2>/dev/null`)
		if err != nil {
			return "unknown"
		}
		return resolveLinuxStorageClass(
			strings.TrimSpace(out),
			func(device string) (linuxBlockDeviceInfo, error) {
				return c.remoteLinuxBlockDeviceInfo(ctx, device)
			},
			func(info linuxBlockDeviceInfo) ([]string, error) {
				return c.remoteLinuxBlockDeviceSlaves(ctx, info)
			},
			func(device string) (string, error) {
				return c.remoteLinuxRotational(ctx, device)
			},
		)
	default:
		return "unknown"
	}
}

func (c *RemoteCollector) remoteLinuxBlockDeviceInfo(ctx context.Context, device string) (linuxBlockDeviceInfo, error) {
	out, err := c.Exec.Run(ctx, fmt.Sprintf("lsblk -J -n -p -o NAME,KNAME,PKNAME,TYPE,ROTA %q 2>/dev/null", strings.TrimSpace(device)))
	if err != nil {
		return linuxBlockDeviceInfo{}, err
	}
	return parseLinuxBlockDeviceInfo(out)
}

func (c *RemoteCollector) remoteLinuxBlockDeviceSlaves(ctx context.Context, info linuxBlockDeviceInfo) ([]string, error) {
	sysfsName := linuxSysfsBlockName(info)
	if sysfsName == "" {
		return nil, fmt.Errorf("no sysfs block name for %+v", info)
	}
	out, err := c.Exec.Run(ctx, fmt.Sprintf("ls -1 /sys/class/block/%s/slaves 2>/dev/null", sysfsName))
	if err != nil {
		return nil, err
	}

	var parents []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name := filepath.Base(strings.TrimSpace(line))
		if name == "" {
			continue
		}
		parents = append(parents, filepath.Join("/dev", name))
	}
	sort.Strings(parents)
	return parents, nil
}

func (c *RemoteCollector) remoteLinuxRotational(ctx context.Context, device string) (string, error) {
	base := fallbackLinuxBlockBase(device)
	if base == "" {
		return "", fmt.Errorf("no block device base for %q", device)
	}
	return c.Exec.Run(ctx, fmt.Sprintf("cat /sys/block/%s/queue/rotational 2>/dev/null", base))
}

func (c *RemoteCollector) remoteBatteryPercent(ctx context.Context, osName string) (int, bool) {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "darwin":
		out, err := c.Exec.Run(ctx, "pmset -g batt 2>/dev/null")
		if err != nil {
			return 0, false
		}
		return parsePmsetBattery(out)
	case "linux":
		out, err := c.Exec.Run(ctx, "cat /sys/class/power_supply/BAT0/capacity /sys/class/power_supply/BAT1/capacity /sys/class/power_supply/BATT/capacity 2>/dev/null | head -1")
		if err != nil {
			return 0, false
		}
		if pct, err := strconv.Atoi(strings.TrimSpace(out)); err == nil && pct >= 0 && pct <= 100 {
			return pct, true
		}
		return 0, false
	default:
		return 0, false
	}
}

func (c *RemoteCollector) remoteThermalState(ctx context.Context, osName string) string {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "darwin":
		out, err := c.Exec.Run(ctx, "pmset -g therm 2>/dev/null")
		if err != nil {
			return ""
		}
		return parsePmsetThermal(out)
	case "linux":
		out, err := c.Exec.Run(ctx, "cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null")
		if err != nil {
			return ""
		}
		var maxTemp int
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if temp, err := strconv.Atoi(strings.TrimSpace(line)); err == nil && temp > maxTemp {
				maxTemp = temp
			}
		}
		if maxTemp == 0 {
			return ""
		}
		tempC := maxTemp / 1000
		switch {
		case tempC >= 95:
			return "critical"
		case tempC >= 85:
			return "serious"
		case tempC >= 75:
			return "fair"
		default:
			return "nominal"
		}
	default:
		return ""
	}
}

func (c *RemoteCollector) remotePowerSource(ctx context.Context, osName string) string {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "darwin":
		out, err := c.Exec.Run(ctx, "pmset -g batt 2>/dev/null")
		if err != nil {
			return ""
		}
		return parsePmsetPowerSource(out)
	case "linux":
		for _, name := range []string{"BAT0", "BAT1", "BATT", "AC", "ACAD", "ADP1"} {
			out, err := c.Exec.Run(ctx, fmt.Sprintf("cat /sys/class/power_supply/%s/status 2>/dev/null", name))
			if err != nil {
				continue
			}
			status := strings.TrimSpace(out)
			switch strings.ToLower(status) {
			case "charging", "full", "not charging":
				return "ac"
			case "discharging":
				return "battery"
			}
		}
		return ""
	default:
		return ""
	}
}

func (c *RemoteCollector) remoteThermalZones(ctx context.Context, osName string) []models.ThermalZone {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "linux":
		out, err := c.Exec.Run(ctx, "for z in /sys/class/thermal/thermal_zone*; do cat \"$z/temp\" 2>/dev/null; echo; cat \"$z/type\" 2>/dev/null; echo; done")
		if err != nil || out == "" {
			return nil
		}
		var zones []models.ThermalZone
		lines := strings.Split(strings.TrimSpace(out), "\n")
		for i := 0; i+1 < len(lines); i += 2 {
			tempMilli, err := strconv.Atoi(strings.TrimSpace(lines[i]))
			if err != nil {
				continue
			}
			tempC := float64(tempMilli) / 1000.0
			zoneType := strings.TrimSpace(lines[i+1])
			if zoneType == "" {
				zoneType = fmt.Sprintf("zone_%d", len(zones))
			}
			zones = append(zones, models.ThermalZone{
				Type:  zoneType,
				TempC: tempC,
				State: thermalStateFromTempC(tempC),
			})
		}
		return zones
	case "darwin":
		out, err := c.Exec.Run(ctx, "pmset -g therm 2>/dev/null")
		if err != nil {
			return nil
		}
		limit := parseCPUThermalLimit(out)
		if limit == 0 {
			return nil
		}
		state := "nominal"
		switch {
		case limit < 50:
			state = "critical"
		case limit < 80:
			state = "serious"
		case limit < 100:
			state = "fair"
		}
		return []models.ThermalZone{{Type: "cpu", State: state}}
	default:
		return nil
	}
}
