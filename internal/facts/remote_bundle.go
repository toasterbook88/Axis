package facts

import (
	"context"
	"encoding/base64"
	"fmt"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// remoteFactBundleScript gathers mandatory + best-effort facts in a single bash
// process so slow login shells (fish) only pay startup cost once per node.
// Output is a simple framed key/value protocol parsed by parseRemoteFactBundle.
const remoteFactBundleScript = `
set +e
printf '%s\n' '__AXIS_BUNDLE_V1__'

OS=$(uname -s 2>/dev/null)
ARCH=$(uname -m 2>/dev/null)
HOSTNAME=$(hostname 2>/dev/null || uname -n 2>/dev/null)
printf 'os=%s\n' "$OS"
printf 'arch=%s\n' "$ARCH"
printf 'hostname=%s\n' "$HOSTNAME"

case "$(printf '%s' "$OS" | tr '[:upper:]' '[:lower:]')" in
  darwin)
    printf 'os_version=%s\n' "$(sw_vers -productVersion 2>/dev/null)"
    printf 'cpu_cores=%s\n' "$(sysctl -n hw.ncpu 2>/dev/null)"
    printf 'cpu_model=%s\n' "$(sysctl -n machdep.cpu.brand_string 2>/dev/null || sysctl -n hw.model 2>/dev/null)"
    printf 'hw_memsize=%s\n' "$(sysctl -n hw.memsize 2>/dev/null)"
    printf 'vm_stat_b64=%s\n' "$(vm_stat 2>/dev/null | base64 | tr -d '\n')"
    printf 'loadavg=%s\n' "$(sysctl -n vm.loadavg 2>/dev/null)"
    printf 'pressure=%s\n' "$(sysctl -n kern.memorystatus_vm_pressure_level 2>/dev/null)"
    printf 'gpu_b64=%s\n' "$(system_profiler SPDisplaysDataType 2>/dev/null | grep -E 'Chipset Model:|VRAM|Metal' | sed 's/^ *//' | base64 | tr -d '\n')"
    printf 'identity=%s\n' "$(ioreg -rd1 -c IOPlatformExpertDevice 2>/dev/null | awk -F '"' '/IOPlatformUUID/ {print $4; exit}')"
    printf 'battery=%s\n' "$(pmset -g batt 2>/dev/null | base64 | tr -d '\n')"
    printf 'therm=%s\n' "$(pmset -g therm 2>/dev/null | base64 | tr -d '\n')"
    printf 'storage=%s\n' "$(diskutil info / 2>/dev/null | base64 | tr -d '\n')"
    ;;
  *)
    printf 'os_version=%s\n' "$(uname -r 2>/dev/null)"
    printf 'cpu_cores=%s\n' "$(nproc 2>/dev/null)"
    printf 'cpu_model=%s\n' "$(grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2)"
    printf 'meminfo_b64=%s\n' "$(grep -E 'MemTotal|MemAvailable|MemFree' /proc/meminfo 2>/dev/null | base64 | tr -d '\n')"
    printf 'loadavg=%s\n' "$(cat /proc/loadavg 2>/dev/null)"
    printf 'pressure_b64=%s\n' "$(cat /proc/pressure/memory 2>/dev/null | base64 | tr -d '\n')"
    printf 'gpu_b64=%s\n' "$(nvidia-smi --query-gpu=name,memory.total --format=csv,noheader,nounits 2>/dev/null || lspci 2>/dev/null | grep -iE 'vga|3d' | sed 's/.*: //' | base64 | tr -d '\n')"
    printf 'identity=%s\n' "$(cat /etc/machine-id 2>/dev/null || cat /var/lib/dbus/machine-id 2>/dev/null)"
    printf 'battery=%s\n' "$(cat /sys/class/power_supply/BAT0/capacity /sys/class/power_supply/BAT1/capacity /sys/class/power_supply/BATT/capacity 2>/dev/null | head -1)"
    printf 'power=%s\n' "$(for n in AC ADP0 ACAD Mains; do s=$(cat /sys/class/power_supply/$n/status 2>/dev/null); [ -n "$s" ] && echo "$s" && break; done)"
    printf 'therm_b64=%s\n' "$(cat /sys/class/thermal/thermal_zone*/temp 2>/dev/null | base64 | tr -d '\n')"
    printf 'therm_types_b64=%s\n' "$(for z in /sys/class/thermal/thermal_zone*; do cat "$z/type" 2>/dev/null; echo; done | base64 | tr -d '\n')"
    printf 'findmnt=%s\n' "$(findmnt -n -o SOURCE / 2>/dev/null)"
    ;;
esac

printf 'df_b64=%s\n' "$(df -kP / 2>/dev/null | base64 | tr -d '\n')"
printf 'addrs_b64=%s\n' "$(if command -v ip >/dev/null 2>&1; then ip -o addr show scope global 2>/dev/null || ip addr show scope global 2>/dev/null | awk '/inet/ {print $2}'; else ifconfig 2>/dev/null | awk '/^[a-z]/ {iface=$1} /inet / && !/127.0.0.1/ {print iface, $2}; /inet6 / && !/::1/ && !/fe80/ {print iface, $2}' | sed 's/://'; fi | base64 | tr -d '\n')"

# Linux storage: best-effort rotational flag for root's block device (mapper-aware via lsblk -s/-n).
if command -v lsblk >/dev/null 2>&1; then
  ROOT_SRC=$(findmnt -n -o SOURCE / 2>/dev/null)
  if [ -n "$ROOT_SRC" ]; then
    # Prefer leaf disk rotational via lsblk reverse dependencies when available.
    ROTA=$(lsblk -ndo ROTA,TYPE,NAME "$ROOT_SRC" 2>/dev/null | awk '$2=="disk"{print $1; exit}')
    if [ -z "$ROTA" ]; then
      ROTA=$(lsblk -sndo ROTA,TYPE,NAME "$ROOT_SRC" 2>/dev/null | awk '$2=="disk"{print $1; exit}')
    fi
    if [ -z "$ROTA" ]; then
      ROTA=$(lsblk -ndo ROTA "$ROOT_SRC" 2>/dev/null | head -1)
    fi
    printf 'lsblk_rota=%s\n' "$ROTA"
  fi
fi

# Tools: path + version (same tool set as defaultToolDefs)
for t in go python3 git jq nix docker ollama mlx_lm llama-cli llama-server node swift cargo gcc; do
  p=$(command -v "$t" 2>/dev/null)
  if [ -n "$p" ]; then
    printf 'tool_%s=%s\n' "$t" "$p"
    case "$t" in
      go) v=$("$p" version 2>/dev/null | head -1) ;;
      python3) v=$("$p" --version 2>/dev/null | head -1) ;;
      git) v=$("$p" --version 2>/dev/null | head -1) ;;
      jq) v=$("$p" --version 2>/dev/null | head -1) ;;
      nix) v=$("$p" --version 2>/dev/null | head -1) ;;
      docker) v=$("$p" --version 2>/dev/null | head -1) ;;
      ollama) v=$("$p" --version 2>/dev/null | head -1) ;;
      mlx_lm) v=$("$p" --help 2>/dev/null | head -1) ;;
      llama-cli|llama-server) v=$("$p" --version 2>/dev/null | head -1) ;;
      node) v=$("$p" --version 2>/dev/null | head -1) ;;
      swift) v=$("$p" --version 2>/dev/null | head -1) ;;
      cargo) v=$("$p" --version 2>/dev/null | head -1) ;;
      gcc) v=$("$p" --version 2>/dev/null | head -1) ;;
      *) v= ;;
    esac
    if [ -n "$v" ]; then
      printf 'toolver_%s=%s\n' "$t" "$(printf '%s' "$v" | tr '\n' ' ' | sed 's/[[:space:]]*$//')"
    fi
  fi
done

printf '%s\n' '__AXIS_BUNDLE_END__'
`

type remoteBundleKV map[string]string

func parseRemoteFactBundle(out string) (remoteBundleKV, error) {
	if !strings.Contains(out, "__AXIS_BUNDLE_V1__") {
		return nil, fmt.Errorf("missing bundle header")
	}
	kv := make(remoteBundleKV)
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || line == "__AXIS_BUNDLE_V1__" {
			continue
		}
		if line == "__AXIS_BUNDLE_END__" {
			break
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || k == "" {
			continue
		}
		kv[k] = v
	}
	if kv["os"] == "" && kv["arch"] == "" {
		return nil, fmt.Errorf("bundle missing core fields")
	}
	return kv, nil
}

func b64field(kv remoteBundleKV, key string) string {
	raw := kv[key]
	if raw == "" {
		return ""
	}
	// Values are base64 without newlines; tolerate raw text if decode fails.
	dec, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return raw
	}
	return string(dec)
}

