package facts

import (
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func mergePressureLevels(levels ...string) string {
	worst := "none"
	for _, level := range levels {
		if pressureSeverity(level) > pressureSeverity(worst) {
			worst = strings.ToLower(strings.TrimSpace(level))
		}
	}
	return worst
}

func pressureSeverity(level string) int {
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "none":
		return 0
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	default:
		return 0
	}
}

func detectMemoryTopology(osName, arch, cpuModel string) (models.MemoryTopology, int) {
	lowerOS := strings.ToLower(strings.TrimSpace(osName))
	lowerArch := strings.ToLower(strings.TrimSpace(arch))
	lowerModel := strings.ToLower(strings.TrimSpace(cpuModel))

	if lowerOS == "darwin" && (strings.Contains(lowerArch, "arm64") || strings.Contains(lowerArch, "aarch64") || strings.Contains(lowerModel, "apple") || strings.Contains(lowerModel, "m1") || strings.Contains(lowerModel, "m2") || strings.Contains(lowerModel, "m3") || strings.Contains(lowerModel, "m4")) {
		class := 1
		switch {
		case strings.Contains(lowerModel, "m4"):
			class = 4
		case strings.Contains(lowerModel, "m3"):
			class = 3
		case strings.Contains(lowerModel, "m2"):
			class = 2
		case strings.Contains(lowerModel, "m1"):
			class = 1
		}
		if strings.Contains(lowerModel, "max") || strings.Contains(lowerModel, "ultra") {
			class++
		}
		return models.MemoryTopologyUnified, class
	}
	return models.MemoryTopologyStandard, 0
}

func parseLinuxPressureStall10(data string) (float64, bool) {
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "some ") {
			continue
		}
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "avg10=") {
				continue
			}
			value := strings.TrimPrefix(field, "avg10=")
			stall10, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return 0, false
			}
			return stall10, true
		}
	}
	return 0, false
}

func linuxPressureLevel(stall10 float64) string {
	switch {
	case stall10 >= 15:
		return "high"
	case stall10 >= 5:
		return "medium"
	case stall10 > 0:
		return "low"
	default:
		return "none"
	}
}

func parseDarwinMemoryPressureLevel(data string) (int, bool) {
	level, err := strconv.Atoi(strings.TrimSpace(data))
	if err != nil {
		return 0, false
	}
	return level, true
}

func darwinPressureLevel(level int) string {
	switch level {
	case 4:
		return "high"
	case 2:
		return "medium"
	case 1:
		return "none"
	default:
		return "none"
	}
}

// parseLinuxPSI reads the memory pressure data and extracts both "some" avg10 and "full" avg10 values.
func parseLinuxPSI(data string) (some float64, full float64, ok bool) {
	hasSome := false
	hasFull := false
	for _, line := range strings.Split(data, "\n") {
		line = strings.TrimSpace(line)
		var isSome bool
		if strings.HasPrefix(line, "some ") {
			isSome = true
		} else if strings.HasPrefix(line, "full ") {
			isSome = false
		} else {
			continue
		}
		for _, field := range strings.Fields(line) {
			if !strings.HasPrefix(field, "avg10=") {
				continue
			}
			value := strings.TrimPrefix(field, "avg10=")
			val, err := strconv.ParseFloat(value, 64)
			if err != nil {
				return 0, 0, false
			}
			if isSome {
				some = val
				hasSome = true
			} else {
				full = val
				hasFull = true
			}
			break
		}
	}
	return some, full, hasSome && hasFull
}

// MapDarwinPressureToPSI maps Darwin vm pressure level (1, 2, 4) to nominal memory PSI levels (some, full).
func MapDarwinPressureToPSI(level int) (float64, float64) {
	switch level {
	case 2: // warning -> medium
		return 30.0, 10.0
	case 4: // critical -> high
		return 75.0, 40.0
	default:
		return 0.0, 0.0
	}
}
