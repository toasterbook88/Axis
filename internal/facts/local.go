package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
)

// LocalCollector collects facts from the local machine.
type LocalCollector struct {
	Name string
	Role string
}

var runAppleFoundationModelsProbeFn = runAppleFoundationModelsProbe
var buildAppleFoundationModelsHelperFn = buildAppleFoundationModelsHelper
var appleFoundationModelsProbeCommandFn = exec.CommandContext
var appleFoundationModelsBuildCommandFn = exec.CommandContext
var appleFoundationModelsHomeDirFn = os.UserHomeDir
var appleFoundationModelsReadFileFn = os.ReadFile
var appleFoundationModelsWriteFileFn = os.WriteFile

var runOllamaDiscoveryFn = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "bash", "-c", OllamaDiscoveryScript).Output()
}
var runLlamaServerDiscoveryFn = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "bash", "-c", LlamaServerDiscoveryScript).Output()
}
var runMLXDiscoveryFn = func(ctx context.Context) ([]byte, error) {
	return exec.CommandContext(ctx, "bash", "-c", MLXDiscoveryScript).Output()
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
	facts.Identity = detectLocalNodeIdentity(ctx, facts.OS)

	cfgPath := os.Getenv("AXIS_CONFIG")
	if cfgPath == "" {
		home, _ := os.UserHomeDir()
		cfgPath = filepath.Join(home, ".axis", "nodes.yaml")
	}
	if _, err := os.Stat(cfgPath); err == nil {
		if cfg, err := config.Load(cfgPath); err == nil {
			if nc, ok := cfg.FindNode(c.Name); ok {
				facts.SystemReserveMB = nc.SystemReserveMB
			} else if nc, ok := cfg.FindNode(facts.Hostname); ok {
				facts.SystemReserveMB = nc.SystemReserveMB
			}
		}
	}
	if facts.SystemReserveMB <= 0 {
		facts.SystemReserveMB = 1024
	}

	// OS version
	if v, err := localOSVersion(); err != nil {
		facts.Status = models.StatusPartial
	} else {
		facts.OSVersion = v
	}

	// Resources
	res, partial := localResources()
	facts.Resources = res
	facts.PopulateMemoryMetrics()
	if partial {
		facts.Status = models.StatusPartial
	}

	// Network addresses
	facts.Addresses = localAddresses()

	// Tools
	facts.Tools = DiscoverTools(ctx)

	ollamaInfo, residentModels := discoverOllamaLocal(ctx)
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
	if llamaResidents := discoverLlamaServerLocal(ctx); len(llamaResidents) > 0 {
		facts.ResidentModels = append(facts.ResidentModels, llamaResidents...)
	}
	// Merge MLX resident models (runtime="mlx") from the mlx_lm.server API.
	if mlxResidents := discoverMLXLocal(ctx); len(mlxResidents) > 0 {
		facts.ResidentModels = append(facts.ResidentModels, mlxResidents...)
	}
	facts.TurboQuant = detectTurboQuantSupport(ctx, facts.OS, facts.Arch, facts.Tools, facts.Resources, facts.Ollama, runLocalTurboQuantProbe)
	if fm := detectAppleFoundationModels(ctx, facts.OS, facts.Arch, facts.OSVersion, facts.Tools); fm != nil {
		facts.AppleFM = fm
		if fm.Available && fm.Verified {
			toolPath := "swift"
			if swiftTool, ok := findToolInfo(facts.Tools, "swift"); ok && swiftTool.Path != "" {
				toolPath = swiftTool.Path
			}
			facts.Tools = append(facts.Tools, models.ToolInfo{
				Name:    "apple-foundation-models",
				Path:    toolPath,
				Version: fm.Version,
				Class:   models.ToolClassRuntime,
			})
		}
	}

	return facts, nil
}

func runLocalTurboQuantProbe(ctx context.Context, cmd string) (string, error) {
	out, err := exec.CommandContext(ctx, "bash", "-lc", cmd).CombinedOutput()
	return string(out), err
}

func detectAppleFoundationModels(ctx context.Context, osName, arch, osVersion string, tools []models.ToolInfo) *models.AppleFoundationModelsInfo {
	if !strings.EqualFold(osName, "darwin") || !strings.Contains(strings.ToLower(arch), "arm64") {
		return nil
	}
	if !supportsAppleFoundationModelsOS(osVersion) {
		return &models.AppleFoundationModelsInfo{
			Version: osVersion,
			Error:   "requires macOS 26 or later on Apple silicon (Apple platform versioning)",
		}
	}
	if _, ok := findToolInfo(tools, "swift"); !ok {
		return &models.AppleFoundationModelsInfo{
			Version: osVersion,
			Error:   "swift toolchain not detected",
		}
	}

	probeCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()

	out, err := runAppleFoundationModelsProbeFn(probeCtx)
	trimmedOut := strings.TrimSpace(out)
	info := &models.AppleFoundationModelsInfo{
		Version:   osVersion,
		Available: err == nil,
		Verified:  err == nil && trimmedOut != "",
	}
	if err != nil {
		info.Error = trimmedOut
		if info.Error == "" {
			info.Error = err.Error()
		}
	} else if trimmedOut == "" {
		info.Error = "apple foundation models probe returned empty output"
	}
	return info
}

