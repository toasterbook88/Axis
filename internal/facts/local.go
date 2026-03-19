package facts

import (
	"context"
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

	return facts, nil
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

	// RAM
	if total, free, err := localRAM(); err != nil {
		partial = true
	} else {
		r.RAMTotalMB = total
		r.RAMFreeMB = free
		r.Pressure = computePressure(total, free)
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

	return r, partial
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
		if err != nil {
			return 0, "", err
		}
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
		tOut, err := exec.Command("sysctl", "-n", "hw.memsize").Output()
		if err != nil {
			return 0, 0, err
		}
		totalBytes, _ := strconv.ParseInt(strings.TrimSpace(string(tOut)), 10, 64)
		totalMB := totalBytes / (1024 * 1024)

		vmOut, err := exec.Command("vm_stat").Output()
		if err != nil {
			return totalMB, 0, err
		}
		freeMB := parseDarwinFreeRAM(string(vmOut))
		return totalMB, freeMB, nil
	}
	// Linux
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0, 0, err
	}
	return parseLinuxMeminfo(string(data))
}

func parseDarwinFreeRAM(vmstat string) int64 {
	pageSize := int64(16384) // arm64
	if runtime.GOARCH == "amd64" {
		pageSize = 4096
	}
	var free, inactive int64
	for _, line := range strings.Split(vmstat, "\n") {
		if strings.HasPrefix(line, "Pages free:") {
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
	var total, available int64
	for _, line := range strings.Split(data, "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			total = parseKBField(line)
		} else if strings.HasPrefix(line, "MemAvailable:") {
			available = parseKBField(line)
		}
	}
	return total / 1024, available / 1024, nil
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
	out, err := exec.Command("df", "-k", "/").Output()
	if err != nil {
		return 0, 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) < 2 {
		return 0, 0, fmt.Errorf("unexpected df output")
	}
	fields := strings.Fields(lines[1])
	if len(fields) < 4 {
		return 0, 0, fmt.Errorf("unexpected df fields")
	}
	totalKB, _ := strconv.ParseInt(fields[1], 10, 64)
	freeKB, _ := strconv.ParseInt(fields[3], 10, 64)
	return totalKB / (1024 * 1024), freeKB / (1024 * 1024), nil
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
			if ip.IsLinkLocalMulticast() {
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
