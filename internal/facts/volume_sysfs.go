package facts

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// linuxSysfsRoot is the sysfs prefix. Tests override via ObserveLinuxBlock's root.
const linuxSysfsRoot = "/sys"

// ObserveLinuxBlock reads rotational, removable, and USB speed for a local
// block device under sysRoot (normally /sys). Network devices are ignored.
func ObserveLinuxBlock(device, sysRoot string) models.Volume {
	var out models.Volume
	if device == "" || looksNetworkDevice(device) {
		return out
	}
	base := fallbackLinuxBlockBase(device)
	if base == "" {
		return out
	}
	block := filepath.Join(sysRoot, "class", "block", base)
	if st, err := os.Stat(block); err != nil || !st.IsDir() {
		return out
	}

	switch {
	case strings.HasPrefix(base, "nvme"):
		out.Bus = "nvme"
		out.Class = "nvme"
	case strings.HasPrefix(base, "mmcblk"):
		out.Bus = "mmc"
		out.Class = "ssd"
	default:
		out.Bus = "sata"
		out.Class = "unknown"
	}

	if rot := strings.TrimSpace(readSysFile(filepath.Join(block, "queue", "rotational"))); rot != "" {
		if out.Bus != "nvme" {
			if rot == "1" {
				out.Class = "hdd"
			} else if rot == "0" {
				out.Class = "ssd"
			}
		}
	}
	if rem := strings.TrimSpace(readSysFile(filepath.Join(block, "removable"))); rem == "1" {
		out.Removable = true
	}
	if speed := strings.TrimSpace(readSysFile(filepath.Join(block, "device", "speed"))); speed != "" {
		if n, err := strconv.ParseInt(speed, 10, 64); err == nil && n > 0 {
			out.Bus = "usb"
			out.LinkMbit = n
		}
	}
	return out
}

func applyLinuxBlockObservation(v *models.Volume, sysRoot string) {
	if v == nil || v.Kind == "network" {
		return
	}
	obs := ObserveLinuxBlock(v.Device, sysRoot)
	if obs.Bus != "" {
		v.Bus = obs.Bus
	}
	if obs.Class != "" {
		v.Class = obs.Class
	}
	if obs.Removable {
		v.Removable = true
	}
	if obs.LinkMbit > 0 {
		v.LinkMbit = obs.LinkMbit
	}
}

func readSysFile(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}