func supportsAppleFoundationModelsOS(osVersion string) bool {
	// Current Apple platform releases report macOS 26.x via sw_vers -productVersion.
	fields := strings.SplitN(strings.TrimSpace(osVersion), ".", 2)
	if len(fields) == 0 || fields[0] == "" {
		return false
	}
	major, err := strconv.Atoi(fields[0])
	if err != nil {
		return false
	}
	return major >= 26
}

func runAppleFoundationModelsProbe(ctx context.Context) (string, error) {
	type buildResult struct {
		path string
		err  error
	}
	ch := make(chan buildResult, 1)
	go func() {
		// Compilation is a one-time, potentially slow operation (first-time
		// xcrun swiftc can take tens of seconds). Give it its own generous
		// timeout so the cache is populated correctly on first run, without
		// blocking the caller context which has a short probe deadline.
		buildCtx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
		defer cancel()
		path, err := buildAppleFoundationModelsHelperFn(buildCtx)
		ch <- buildResult{path, err}
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case result := <-ch:
		if result.err != nil {
			return "", result.err
		}
		out, err := appleFoundationModelsProbeCommandFn(ctx, result.path, "--self-test").CombinedOutput()
		return string(out), err
	}
}

func buildAppleFoundationModelsHelper(ctx context.Context) (string, error) {
	homeDir, err := appleFoundationModelsHomeDirFn()
	if err != nil {
		return "", fmt.Errorf("resolve home directory for apple foundation models helper: %w", err)
	}

	cacheDir := filepath.Join(homeDir, ".axis", "cache")
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return "", fmt.Errorf("create apple foundation models cache directory: %w", err)
	}

	helperSource := filepath.Join(cacheDir, "apple-foundation-models.swift")
	if err := ensureAppleFoundationModelsHelperSource(helperSource); err != nil {
		return "", err
	}

	helperBinary := filepath.Join(cacheDir, "apple-foundation-models-helper")
	upToDate, err := appleFoundationModelsHelperUpToDate(helperSource, helperBinary)
	if err != nil {
		return "", err
	}
	if upToDate {
		return helperBinary, nil
	}

	tmpFile, err := os.CreateTemp(cacheDir, "apple-foundation-models-helper-*.tmp")
	if err != nil {
		return "", fmt.Errorf("create temporary file for apple foundation models helper: %w", err)
	}
	tmpBinary := tmpFile.Name()
	if err := tmpFile.Close(); err != nil {
		_ = os.Remove(tmpBinary)
		return "", fmt.Errorf("close temporary helper file: %w", err)
	}
	defer os.Remove(tmpBinary)

	out, err := appleFoundationModelsBuildCommandFn(
		ctx,
		"xcrun",
		"swiftc",
		helperSource,
		"-o",
		tmpBinary,
	).CombinedOutput()
	if err != nil {
		msg := strings.TrimSpace(string(out))
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("build apple foundation models helper: %s", msg)
	}
	if err := os.Rename(tmpBinary, helperBinary); err != nil {
		return "", fmt.Errorf("install apple foundation models helper: %w", err)
	}
	return helperBinary, nil
}

func ensureAppleFoundationModelsHelperSource(helperSource string) error {
	existing, err := appleFoundationModelsReadFileFn(helperSource)
	switch {
	case err == nil && string(existing) == appleFoundationModelsHelperSource:
		return nil
	case err != nil && !os.IsNotExist(err):
		return fmt.Errorf("read apple foundation models helper source: %w", err)
	}

	if err := appleFoundationModelsWriteFileFn(helperSource, []byte(appleFoundationModelsHelperSource), 0o644); err != nil {
		return fmt.Errorf("write apple foundation models helper source: %w", err)
	}
	return nil
}

