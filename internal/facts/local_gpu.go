package facts

import (
	"context"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func localGPUs(ctx context.Context) []models.GPUInfo {
	if runtime.GOOS == "darwin" {
		return localGPUsDarwin(ctx)
	}
	return localGPUsLinux(ctx)
}

func localGPUsDarwin(ctx context.Context) []models.GPUInfo {
	out, err := exec.CommandContext(ctx, "system_profiler", "SPDisplaysDataType").Output()
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

func localGPUsLinux(ctx context.Context) []models.GPUInfo {
	// Try nvidia-smi first for NVIDIA GPUs
	gpus := localGPUsNvidiaSMI(ctx)

	// Fallback: lspci for non-NVIDIA or if nvidia-smi unavailable
	lspciGPUs := localGPUsLspci(ctx)
	for _, g := range lspciGPUs {
		if g.Vendor == "nvidia" && len(gpus) > 0 {
			continue // nvidia-smi gave better data
		}
		gpus = append(gpus, g)
	}
	return gpus
}

func localGPUsNvidiaSMI(ctx context.Context) []models.GPUInfo {
	out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=name,memory.total,memory.free", "--format=csv,noheader,nounits").Output()
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
		parts := strings.Split(line, ", ")
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
		// memory.free is present only when the query requests it; older callers
		// (and the two-column remote fallback) legitimately omit it.
		if len(parts) >= 3 {
			if free, err := strconv.Atoi(strings.TrimSpace(parts[2])); err == nil {
				gpu.VRAMFreeMB = free
			}
		}
		gpus = append(gpus, gpu)
	}
	return gpus
}

func localGPUsLspci(ctx context.Context) []models.GPUInfo {
	out, err := exec.CommandContext(ctx, "bash", "-c", `lspci 2>/dev/null | grep -iE 'vga|3d' | sed 's/.*: //'`).Output()
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

func localGPUUtilPercent(ctx context.Context) (float64, bool) {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "ioreg", "-r", "-c", "AGXAccelerator").Output()
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
		out, err := exec.CommandContext(ctx, "nvidia-smi", "--query-gpu=utilization.gpu", "--format=csv,noheader,nounits").Output()
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
