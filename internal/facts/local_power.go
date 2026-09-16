package facts

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

func localBatteryPercent(ctx context.Context) (int, bool) {
	switch runtime.GOOS {
	case "darwin":
		return localBatteryDarwin(ctx)
	case "linux":
		return localBatteryLinux()
	default:
		return 0, false
	}
}

func localBatteryDarwin(ctx context.Context) (int, bool) {
	out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
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

func localPowerSource(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		out, err := exec.CommandContext(ctx, "pmset", "-g", "batt").Output()
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

func localThermalState(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		return localThermalDarwin(ctx)
	case "linux":
		return localThermalLinux()
	default:
		return ""
	}
}

func localThermalDarwin(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "pmset", "-g", "therm").Output()
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

func localThermalZones(ctx context.Context) []models.ThermalZone {
	switch runtime.GOOS {
	case "linux":
		return linuxThermalZones()
	case "darwin":
		return darwinThermalZones(ctx)
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

func darwinThermalZones(ctx context.Context) []models.ThermalZone {
	out, err := exec.CommandContext(ctx, "pmset", "-g", "therm").Output()
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