func appleFoundationModelsHelperUpToDate(helperSource, helperBinary string) (bool, error) {
	sourceInfo, err := os.Stat(helperSource)
	if err != nil {
		return false, fmt.Errorf("stat apple foundation models helper source: %w", err)
	}
	binaryInfo, err := os.Stat(helperBinary)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat apple foundation models helper binary: %w", err)
	}
	if binaryInfo.IsDir() {
		return false, fmt.Errorf("apple foundation models helper binary path is a directory: %s", helperBinary)
	}
	return !binaryInfo.ModTime().Before(sourceInfo.ModTime()), nil
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

	if source, level, stall10, someAvg10, fullAvg10, ok := localPressureSignal(); ok {
		r.Pressure = mergePressureLevels(r.Pressure, level)
		r.PressureSource = source
		r.PressureStall10 = stall10
		r.MemoryPSISomeAvg10 = someAvg10
		r.MemoryPSIFullAvg10 = fullAvg10
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

	// Secondary Disk (best-effort)
	if totalExt, freeExt, err := localDiskExt(); err == nil {
		r.DiskTotalGB_Ext = totalExt
		r.DiskFreeGB_Ext = freeExt
	}

	// GPU (best-effort, never causes partial)
	r.GPUs = localGPUs()
	if util, ok := localGPUUtilPercent(); ok {
		r.GPUUtilPercent = &util
	}

	// Storage class (best-effort)
	r.StorageClass = localStorageClass()

	// Thermal and power (best-effort)
	if pct, ok := localBatteryPercent(); ok {
		r.BatteryPercent = &pct
	}
	r.PowerSource = localPowerSource()
	r.ThermalState = localThermalState()
	r.ThermalZones = localThermalZones()

	return r, partial
}

func localPressureSignal() (source string, level string, stall10 float64, someAvg float64, fullAvg float64, ok bool) {
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
		out, err := exec.Command("sysctl", "-n", "kern.memorystatus_vm_pressure_level").Output()
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

func localDiskExt() (int64, int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
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

func localGPUs() []models.GPUInfo {
	if runtime.GOOS == "darwin" {
		return localGPUsDarwin()
	}
	return localGPUsLinux()
}

func localGPUsDarwin() []models.GPUInfo {
	out, err := exec.Command("system_profiler", "SPDisplaysDataType").Output()
	if err != nil {
		return nil
	}
	return parseSystemProfilerGPUs(string(out))
}

func parseSystemProfilerGPUs(out string) []models.GPUInfo {
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
			if strings.HasPrefix(trimmed, "VRAM") && strings.Contains(trimmed, ":") {
				vramStr := strings.TrimSpace(strings.SplitN(trimmed, ":", 2)[1])
				current.VRAMMB = parseVRAMMB(vramStr)
			}
			if strings.HasPrefix(trimmed, "Metal Family:") || strings.HasPrefix(trimmed, "Metal Support:") {
				if !current.HasCapability("metal") {
					current.Capabilities = append(current.Capabilities, "metal")
				}
			}
		}
	}
	if current != nil {
		// Apple Silicon always supports Metal even if not explicitly listed
		if current.Vendor == "apple" && !current.HasCapability("metal") {
			current.Capabilities = append(current.Capabilities, "metal")
		}
		gpus = append(gpus, *current)
	}
	return gpus
}

// parseVRAMMB extracts MB from strings like "16 GB", "4096 MB", "16384 MB".
func parseVRAMMB(s string) int {
	s = strings.TrimSpace(s)
	parts := strings.Fields(s)
	if len(parts) < 1 {
		return 0
	}
	val, err := strconv.Atoi(parts[0])
	if err != nil {
		return 0
	}
	if len(parts) >= 2 {
		switch strings.ToLower(parts[1]) {
		case "gb":
			return val * 1024
		case "mb":
			return val
		}
	}
	// Assume MB if no unit
	return val
}