// tryBundleCollect fills facts from a single remote bash invocation.
// Returns false when the bundle path should fall back to legacy multi-Run.
// On false, facts are left unchanged (no partial reasons recorded) so legacy
// can still yield StatusComplete.
func (c *RemoteCollector) tryBundleCollect(ctx context.Context, facts *models.NodeFacts) bool {
	out, err := c.Exec.Run(ctx, remoteFactBundleScript)
	if err != nil {
		return false
	}
	kv, err := parseRemoteFactBundle(out)
	if err != nil {
		return false
	}

	partial := false
	note := func(probe, msg string) {
		partial = true
		facts.PartialReasons = append(facts.PartialReasons, models.PartialReason{Probe: probe, Message: msg})
	}

	osName := strings.ToLower(strings.TrimSpace(kv["os"]))
	facts.OS = osName
	if h := strings.TrimSpace(kv["hostname"]); h != "" {
		facts.Hostname = h
	} else {
		note("hostname", "empty")
		facts.Hostname = c.Hostname
	}
	facts.Arch = strings.TrimSpace(kv["arch"])
	if facts.Arch == "" {
		note("arch", "empty")
	}
	facts.OSVersion = strings.TrimSpace(kv["os_version"])

	// Identity
	if id := strings.TrimSpace(kv["identity"]); id != "" {
		src := "linux-machine-id"
		if osName == "darwin" {
			src = "darwin-platform-uuid"
		}
		facts.Identity = models.NewNodeIdentity(id, src)
	}

	res := &models.Resources{Pressure: "none"}
	if cores, err := strconv.Atoi(strings.TrimSpace(kv["cpu_cores"])); err == nil {
		res.CPUCores = cores
	} else {
		note("cpu_cores", "parse failed")
	}
	res.CPUModel = strings.TrimSpace(kv["cpu_model"])
	res.MemoryTopology, res.MemoryClass = detectMemoryTopology(osName, facts.Arch, res.CPUModel)

	if osName == "darwin" {
		if totalBytes, err := strconv.ParseInt(strings.TrimSpace(kv["hw_memsize"]), 10, 64); err == nil && totalBytes > 0 {
			res.RAMTotalMB = totalBytes / (1024 * 1024)
		} else {
			note("ram_total", "parse failed")
		}
		vm := b64field(kv, "vm_stat_b64")
		if vm != "" {
			res.RAMFreeMB = parseDarwinFreeRAM(vm)
		} else {
			note("ram_free", "vm_stat missing")
		}
		if load1, load5, load15, err := parseDarwinLoadavg(kv["loadavg"]); err == nil {
			res.Load1M, res.Load5M, res.Load15M = load1, load5, load15
		} else {
			note("loadavg", err.Error())
		}
		if level, ok := parseDarwinMemoryPressureLevel(kv["pressure"]); ok {
			someAvg, fullAvg := MapDarwinPressureToPSI(level)
			res.Pressure = mergePressureLevels(res.Pressure, darwinPressureLevel(level))
			res.PressureSource = "darwin-vm-pressure"
			res.MemoryPSISomeAvg10 = someAvg
			res.MemoryPSIFullAvg10 = fullAvg
		}
	} else {
		mem := b64field(kv, "meminfo_b64")
		if total, avail, err := parseLinuxMeminfo(mem); err == nil {
			res.RAMTotalMB = total
			res.RAMFreeMB = avail
		} else {
			note("meminfo", err.Error())
		}
		if load1, load5, load15, err := parseLoadavgFields(kv["loadavg"]); err == nil {
			res.Load1M, res.Load5M, res.Load15M = load1, load5, load15
		} else {
			note("loadavg", err.Error())
		}
		psi := b64field(kv, "pressure_b64")
		if stall10, ok := parseLinuxPressureStall10(psi); ok {
			someAvg, fullAvg, _ := parseLinuxPSI(psi)
			res.Pressure = mergePressureLevels(res.Pressure, linuxPressureLevel(stall10))
			res.PressureSource = "linux-psi"
			res.PressureStall10 = stall10
			res.MemoryPSISomeAvg10 = someAvg
			res.MemoryPSIFullAvg10 = fullAvg
		}
	}

	if res.RAMTotalMB > 0 && res.PressureSource == "" {
		res.Pressure = computePressure(res.RAMTotalMB, res.RAMFreeMB)
		res.PressureSource = "free-ram"
	}

	dfOut := b64field(kv, "df_b64")
	if total, free, err := parseDFOutput(dfOut); err == nil {
		res.DiskTotalGB = total
		res.DiskFreeGB = free
	} else {
		note("df", err.Error())
	}

	gpuOut := strings.TrimSpace(b64field(kv, "gpu_b64"))
	if gpuOut != "" {
		if strings.Contains(gpuOut, ", ") && !strings.Contains(gpuOut, "Chipset Model") {
			res.GPUs = parseNvidiaSMIOutput(gpuOut)
		} else if strings.Contains(gpuOut, "Chipset Model") {
			res.GPUs = parseSystemProfilerGPUs(gpuOut)
		} else {
			for _, line := range strings.Split(gpuOut, "\n") {
				if line = strings.TrimSpace(line); line != "" {
					res.GPUs = append(res.GPUs, models.GPUFromString(line))
				}
			}
		}
	}

	// Storage class
	if osName == "darwin" {
		if s := b64field(kv, "storage"); s != "" {
			res.StorageClass = parseDiskutilStorageClass(s)
		}
	} else {
		src := strings.TrimSpace(kv["findmnt"])
		rota := strings.TrimSpace(kv["lsblk_rota"])
		res.StorageClass = storageClassFromLinuxBundle(src, rota)
		// Mapper-backed roots need the full slave walk when rota is empty.
		if res.StorageClass == "unknown" && src != "" &&
			(strings.Contains(src, "mapper") || strings.Contains(src, "dm-")) {
			if walked := c.remoteStorageClass(ctx, osName); walked != "" && walked != "unknown" {
				res.StorageClass = walked
			}
		}
	}

	// Battery + thermal (must match legacy remote collectors for placement safety)
	if osName == "darwin" {
		if batt := b64field(kv, "battery"); batt != "" {
			if pct, ok := parseDarwinBatteryPercent(batt); ok {
				res.BatteryPercent = &pct
			}
			res.PowerSource = parsePmsetPowerSource(batt)
		}
		if th := b64field(kv, "therm"); th != "" {
			// Established parser: CPU_Speed_Limit, not free-text "critical".
			res.ThermalState = parsePmsetThermal(th)
		}
	} else {
		if b := strings.TrimSpace(kv["battery"]); b != "" {
			if pct, err := strconv.Atoi(b); err == nil {
				res.BatteryPercent = &pct
			}
		}
		if p := strings.TrimSpace(kv["power"]); p != "" {
			switch strings.ToLower(p) {
			case "charging", "full", "not charging":
				res.PowerSource = "ac"
			case "discharging":
				res.PowerSource = "battery"
			default:
				res.PowerSource = strings.ToLower(p)
			}
		}
		// Linux thermal temps (milli-C) → ThermalState + ThermalZones (placement uses these).
		tempsRaw := b64field(kv, "therm_b64")
		typesRaw := b64field(kv, "therm_types_b64")
		res.ThermalZones = parseLinuxThermalZonesBundle(tempsRaw, typesRaw)
		if st := models.ThermalStateFromZones(res.ThermalZones); st != "" {
			res.ThermalState = st
		} else if tempsRaw != "" {
			res.ThermalState = linuxThermalStateFromTempLines(tempsRaw)
		}
	}

	facts.Resources = res

	// Addresses
	for _, line := range strings.Split(b64field(kv, "addrs_b64"), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		addr := parseRemoteAddrLine(line)
		if addr.Address != "" {
			facts.Addresses = append(facts.Addresses, addr)
		}
	}

	// Tools from paths + versions
	classByName := map[string]models.ToolClass{}
	for _, td := range defaultToolDefs() {
		classByName[td.name] = td.class
	}
	for k, v := range kv {
		if !strings.HasPrefix(k, "tool_") || strings.HasPrefix(k, "toolver_") {
			continue
		}
		name := strings.TrimPrefix(k, "tool_")
		path := strings.TrimSpace(v)
		if path == "" {
			continue
		}
		ti := models.ToolInfo{Name: name, Path: path, Class: classByName[name]}
		if ti.Class == "" {
			ti.Class = models.ToolClassRuntime
		}
		if ver := strings.TrimSpace(kv["toolver_"+name]); ver != "" {
			ti.Version = parseVersionString(ver)
		}
		facts.Tools = append(facts.Tools, ti)
	}

	if partial {
		facts.Status = models.StatusPartial
	} else {
		facts.Status = models.StatusComplete
	}
	return true
}

