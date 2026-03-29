package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

// LocalCollector collects facts from the local machine.
type LocalCollector struct {
	Name string
	Role string
}

// NewLocalCollector creates a collector for the local node.
func NewLocalCollector(name, role string) *LocalCollector {
	return &LocalCollector{Name: name, Role: role}
}

// Collect gathers all facts from the local machine.
// Tolerates missing values — degrades to partial, never crashes.
func (c *LocalCollector) Collect(ctx context.Context) (*models.NodeFacts, error) {
	facts := &models.NodeFacts{
		Name:        c.Name,
		Role:        c.Role,
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Status:      models.StatusComplete,
		CollectedAt: time.Now().UTC(),
	}

	hostname, _ := os.Hostname()
	facts.Hostname = hostname

	// OS version
	if v, err := localOSVersion(); err != nil {
		facts.Status = models.StatusPartial
	} else {
		facts.OSVersion = v
	}

	// Resources
	res, partial := localResources()
	facts.Resources = res
	if partial {
		facts.Status = models.StatusPartial
	}

	// Network addresses
	facts.Addresses = localAddresses()

	// Tools
	facts.Tools = DiscoverTools(ctx)

	ollamaInfo := discoverOllamaLocal(ctx)
	if ollamaInfo.Installed {
		facts.Ollama = &ollamaInfo
		facts.Tools = append(facts.Tools, models.ToolInfo{
			Name:    "ollama",
			Path:    ollamaInfo.Path,
			Version: ollamaInfo.Version,
			Class:   models.ToolClassAICLI,
		})
	}
	facts.TurboQuant = detectTurboQuantSupport(ctx, facts.OS, facts.Arch, facts.Tools, facts.Resources, facts.Ollama, runLocalTurboQuantProbe)

	return facts, nil
}

func runLocalTurboQuantProbe(ctx context.Context, cmd string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-lc", cmd).CombinedOutput()
	return string(out), err
}

func localOSVersion() (string, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sw_vers", "-productVersion").Output()
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(out)), nil
	}
	out, err := exec.Command("uname", "-r").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func localResources() (*models.Resources, bool) {
	r := &models.Resources{Pressure: "none"}
	partial := false

	// CPU
	if cores, model, err := localCPU(); err != nil {
		partial = true
	} else {
		r.CPUCores = cores
		r.CPUModel = model
	}
	r.MemoryTopology, r.MemoryClass = detectMemoryTopology(runtime.GOOS, runtime.GOARCH, r.CPUModel)

	// RAM
	if total, free, err := localRAM(); err != nil {
		partial = true
	} else {
		r.RAMTotalMB = total
		r.RAMFreeMB = free
		r.Pressure = computePressure(total, free)
		r.PressureSource = "free-ram"
	}

	if source, level, stall10, ok := localPressureSignal(); ok {
		r.Pressure = mergePressureLevels(r.Pressure, level)
		r.PressureSource = source
		r.PressureStall10 = stall10
	}

	if load1, load5, load15, err := localLoadAverages(); err != nil {
		partial = true
	} else {
		r.Load1M = load1
		r.Load5M = load5
		r.Load15M = load15
	}

	// Disk
	if total, free, err := localDisk(); err != nil {
		partial = true
	} else {
		r.DiskTotalGB = total
		r.DiskFreeGB = free
	}

	// GPU (best-effort, never causes partial)
	r.GPUs = localGPUs()
	if util, ok := localGPUUtilPercent(); ok {
		r.GPUUtilPercent = util
	}

	return r, partial
}

func localPressureSignal() (string, string, float64, bool) {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/pressure/memory")
		if err != nil {
			return "", "", 0, false
		}
		stall10, ok := parseLinuxPressureStall10(string(data))
		if !ok {
			return "", "", 0, false
		}
		return "linux-psi", linuxPressureLevel(stall10), stall10, true
	case "darwin":
		out, err := exec.Command("sysctl", "-n", "kern.memorystatus_vm_pressure_level").Output()
		if err != nil {
			return "", "", 0, false
		}
		level, ok := parseDarwinMemoryPressureLevel(string(out))
		if !ok {
			return "", "", 0, false
		}
		return "darwin-vm-pressure", darwinPressureLevel(level), 0, true
	default:
		return "", "", 0, false
	}
}

func computePressure(totalMB, freeMB int64) string {
	if totalMB <= 0 {
		return "none"
	}
	pct := float64(freeMB) / float64(totalMB)
	switch {
	case pct < 0.05:
		return "high"
	case pct < 0.10:
		return "medium"
	case pct < 0.20:
		return "low"
	default:
		return "none"
	}
}