func localGPUsLinux() []models.GPUInfo {
	// Try nvidia-smi first for NVIDIA GPUs
	gpus := localGPUsNvidiaSMI()

	// Fallback: lspci for non-NVIDIA or if nvidia-smi unavailable
	lspciGPUs := localGPUsLspci()
	for _, g := range lspciGPUs {
		if g.Vendor == "nvidia" && len(gpus) > 0 {
			continue // nvidia-smi gave better data
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func localGPUsNvidiaSMI() []models.GPUInfo {
	out, err := exec.Command("nvidia-smi", "--query-gpu=name,memory.total", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil
	}
	return parseNvidiaSMIOutput(string(out))
}

func parseNvidiaSMIOutput(out string) []models.GPUInfo {
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

func localGPUsLspci() []models.GPUInfo {
	out, err := exec.Command("bash", "-c", `lspci 2>/dev/null | grep -iE 'vga|3d' | sed 's/.*: //'`).Output()
	if err != nil || len(out) == 0 {
		return nil
	}
	var gpus []models.GPUInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			gpus = append(gpus, models.GPUFromString(line))
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
		lowerName := strings.ToLower(iface.Name)
		if strings.HasPrefix(lowerName, "docker") || strings.HasPrefix(lowerName, "br-") || strings.HasPrefix(lowerName, "veth") || strings.HasPrefix(lowerName, "virbr") {
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
			scope := ""
			if ip.IsLinkLocalMulticast() || ip.IsLinkLocalUnicast() {
				scope = "link-local"
			}

			kind := "ipv4"
			if ip.To4() == nil {
				kind = "ipv6"
			}
			addrs = append(addrs, models.NetworkAddress{
				Kind:       kind,
				Address:    ip.String(),
				Interface:  iface.Name,
				Subnet:     subnetFromIPNet(ipNet),
				SpeedClass: classifyInterfaceSpeed(iface.Name, ip),
				Scope:      scope,
			})
		}
	}
	return addrs
}

func subnetFromIPNet(ipNet *net.IPNet) string {
	if ipNet == nil {
		return ""
	}
	ones, _ := ipNet.Mask.Size()
	return ipNet.IP.Mask(ipNet.Mask).String() + "/" + strconv.Itoa(ones)
}

func parseAddressWithOptionalCIDR(raw string) (net.IP, string) {
	if strings.Contains(raw, "/") {
		ip, ipNet, err := net.ParseCIDR(raw)
		if err == nil {
			return ip, subnetFromIPNet(ipNet)
		}
	}
	return net.ParseIP(raw), ""
}

// readSysfsLinkSpeed reads the negotiated link speed (Mbps) for a Linux network
// interface from /sys/class/net/<iface>/speed. It returns an error when sysfs
// is unavailable (a missing interface, or non-numeric content) so
// classifyInterfaceSpeed can fall back to the name/IP heuristic. It is a
// package-level var so tests can stub it for deterministic classification
// independent of the host's real interfaces — e.g. a CI runner whose eth0
// genuinely reports a 10GbE link via sysfs.
var readSysfsLinkSpeed = func(ifName string) (int, error) {
	data, err := os.ReadFile(fmt.Sprintf("/sys/class/net/%s/speed", ifName))
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

// classifyInterfaceSpeed determines the speed class of a network interface. On
// Linux it first consults the exact negotiated link speed from sysfs (≥10000 →
// "10gbe", ≥1000 → "gigabit"); otherwise it falls back to a name/IP heuristic.
// This enables topology-aware decisions (e.g., preferring Thunderbolt links
// for heavy data transfers).
func classifyInterfaceSpeed(ifName string, ip net.IP) string {
	if runtime.GOOS == "linux" {
		if speed, err := readSysfsLinkSpeed(ifName); err == nil {
			if speed >= 10000 {
				return "10gbe"
			}
			if speed >= 1000 {
				return "gigabit"
			}
		}
	}

	lower := strings.ToLower(ifName)

	// Overlay / VPN tunnels — detect by interface name
	if strings.HasPrefix(lower, "wg") {
		return "wireguard"
	}
	if strings.HasPrefix(lower, "tailscale") || strings.HasPrefix(lower, "ts") {
		return "tailscale"
	}
	if strings.HasPrefix(lower, "utun") || strings.HasPrefix(lower, "tun") {
		// Tailscale on macOS typically uses utun; check by IP range
		if isTailscaleIP(ip) {
			return "tailscale"
		}
		return "vpn"
	}
	if strings.HasPrefix(lower, "zt") {
		return "zerotier"
	}
	if strings.HasPrefix(lower, "nb") || strings.HasPrefix(lower, "netbird") {
		return "netbird"
	}

	// Detect by IP range for non-tunnel interfaces
	if isTailscaleIP(ip) {
		return "tailscale"
	}

	// Thunderbolt bridge / point-to-point
	if strings.Contains(lower, "bridge") || strings.Contains(lower, "thunder") {
		return "thunderbolt"
	}

	// Wi-Fi — common interface names across platforms
	if lower == "wlan0" || lower == "wlp" || strings.HasPrefix(lower, "wlp") {
		return "wifi"
	}
	if runtime.GOOS == "darwin" && (lower == "en0") {
		// On many Macs, en0 is Wi-Fi; can't be 100% certain without IOKit
		return "wifi"
	}

	// Ethernet
	if strings.HasPrefix(lower, "en") || strings.HasPrefix(lower, "eth") || strings.HasPrefix(lower, "enp") {
		return "gigabit"
	}

	return "unknown"
}

// isTailscaleIP returns true if the IP is in the Tailscale CGNAT range (100.64.0.0/10).
func isTailscaleIP(ip net.IP) bool {
	ip4 := ip.To4()
	if ip4 == nil {
		return false
	}
	// 100.64.0.0/10 → first byte 100, second byte 64-127
	return ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127
}

func discoverOllamaLocal(ctx context.Context) (models.OllamaInfo, []models.ResidentModel) {
	info := models.OllamaInfo{Installed: false}

	out, err := runOllamaDiscoveryFn(ctx)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}

	// parse the JSON blob
	var parsed ollamaDiscoveryPayload
	if json.Unmarshal(out, &parsed) == nil {
		ApplyOllamaWarmth(&parsed.OllamaInfo, parsed.ResidentModels)
		return parsed.OllamaInfo, parsed.ResidentModels
	}
	return info, nil
}

// applyOllamaWarmth populates ExpiresAt and WarmthScore for each ResidentModel
// from Ollama's /api/ps payload. The Ollama probe emits an `expires_at` field
// per resident model and a process-level `default_keep_alive` duration
// (Ollama 0.3.10+). Warmth is a continuous score in [0, 1] computed as
// remaining / total, where total falls back to 5m (Ollama's stock default)
// when `default_keep_alive` is absent or unparseable. When `expires_at` is
// missing or already past, WarmthScore is 0 (cold). Both fields are
// advisory metadata only — placement consumes them as a bounded
// tiebreaker in internal/placement/ranker.go modelWarmthRank.
//
// Exported for testability from internal/placement and from
// internal/facts tests.
func ApplyOllamaWarmth(info *models.OllamaInfo, rms []models.ResidentModel) {
	if len(rms) == 0 {
		return
	}
	now := time.Now()
	total := DefaultOllamaKeepAlive(info)
	for i := range rms {
		rm := &rms[i]
		if rm.ExpiresAt.IsZero() {
			continue
		}
		if !rm.ExpiresAt.After(now) {
			rm.WarmthScore = 0
			continue
		}
		remaining := rm.ExpiresAt.Sub(now)
		score := float64(remaining) / float64(total)
		if score < 0 {
			score = 0
		}
		if score > 1 {
			score = 1
		}
		rm.WarmthScore = score
	}
}

// DefaultOllamaKeepAlive resolves the process-level default_keep_alive
// duration from an Ollama /api/ps payload, falling back to 5m (Ollama's
// stock default since 0.3.10) when the field is absent or unparseable.
// Returns a positive duration on success. Exported for testability.
func DefaultOllamaKeepAlive(info *models.OllamaInfo) time.Duration {
	const fallback = 5 * time.Minute
	if info == nil {
		return fallback
	}
	val := strings.TrimSpace(info.DefaultKeepAlive)
	if val == "" {
		return fallback
	}
	// If it's a bare integer (seconds), append "s" so ParseDuration can parse it.
	if _, err := strconv.Atoi(val); err == nil {
		val += "s"
	}
	d, err := time.ParseDuration(val)
	if err != nil || d <= 0 {
		return fallback
	}
	return d
}

// discoverLlamaServerLocal probes for a running llama-server process and
// returns its resident models. Returns nil if llama-server is not installed or
// not running.
func discoverLlamaServerLocal(ctx context.Context) []models.ResidentModel {
	out, err := runLlamaServerDiscoveryFn(ctx)
	if err != nil {
		return nil
	}
	var parsed llamaServerDiscoveryPayload
	if json.Unmarshal(out, &parsed) == nil && parsed.Installed {
		return parsed.ResidentModels
	}
	return nil
}

// discoverMLXLocal probes for a running mlx_lm.server process on the local
// node and queries its /v1/models endpoint to enumerate resident models.
// Returns nil if mlx_lm is not installed or no server is running.
func discoverMLXLocal(ctx context.Context) []models.ResidentModel {
	out, err := runMLXDiscoveryFn(ctx)
	if err != nil {
		return nil
	}
	var parsed mlxDiscoveryPayload
	if json.Unmarshal(out, &parsed) == nil && parsed.Installed {
		return parsed.ResidentModels
	}
	return nil
}

func localGPUUtilPercent() (float64, bool) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("ioreg", "-r", "-c", "AGXAccelerator").Output()
		if err != nil {
			return 0, false
		}
		const marker = "\"Device Utilization %\"="
		for _, line := range strings.Split(string(out), "\n") {
			if idx := strings.Index(line, marker); idx != -1 {
				rest := line[idx+len(marker):]
				end := strings.IndexAny(rest, ",}")
				if end == -1 {
					end = len(rest)
				}
				if v, err := strconv.ParseFloat(strings.TrimSpace(rest[:end]), 64); err == nil {
					return v, true
				}
			}
		}
		return 0, false
	case "linux":
		out, err := exec.Command("nvidia-smi", "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits").Output()
		if err != nil {
			return 0, false
		}
		return parseLinuxGPUUtilPercent(string(out))
	default:
		return 0, false
	}
}

func parseLinuxGPUUtilPercent(out string) (float64, bool) {
	var (
		maxUtil float64
		found   bool
	)

	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if v, err := strconv.ParseFloat(strings.TrimSpace(line), 64); err == nil {
			found = true
			if v > maxUtil {
				maxUtil = v
			}
		}
	}

	return maxUtil, found
}

