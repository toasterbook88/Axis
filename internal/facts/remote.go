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
	// Fallback script that tries `ip` first, then `ifconfig`
	cmd := `if command -v ip >/dev/null 2>&1; then ip addr show scope global | awk '/inet/ {print $2}' | cut -d/ -f1; else ifconfig | awk '/inet / && !/127.0.0.1/ {print $2}; /inet6 / && !/::1/ && !/fe80/ {print $2}' | cut -d% -f1 | cut -d/ -f1; fi`

	out, err := c.Exec.Run(ctx, cmd)
	if err != nil {
		return addrs
	}

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		ipStr := strings.TrimSpace(line)
		if ipStr == "" {
			continue
		}
		ip := net.ParseIP(ipStr)
		if ip == nil {
			continue
		}

		kind := "ipv4"
		if ip.To4() == nil {
			kind = "ipv6"
		}
		addrs = append(addrs, models.NetworkAddress{
			Kind:    kind,
			Address: ip.String(),
		})
	}
	return addrs
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