func localCPU() (int, string, error) {
	if runtime.GOOS == "darwin" {
		cOut, err := exec.Command("sysctl", "-n", "hw.ncpu").Output()
		if err == nil {
			cores, _ := strconv.Atoi(strings.TrimSpace(string(cOut)))
			mOut, _ := exec.Command("sysctl", "-n", "machdep.cpu.brand_string").Output()
			model := strings.TrimSpace(string(mOut))
			if model == "" {
				// Apple Silicon doesn't have machdep.cpu.brand_string
				mOut, _ = exec.Command("sysctl", "-n", "hw.model").Output()
				model = strings.TrimSpace(string(mOut))
			}
			return cores, model, nil
		}

		if out, err := exec.Command("system_profiler", "SPHardwareDataType").Output(); err == nil {
			var cores int
			var model string
			for _, line := range strings.Split(string(out), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.HasPrefix(trimmed, "Chip:") {
					model = strings.TrimSpace(strings.TrimPrefix(trimmed, "Chip:"))
				} else if strings.HasPrefix(trimmed, "Total Number of Cores:") {
					fields := strings.Fields(strings.TrimPrefix(trimmed, "Total Number of Cores:"))
					if len(fields) > 0 {
						cores, _ = strconv.Atoi(fields[0])
					}
				}
			}
			if cores > 0 || model != "" {
				return cores, model, nil
			}
		}

		if out, err := exec.Command("hostinfo").Output(); err == nil {
			for _, line := range strings.Split(string(out), "\n") {
				trimmed := strings.TrimSpace(line)
				if strings.Contains(trimmed, "processors are logically available.") {
					fields := strings.Fields(trimmed)
					if len(fields) > 0 {
						cores, _ := strconv.Atoi(fields[0])
						if cores > 0 {
							return cores, "Apple Silicon", nil
						}
					}
				}
			}
		}

		return 0, "", err
	}
	// Linux
	cOut, err := exec.Command("nproc").Output()
	if err != nil {
		return 0, "", err
	}
	cores, _ := strconv.Atoi(strings.TrimSpace(string(cOut)))
	mOut, _ := exec.Command("bash", "-c", `grep -m1 'model name' /proc/cpuinfo | cut -d: -f2`).Output()
	return cores, strings.TrimSpace(string(mOut)), nil
}

func localRAM() (int64, int64, error) {
	if runtime.GOOS == "darwin" {
		totalMB := darwinTotalRAMMB()

		vmOut, err := exec.Command("vm_stat").Output()
		freeMB := int64(0)
		if err == nil {
			freeMB = parseDarwinFreeRAM(string(vmOut))
		}
		if totalMB > 0 && freeMB == 0 {
			freeMB = totalMB / 4
		}
		if totalMB > 0 {
			return totalMB, freeMB, nil
		}
		if err != nil {
			return 0, 0, err
		}
		return 0, freeMB, fmt.Errorf("could not determine total RAM")
	}
	// Linux
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return parseLinuxMeminfo(string(data))
}

func parseDarwinFreeRAM(vmstat string) int64 {
	pageSize := int64(16384) // fallback for arm64

	var free, inactive int64
	remaining := vmstat
	for len(remaining) > 0 {
		var line string
		if idx := strings.IndexByte(remaining, '\n'); idx == -1 {
			line = remaining
			remaining = ""
		} else {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		}
		// e.g. "Mach Virtual Memory Statistics: (page size of 16384 bytes)"
		if strings.HasPrefix(line, "Mach Virtual Memory Statistics:") {
			if idx := strings.Index(line, "page size of "); idx != -1 {
				parts := strings.Fields(line[idx+13:])
				if len(parts) > 0 {
					if size, err := strconv.ParseInt(parts[0], 10, 64); err == nil {
						pageSize = size
					}
				}
			}
		} else if strings.HasPrefix(line, "Pages free:") {
			free = parseVMStatVal(line)
		} else if strings.HasPrefix(line, "Pages inactive:") {
			inactive = parseVMStatVal(line)
		}
	}
	return (free + inactive) * pageSize / (1024 * 1024)
}

func parseVMStatVal(line string) int64 {
	parts := strings.SplitN(line, ":", 2)
	if len(parts) < 2 {
		return 0
	}
	s := strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(parts[1]), "."))
	v, _ := strconv.ParseInt(s, 10, 64)
	return v
}

func parseLinuxMeminfo(data string) (int64, int64, error) {
	var total, available, free int64
	remaining := data
	for len(remaining) > 0 {
		var line string
		if idx := strings.IndexByte(remaining, '\n'); idx == -1 {
			line = remaining
			remaining = ""
		} else {
			line = remaining[:idx]
			remaining = remaining[idx+1:]
		}
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseKBField(line)
		} else if strings.HasPrefix(line, "MemFree:") {
			free = parseKBField(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			available = parseKBField(line)
		}
	}
	if total <= 0 {
		return 0, 0, fmt.Errorf("meminfo missing MemTotal")
	}
	if available <= 0 {
		if free > 0 {
			available = free
		} else {
			return 0, 0, fmt.Errorf("meminfo missing MemAvailable")
		}
	}
	return total / 1024, available / 1024, nil
}

func localLoadAverages() (float64, float64, float64, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("sysctl", "-n", "vm.loadavg").Output()
		if err != nil {
			return 0, 0, 0, err
		}
		return parseDarwinLoadavg(string(out))
	}

	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return 0, 0, 0, err
	}
	return parseLoadavgFields(string(data))
}

func parseDarwinLoadavg(data string) (float64, float64, float64, error) {
	clean := strings.NewReplacer("{", "", "}", "").Replace(strings.TrimSpace(data))
	return parseLoadavgFields(clean)
}