// --- Storage Class Detection ---

func localStorageClass() string {
	switch runtime.GOOS {
	case "darwin":
		return localStorageClassDarwin()
	case "linux":
		return localStorageClassLinux()
	default:
		return "unknown"
	}
}

func localStorageClassDarwin() string {
	out, err := exec.Command("diskutil", "info", "/").Output()
	if err != nil {
		return "unknown"
	}
	return parseDiskutilStorageClass(string(out))
}

func parseDiskutilStorageClass(out string) string {
	isSolid := false
	isNVMe := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Solid State:") && strings.Contains(strings.ToLower(trimmed), "yes") {
			isSolid = true
		}
		if strings.HasPrefix(trimmed, "Protocol:") && strings.Contains(strings.ToLower(trimmed), "nvme") {
			isNVMe = true
		}
		if strings.HasPrefix(trimmed, "Device / Media Name:") && strings.Contains(strings.ToLower(trimmed), "nvme") {
			isNVMe = true
		}
	}
	if isNVMe {
		return "nvme"
	}
	if isSolid {
		return "ssd"
	}
	return "unknown"
}

func localStorageClassLinux() string {
	out, err := exec.Command("bash", "-c", `findmnt -n -o SOURCE / 2>/dev/null`).Output()
	if err != nil {
		return "unknown"
	}
	return resolveLinuxStorageClass(
		strings.TrimSpace(string(out)),
		localLinuxBlockDeviceInfo,
		localLinuxBlockDeviceSlaves,
		localLinuxRotational,
	)
}

