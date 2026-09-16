package facts

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/persist"
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
var appleFoundationModelsCacheDirFn = func() string { return persist.AxisPath("cache") }
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

var runDiskutilInfo = func(ctx context.Context, device string) (string, error) {
	out, err := exec.CommandContext(ctx, "diskutil", "info", device).Output()
	if err != nil {
		return "", err
	}
	return string(out), nil
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
		cfgPath = config.DefaultConfigPath()
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
	if v, err := localOSVersion(ctx); err != nil {
		facts.Status = models.StatusPartial
	} else {
		facts.OSVersion = v
	}

	// Resources
	res, partial := localResources(ctx)
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
	applyLocalDiskWeights(ctx, facts)
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