func parseLoadavgFields(data string) (float64, float64, float64, error) {
	fields := strings.Fields(strings.TrimSpace(data))
	if len(fields) < 3 {
		return 0, 0, 0, fmt.Errorf("unexpected loadavg output")
	}

	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid load1: %w", err)
	}
	load5, err := strconv.ParseFloat(fields[1], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid load5: %w", err)
	}
	load15, err := strconv.ParseFloat(fields[2], 64)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("invalid load15: %w", err)
	}
	return load1, load5, load15, nil
}

func darwinTotalRAMMB() int64 {
	if out, err := exec.Command("sysctl", "-n", "hw.memsize").Output(); err == nil {
		totalBytes, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if totalBytes > 0 {
			return totalBytes / (1024 * 1024)
		}
	}

	if out, err := exec.Command("hostinfo").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Primary memory available:") {
				fields := strings.Fields(trimmed)
				if len(fields) >= 5 {
					value, _ := strconv.ParseFloat(fields[3], 64)
					unit := strings.ToLower(fields[4])
					switch {
					case strings.HasPrefix(unit, "gigabyte"):
						return int64(value * 1024)
					case strings.HasPrefix(unit, "megabyte"):
						return int64(value)
					}
				}
			}
		}
	}

	if out, err := exec.Command("system_profiler", "SPHardwareDataType").Output(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Memory:") {
				fields := strings.Fields(strings.TrimPrefix(trimmed, "Memory:"))
				if len(fields) >= 2 {
					value, _ := strconv.ParseFloat(fields[0], 64)
					unit := strings.ToLower(fields[1])
					switch unit {
					case "gb":
						return int64(value * 1024)
					case "mb":
						return int64(value)
					}
				}
			}
		}
	}

	return 0
}

func parseKBField(line string) int64 {
	fields := strings.Fields(line)
	if len(fields) < 2 {
		return 0
	}
	v, _ := strconv.ParseInt(fields[1], 10, 64)
	return v
}

func localDisk() (int64, int64, error) {
	out, err := exec.Command("df", "-kP", "/").Output()
	if err != nil {
		return 0, 0, err
	}
	return parseDFOutput(string(out))
}

func parseDFOutput(out string) (int64, int64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected df output")
	}

	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}

		totalKB, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid df total: %w", err)
		}
		freeKB, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			return 0, 0, fmt.Errorf("invalid df free: %w", err)
		}
		return totalKB / (1024 * 1024), freeKB / (1024 * 1024), nil
	}

	return 0, 0, fmt.Errorf("unexpected df fields")
}

func localGPUs() []string {
	if runtime.GOOS == "darwin" {
		out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
		if err != nil {
			return nil
		}
		var gpus []string
		for _, line := range strings.Split(string(out), "\n") {
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "Chipset Model:") {
				gpu := strings.TrimSpace(strings.TrimPrefix(trimmed, "Chipset Model:"))
				if gpu != "" {
					gpus = append(gpus, gpu)
				}
			}
		}
		return gpus
	}
	// Linux
	out, err := exec.Command("bash", "-c", `lspci 2>/dev/null | grep -iE 'vga|3d' | sed 's/.*: //'`).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var gpus []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			gpus = append(gpus, strings.TrimSpace(line))
		}
	}
	return gpus
}

func localAddresses() []models.NetworkAddress {
	var addrs []models.NetworkAddress

	ifaces, err := net.Interfaces()
	if err != nil {
		return addrs
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagLoopback != 0 || iface.Flags&net.FlagUp == 0 {
			continue
		}
		ifAddrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, a := range ifAddrs {
			ipNet, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipNet.IP
			if ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
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
	}
	return addrs
}

func discoverOllamaLocal(ctx context.Context) models.OllamaInfo {
	info := models.OllamaInfo{Installed: false}

	out, err := exec.CommandContext(ctx, "bash", "-c", OllamaDiscoveryScript).Output()
	if err != nil {
		info.Error = err.Error()
		return info
	}

	// parse the JSON blob
	var parsed models.OllamaInfo
	if json.Unmarshal(out, &parsed) == nil {
		return parsed
	}
	return info
}

func localGPUUtilPercent() (float64, bool) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("ioreg", "-r", "-c", "AGXAccelerator").Output()
		if err != nil {
			return 0, false
		}
		for _, line := range strings.Split(string(out), "\n") {
			if strings.Contains(line, "Device Utilization%") {
				parts := strings.SplitN(line, "=", 2)
				if len(parts) == 2 {
					v, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
					if err == nil {
						return v, true
					}
				}
			}
		}
		return 0, false
	case "linux":
		out, err := exec.Command("nvidia-smi", "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits").Output()
		if err != nil || len(out) == 0 {
			return 0, false
		}
		lines := strings.SplitN(strings.TrimSpace(string(out)), "\n", 2)
		v, err := strconv.ParseFloat(strings.TrimSpace(lines[0]), 64)
		if err != nil {
			return 0, false
		}
		return v, true
	default:
		return 0, false
	}
}