type linuxBlockDeviceInfo struct {
	Name   string `json:"name"`
	KName  string `json:"kname"`
	PKName string `json:"pkname"`
	Type   string `json:"type"`
	ROTA   *int   `json:"rota"`
}

type linuxBlockDevicesResponse struct {
	Blockdevices []linuxBlockDeviceInfo `json:"blockdevices"`
}

func resolveLinuxStorageClass(
	source string,
	query func(string) (linuxBlockDeviceInfo, error),
	slaves func(linuxBlockDeviceInfo) ([]string, error),
	fallbackReadRotational func(string) (string, error),
) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "unknown"
	}

	classes := linuxStorageAncestorClasses(source, query, slaves)
	if class := aggregateLinuxStorageClass(classes); class != "unknown" {
		return class
	}

	return fallbackLinuxStorageClass(source, fallbackReadRotational)
}

func linuxStorageAncestorClasses(
	source string,
	query func(string) (linuxBlockDeviceInfo, error),
	slaves func(linuxBlockDeviceInfo) ([]string, error),
) []string {
	queue := []string{strings.TrimSpace(source)}
	seen := map[string]struct{}{}
	var classes []string

	for len(queue) > 0 && len(seen) < 16 {
		current := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if current == "" {
			continue
		}
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}

		info, err := query(current)
		if err != nil {
			continue
		}

		if info.Type == "disk" {
			if class := classifyLinuxBlockDevice(info); class != "unknown" {
				classes = append(classes, class)
			}
			continue
		}

		parents := linuxParentDevicePaths(info)
		if len(parents) == 0 {
			if discovered, err := slaves(info); err == nil {
				parents = discovered
			}
		}
		queue = append(queue, parents...)
	}

	return classes
}

func aggregateLinuxStorageClass(classes []string) string {
	hasNVMe := false
	hasSSD := false

	for _, class := range classes {
		switch strings.ToLower(strings.TrimSpace(class)) {
		case "hdd":
			return "hdd"
		case "nvme":
			hasNVMe = true
		case "ssd":
			hasSSD = true
		}
	}

	switch {
	case hasNVMe:
		return "nvme"
	case hasSSD:
		return "ssd"
	default:
		return "unknown"
	}
}

func classifyLinuxBlockDevice(info linuxBlockDeviceInfo) string {
	if strings.HasPrefix(blockDeviceName(info.Name), "nvme") {
		return "nvme"
	}
	if info.ROTA == nil {
		return "unknown"
	}
	switch *info.ROTA {
	case 0:
		return "ssd"
	case 1:
		return "hdd"
	default:
		return "unknown"
	}
}

func parseLinuxBlockDeviceInfo(out string) (linuxBlockDeviceInfo, error) {
	var resp linuxBlockDevicesResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return linuxBlockDeviceInfo{}, err
	}
	if len(resp.Blockdevices) == 0 {
		return linuxBlockDeviceInfo{}, fmt.Errorf("lsblk returned no block devices")
	}
	return resp.Blockdevices[0], nil
}