// storageClassFromLinuxBundle combines findmnt source name with lsblk ROTA when present.
func storageClassFromLinuxBundle(source, rota string) string {
	s := strings.ToLower(source)
	switch {
	case strings.Contains(s, "nvme"):
		return "nvme"
	case rota == "1":
		return "hdd"
	case rota == "0":
		return "ssd"
	case strings.Contains(s, "mmc"):
		return "ssd"
	default:
		return "unknown"
	}
}

func parseDarwinBatteryPercent(pmset string) (int, bool) {
	// e.g. " -InternalBattery-0 (id=...) 82%; charging; ..."
	for _, field := range strings.Fields(pmset) {
		if strings.HasSuffix(field, "%;") || strings.HasSuffix(field, "%") {
			num := strings.TrimSuffix(strings.TrimSuffix(field, ";"), "%")
			if pct, err := strconv.Atoi(num); err == nil {
				return pct, true
			}
		}
	}
	return 0, false
}

func parseLinuxThermalZonesBundle(tempsRaw, typesRaw string) []models.ThermalZone {
	temps := strings.Split(strings.TrimSpace(tempsRaw), "\n")
	types := strings.Split(strings.TrimSpace(typesRaw), "\n")
	var zones []models.ThermalZone
	for i, tline := range temps {
		tline = strings.TrimSpace(tline)
		if tline == "" {
			continue
		}
		tempMilli, err := strconv.Atoi(tline)
		if err != nil {
			continue
		}
		tempC := float64(tempMilli) / 1000.0
		zoneType := ""
		if i < len(types) {
			zoneType = strings.TrimSpace(types[i])
		}
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
}

func linuxThermalStateFromTempLines(tempsRaw string) string {
	var maxTemp int
	for _, line := range strings.Split(strings.TrimSpace(tempsRaw), "\n") {
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
}
