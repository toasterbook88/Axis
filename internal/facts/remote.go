package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
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
		Name:        c.NodeName,
		Role:        c.Role,
		Hostname:    c.Hostname,
		Status:      models.StatusComplete,
		CollectedAt: time.Now().UTC(),
	}

	if err := c.Exec.Connect(ctx); err != nil {
		facts.Status = models.StatusUnreachable
		facts.Error = err.Error()
		return facts, nil
	}
	defer c.Exec.Close()

	partial := false

	// Detect OS
	osOut, err := c.Exec.Run(ctx, "uname -s")
	if err != nil {
		partial = true
	}
	osName := strings.ToLower(strings.TrimSpace(osOut))
	facts.OS = osName

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

	ollamaInfo := c.discoverOllamaRobust(ctx)
	if ollamaInfo.Installed {
		facts.Ollama = &ollamaInfo
		facts.Tools = append(facts.Tools, models.ToolInfo{
			Name:    "ollama",
			Path:    ollamaInfo.Path,
			Version: ollamaInfo.Version,
			Class:   models.ToolClassAICLI,
		})
	}
	facts.TurboQuant = detectTurboQuantSupport(ctx, facts.OS, facts.Arch, facts.Tools, facts.Resources, facts.Ollama, func(ctx context.Context, cmd string) (string, error) {
		return c.Exec.Run(ctx, cmd)
	})

	if partial {
		facts.Status = models.StatusPartial
	}
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

	if source, level, stall10, ok := c.remotePressureSignal(ctx, osName); ok {
		r.Pressure = mergePressureLevels(r.Pressure, level)
		r.PressureSource = source
		r.PressureStall10 = stall10
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
				r.GPUs = parseRemoteNvidiaSMI(out)
			} else if strings.Contains(out, "Chipset Model") {
				r.GPUs = parseRemoteSystemProfiler(out)
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
	r.ThermalState = c.remoteThermalState(ctx, osName)

	return r, partial
}

func (c *RemoteCollector) remotePressureSignal(ctx context.Context, osName string) (string, string, float64, bool) {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "linux":
		out, err := c.Exec.Run(ctx, "cat /proc/pressure/memory 2>/dev/null")
		if err != nil || strings.TrimSpace(out) == "" {
			return "", "", 0, false
		}
		stall10, ok := parseLinuxPressureStall10(out)
		if !ok {
			return "", "", 0, false
		}
		return "linux-psi", linuxPressureLevel(stall10), stall10, true
	case "darwin":
		out, err := c.Exec.Run(ctx, "sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null")
		if err != nil || strings.TrimSpace(out) == "" {
			return "", "", 0, false
		}
		level, ok := parseDarwinMemoryPressureLevel(out)
		if !ok {
			return "", "", 0, false
		}
		return "darwin-vm-pressure", darwinPressureLevel(level), 0, true
	default:
		return "", "", 0, false
	}
}

func (c *RemoteCollector) remoteAddresses(ctx context.Context) []models.NetworkAddress {
	var addrs []models.NetworkAddress
	// Try `ip -o addr` first (outputs: "2: eth0 inet 192.168.1.5/24 ..."), fallback to basic ip/ifconfig
	cmd := `if command -v ip >/dev/null 2>&1; then ip -o addr show scope global 2>/dev/null || ip addr show scope global | awk '/inet/ {print $2}' | cut -d/ -f1; else ifconfig 2>/dev/null | awk '/^[a-z]/ {iface=$1} /inet / && !/127.0.0.1/ {print iface, $2}; /inet6 / && !/::1/ && !/fe80/ {print iface, $2}' | sed 's/://'; fi`

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

	var ipStr, ifName string
	for i, f := range fields {
		if f == "inet" || f == "inet6" {
			if i+1 < len(fields) {
				ipStr = strings.Split(fields[i+1], "/")[0]
			}
			if i >= 2 {
				ifName = strings.TrimSuffix(fields[1], ":")
			}
			break
		}
	}

	// Fallback: might be "ifname 1.2.3.4" or just "1.2.3.4"
	if ipStr == "" {
		switch len(fields) {
		case 1:
			ipStr = fields[0]
		case 2:
			ifName = fields[0]
			ipStr = fields[1]
		}
	}

	ip := net.ParseIP(ipStr)
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
func (c *RemoteCollector) discoverOllamaRobust(ctx context.Context) models.OllamaInfo {
	info := models.OllamaInfo{Installed: false}

	out, err := c.Exec.Run(ctx, OllamaDiscoveryScript)
	if err != nil {
		info.Error = err.Error()
		return info
	}

	// parse the JSON blob
	var parsed models.OllamaInfo
	if json.Unmarshal([]byte(out), &parsed) == nil {
		return parsed
	}
	return info
}

func parseRemoteNvidiaSMI(out string) []models.GPUInfo {
	var gpus []models.GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, ", ", 2)
		name := strings.TrimSpace(parts[0])
		gpu := models.GPUInfo{
			Model:        name,
			Vendor:       "nvidia",
			Capabilities: []string{"cuda"},
		}
		if len(parts) >= 2 {
			if vram, err := strconv.Atoi(strings.TrimSpace(parts[1])); err == nil {
				gpu.VRAMMB = vram
			}
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

func parseRemoteSystemProfiler(out string) []models.GPUInfo {
	var gpus []models.GPUInfo
	var current *models.GPUInfo

	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Chipset Model:") {
			if current != nil {
				gpus = append(gpus, *current)
			}
			model := strings.TrimSpace(strings.TrimPrefix(trimmed, "Chipset Model:"))
			current = &models.GPUInfo{
				Model:  model,
				Vendor: models.GPUFromString(model).Vendor,
			}
		}
		if current != nil {
			if strings.HasPrefix(trimmed, "Metal Family:") || strings.HasPrefix(trimmed, "Metal Support:") {
				if !current.HasCapability("metal") {
					current.Capabilities = append(current.Capabilities, "metal")
				}
			}
		}
	}
	if current != nil {
		if current.Vendor == "apple" && !current.HasCapability("metal") {
			current.Capabilities = append(current.Capabilities, "metal")
		}
		gpus = append(gpus, *current)
	}
	return gpus
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
		out, err := c.Exec.Run(ctx, `findmnt -n -o SOURCE / 2>/dev/null | sed 's/[0-9]*$//' | sed 's|/dev/||'`)
		if err != nil {
			return "unknown"
		}
		dev := strings.TrimSpace(out)
		if strings.HasPrefix(dev, "nvme") {
			return "nvme"
		}
		base := strings.TrimRight(dev, "0123456789")
		if base == "" {
			return "unknown"
		}
		rotOut, err := c.Exec.Run(ctx, fmt.Sprintf("cat /sys/block/%s/queue/rotational 2>/dev/null", base))
		if err != nil {
			return "unknown"
		}
		switch strings.TrimSpace(rotOut) {
		case "0":
			return "ssd"
		case "1":
			return "hdd"
		}
		return "unknown"
	default:
		return "unknown"
	}
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