func localLinuxBlockDeviceInfo(device string) (linuxBlockDeviceInfo, error) {
	out, err := exec.Command("lsblk", "-J", "-n", "-p", "-o", "NAME,KNAME,PKNAME,TYPE,ROTA", device).Output()
	if err != nil {
		return linuxBlockDeviceInfo{}, err
	}
	return parseLinuxBlockDeviceInfo(string(out))
}

func localLinuxBlockDeviceSlaves(info linuxBlockDeviceInfo) ([]string, error) {
	sysfsName := linuxSysfsBlockName(info)
	if sysfsName == "" {
		return nil, fmt.Errorf("no sysfs block name for %+v", info)
	}
	entries, err := os.ReadDir(filepath.Join("/sys/class/block", sysfsName, "slaves"))
	if err != nil {
		return nil, err
	}

	parents := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := filepath.Base(strings.TrimSpace(entry.Name()))
		if name == "" {
			continue
		}
		parents = append(parents, filepath.Join("/dev", name))
	}
	sort.Strings(parents)
	return parents, nil
}

func localLinuxRotational(device string) (string, error) {
	base := fallbackLinuxBlockBase(device)
	if base == "" {
		return "", fmt.Errorf("no block device base for %q", device)
	}
	data, err := os.ReadFile(fmt.Sprintf("/sys/block/%s/queue/rotational", base))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}

func fallbackLinuxStorageClass(source string, readRotational func(string) (string, error)) string {
	base := fallbackLinuxBlockBase(source)
	if base == "" {
		return "unknown"
	}
	if strings.HasPrefix(base, "nvme") {
		return "nvme"
	}
	rot, err := readRotational(base)
	if err != nil {
		return "unknown"
	}
	switch strings.TrimSpace(rot) {
	case "0":
		return "ssd"
	case "1":
		return "hdd"
	default:
		return "unknown"
	}
}

func fallbackLinuxBlockBase(device string) string {
	base := blockDeviceName(device)
	if base == "" {
		return ""
	}

	switch {
	case strings.HasPrefix(base, "nvme"), strings.HasPrefix(base, "mmcblk"):
		if idx := strings.LastIndex(base, "p"); idx > 0 && hasOnlyDigits(base[idx+1:]) {
			return base[:idx]
		}
		return base
	case strings.HasPrefix(base, "loop"), strings.HasPrefix(base, "dm-"):
		return base
	default:
		trimmed := strings.TrimRight(base, "0123456789")
		if trimmed == "" {
			return base
		}
		return trimmed
	}
}

func blockDeviceName(device string) string {
	device = strings.TrimSpace(device)
	if device == "" {
		return ""
	}
	device = strings.TrimPrefix(device, "/dev/")
	return filepath.Base(device)
}

func parentDevicePath(pkName string) string {
	pkName = strings.TrimSpace(pkName)
	if pkName == "" {
		return ""
	}
	if strings.HasPrefix(pkName, "/dev/") {
		return pkName
	}
	return filepath.Join("/dev", filepath.Base(pkName))
}

func linuxParentDevicePaths(info linuxBlockDeviceInfo) []string {
	parent := parentDevicePath(info.PKName)
	if parent == "" {
		return nil
	}
	return []string{parent}
}

func linuxSysfsBlockName(info linuxBlockDeviceInfo) string {
	if info.KName != "" {
		return filepath.Base(strings.TrimSpace(info.KName))
	}
	return blockDeviceName(info.Name)
}

func hasOnlyDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

// --- Battery / Thermal Detection ---

func localBatteryPercent() (int, bool) {
	switch runtime.GOOS {
	case "darwin":
		return localBatteryDarwin()
	case "linux":
		return localBatteryLinux()
	default:
		return 0, false
	}
}

func localBatteryDarwin() (int, bool) {
	out, err := exec.Command("pmset", "-g", "batt").Output()
	if err != nil {
		return 0, false
	}
	return parsePmsetBattery(string(out))
}

func parsePmsetBattery(out string) (int, bool) {
	for _, line := range strings.Split(out, "\n") {
		if idx := strings.Index(line, "%"); idx > 0 {
			// Walk backward to find the number before %
			start := idx - 1
			for start >= 0 && line[start] >= '0' && line[start] <= '9' {
				start--
			}
			start++
			if start < idx {
				if pct, err := strconv.Atoi(line[start:idx]); err == nil && pct >= 0 && pct <= 100 {
					return pct, true
				}
			}
		}
	}
	return 0, false
}

