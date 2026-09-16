package facts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func localResources(ctx context.Context) (*models.Resources, bool) {
	r := &models.Resources{Pressure: "none"}
	partial := false

	// CPU
	if cores, model, err := localCPU(ctx); err != nil {
		partial = true
	} else {
		r.CPUCores = cores
		r.CPUModel = model
	}
	r.MemoryTopology, r.MemoryClass = detectMemoryTopology(runtime.GOOS, runtime.GOARCH, r.CPUModel)

	// RAM
	if total, free, err := localRAM(ctx); err != nil {
		partial = true
	} else {
		r.RAMTotalMB = total
		r.RAMFreeMB = free
		r.Pressure = computePressure(total, free)
		r.PressureSource = "free-ram"
	}

	if source, level, stall10, someAvg10, fullAvg10, ok := localPressureSignal(ctx); ok {
		r.Pressure = mergePressureLevels(r.Pressure, level)
		r.PressureSource = source
		r.PressureStall10 = stall10
		r.MemoryPSISomeAvg10 = someAvg10
		r.MemoryPSIFullAvg10 = fullAvg10
	}

	if load1, load5, load15, err := localLoadAverages(ctx); err != nil {
		partial = true
	} else {
		r.Load1M = load1
		r.Load5M = load5
		r.Load15M = load15
	}

	// Disk
	if total, free, err := localDisk(ctx); err != nil {
		partial = true
	} else {
		r.DiskTotalGB = total
		r.DiskFreeGB = free
	}

	// Secondary Disk (best-effort)
	if totalExt, freeExt, err := localDiskExt(ctx); err == nil {
		r.DiskTotalGB_Ext = totalExt
		r.DiskFreeGB_Ext = freeExt
	}

	if vols := localVolumes(ctx); len(vols) > 0 {
		r.Volumes = vols
	}

	// GPU (best-effort, never causes partial)
	r.GPUs = localGPUs(ctx)
	if util, ok := localGPUUtilPercent(ctx); ok {
		r.GPUUtilPercent = &util
	}

	// Storage class (best-effort)
	r.StorageClass = localStorageClass(ctx)

	// Thermal and power (best-effort)
	if pct, ok := localBatteryPercent(ctx); ok {
		r.BatteryPercent = &pct
	}
	r.PowerSource = localPowerSource(ctx)
	r.ThermalState = localThermalState(ctx)
	r.ThermalZones = localThermalZones(ctx)

	return r, partial
}

func localPressureSignal(ctx context.Context) (source string, level string, stall10 float64, someAvg float64, fullAvg float64, ok bool) {
	switch runtime.GOOS {
	case "linux":
		data, err := os.ReadFile("/proc/pressure/memory")
		if err != nil {
			return "", "", 0, 0, 0, false
		}
		stall10, ok := parseLinuxPressureStall10(string(data))
		if !ok {
			return "", "", 0, 0, 0, false
		}
		someAvg, fullAvg, _ := parseLinuxPSI(string(data))
		return "linux-psi", linuxPressureLevel(stall10), stall10, someAvg, fullAvg, true
	case "darwin":
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "kern.memorystatus_vm_pressure_level").Output()
		if err != nil {
			return "", "", 0, 0, 0, false
		}
		level, ok := parseDarwinMemoryPressureLevel(string(out))
		if !ok {
			return "", "", 0, 0, 0, false
		}
		someAvg, fullAvg := MapDarwinPressureToPSI(level)
		return "darwin-vm-pressure", darwinPressureLevel(level), 0, someAvg, fullAvg, true
	default:
		return "", "", 0, 0, 0, false
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

func localCPU(ctx context.Context) (int, string, error) {
	if runtime.GOOS == "darwin" {
		cOut, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.ncpu").Output()
		if err == nil {
			cores, _ := strconv.Atoi(strings.TrimSpace(string(cOut)))
			mOut, _ := exec.CommandContext(ctx, "sysctl", "-n", "machdep.cpu.brand_string").Output()
			model := strings.TrimSpace(string(mOut))
			if model == "" {
				// Apple Silicon doesn't have machdep.cpu.brand_string
				mOut, _ = exec.CommandContext(ctx, "sysctl", "-n", "hw.model").Output()
				model = strings.TrimSpace(string(mOut))
			}
			return cores, model, nil
		}

		if out, err := exec.CommandContext(ctx, "system_profiler", "SPHardwareDataType").Output(); err == nil {
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

		if out, err := exec.CommandContext(ctx, "hostinfo").Output(); err == nil {
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
	cOut, err := exec.CommandContext(ctx, "nproc").Output()
	if err != nil {
		return 0, "", err
	}
	cores, _ := strconv.Atoi(strings.TrimSpace(string(cOut)))
	mOut, _ := exec.CommandContext(ctx, "bash", "-c", `grep -m1 'model name' /proc/cpuinfo | cut -d: -f2`).Output()
	return cores, strings.TrimSpace(string(mOut)), nil
}

func localRAM(ctx context.Context) (int64, int64, error) {
	if runtime.GOOS == "darwin" {
		totalMB := darwinTotalRAMMB(ctx)

		vmOut, err := exec.CommandContext(ctx, "vm_stat").Output()
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

func localLoadAverages(ctx context.Context) (float64, float64, float64, error) {
	if runtime.GOOS == "darwin" {
		out, err := exec.CommandContext(ctx, "sysctl", "-n", "vm.loadavg").Output()
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

func darwinTotalRAMMB(ctx context.Context) int64 {
	if out, err := exec.CommandContext(ctx, "sysctl", "-n", "hw.memsize").Output(); err == nil {
		totalBytes, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
		if totalBytes > 0 {
			return totalBytes / (1024 * 1024)
		}
	}

	if out, err := exec.CommandContext(ctx, "hostinfo").Output(); err == nil {
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

	if out, err := exec.CommandContext(ctx, "system_profiler", "SPHardwareDataType").Output(); err == nil {
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

func localDisk(ctx context.Context) (int64, int64, error) {
	out, err := exec.CommandContext(ctx, "df", "-kP", "/").Output()
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

func localDiskExt(ctx context.Context) (int64, int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "df", "-kP").Output()
	if err != nil {
		return 0, 0, err
	}
	return parseDFOutputExt(string(out))
}

func parseDFOutputExt(out string) (int64, int64, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected df output")
	}

	var totalExt, freeExt int64
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		mount := strings.Join(fields[5:], " ")
		if mount == "/mnt" || strings.HasPrefix(mount, "/mnt/") || mount == "/media" || strings.HasPrefix(mount, "/media/") || mount == "/Volumes" || strings.HasPrefix(mount, "/Volumes/") {
			totalKB, err := strconv.ParseInt(fields[1], 10, 64)
			if err != nil {
				continue
			}
			freeKB, err := strconv.ParseInt(fields[3], 10, 64)
			if err != nil {
				continue
			}
			totalExt += totalKB
			freeExt += freeKB
		}
	}
	return totalExt / (1024 * 1024), freeExt / (1024 * 1024), nil
}