func parsePmsetPowerSource(out string) string {
	for _, line := range strings.Split(out, "\n") {
		lower := strings.ToLower(line)
		if strings.Contains(lower, "ac power") {
			return "ac"
		}
		if strings.Contains(lower, "battery power") {
			return "battery"
		}
	}
	return ""
}

func localPowerSource() string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.Command("pmset", "-g", "batt").Output()
		if err != nil {
			return ""
		}
		return parsePmsetPowerSource(string(out))
	case "linux":
		for _, name := range []string{"BAT0", "BAT1", "BATT", "AC", "ACAD", "ADP1"} {
			data, err := os.ReadFile(fmt.Sprintf("/sys/class/power_supply/%s/status", name))
			if err != nil {
				continue
			}
			status := strings.TrimSpace(string(data))
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

func localBatteryLinux() (int, bool) {
	// Try common power supply paths
	for _, name := range []string{"BAT0", "BAT1", "BATT"} {
		data, err := os.ReadFile(fmt.Sprintf("/sys/class/power_supply/%s/capacity", name))
		if err != nil {
			continue
		}
		if pct, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil && pct >= 0 && pct <= 100 {
			return pct, true
		}
	}
	return 0, false
}

func localThermalState() string {
	switch runtime.GOOS {
	case "darwin":
		return localThermalDarwin()
	case "linux":
		return localThermalLinux()
	default:
		return ""
	}
}

func localThermalDarwin() string {
	out, err := exec.Command("pmset", "-g", "therm").Output()
	if err != nil {
		return ""
	}
	return parsePmsetThermal(string(out))
}

// parsePmsetThermal maps macOS thermal pressure to: nominal, fair, serious, critical.
// pmset -g therm outputs a CPU_Speed_Limit line (100 = nominal, < 100 = throttled).
func parsePmsetThermal(out string) string {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "CPU_Speed_Limit") {
			fields := strings.Fields(line)
			if len(fields) >= 3 {
				if val, err := strconv.Atoi(fields[len(fields)-1]); err == nil {
					switch {
					case val >= 100:
						return "nominal"
					case val >= 80:
						return "fair"
					case val >= 50:
						return "serious"
					default:
						return "critical"
					}
				}
			}
		}
	}
	return ""
}

func localThermalLinux() string {
	// Check thermal zone temperatures
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return ""
	}
	var maxTemp int
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "thermal_zone") {
			continue
		}
		data, err := os.ReadFile(fmt.Sprintf("/sys/class/thermal/%s/temp", entry.Name()))
		if err != nil {
			continue
		}
		if temp, err := strconv.Atoi(strings.TrimSpace(string(data))); err == nil {
			if temp > maxTemp {
				maxTemp = temp
			}
		}
	}
	if maxTemp == 0 {
		return ""
	}
	// Temps in millidegrees Celsius
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
}

func localThermalZones() []models.ThermalZone {
	switch runtime.GOOS {
	case "linux":
		return linuxThermalZones()
	case "darwin":
		return darwinThermalZones()
	default:
		return nil
	}
}

func linuxThermalZones() []models.ThermalZone {
	entries, err := os.ReadDir("/sys/class/thermal")
	if err != nil {
		return nil
	}
	var zones []models.ThermalZone
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), "thermal_zone") {
			continue
		}
		tempData, err := os.ReadFile(fmt.Sprintf("/sys/class/thermal/%s/temp", entry.Name()))
		if err != nil {
			continue
		}
		tempMilli, err := strconv.Atoi(strings.TrimSpace(string(tempData)))
		if err != nil {
			continue
		}
		tempC := float64(tempMilli) / 1000.0
		typeData, _ := os.ReadFile(fmt.Sprintf("/sys/class/thermal/%s/type", entry.Name()))
		zoneType := strings.TrimSpace(string(typeData))
		if zoneType == "" {
			zoneType = entry.Name()
		}
		zones = append(zones, models.ThermalZone{
			Type:  zoneType,
			TempC: tempC,
			State: thermalStateFromTempC(tempC),
		})
	}
	return zones
}

func darwinThermalZones() []models.ThermalZone {
	out, err := exec.Command("pmset", "-g", "therm").Output()
	if err != nil {
		return nil
	}
	limit := parseCPUThermalLimit(string(out))
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
	return []models.ThermalZone{
		{Type: "cpu", State: state},
	}
}

func parseCPUThermalLimit(out string) int {
	for _, line := range strings.Split(out, "\n") {
		if strings.Contains(line, "CPU_Speed_Limit") {
			fields := strings.Fields(line)
			for _, f := range fields {
				if n, err := strconv.Atoi(f); err == nil {
					return n
				}
			}
		}
	}
	return 0
}

func thermalStateFromTempC(tempC float64) string {
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
}
