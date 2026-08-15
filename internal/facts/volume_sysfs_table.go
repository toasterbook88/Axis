package facts

import (
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

const sysfsBlockTableCmd = `for d in /sys/class/block/*; do [ -d "$d/queue" ] || continue; name=$(basename "$d"); rota=$(cat "$d/queue/rotational" 2>/dev/null || echo -); rem=$(cat "$d/removable" 2>/dev/null || echo -); speed=$(cat "$d/device/speed" 2>/dev/null || echo -); printf '%s %s %s %s\n' "$name" "$rota" "$rem" "$speed"; done`

// ParseSysfsBlockTable parses "name rotational removable speed" lines
// collected from /sys/class/block without statting remote mounts.
func ParseSysfsBlockTable(table string) map[string]models.Volume {
	out := make(map[string]models.Volume)
	for _, line := range strings.Split(table, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 3 {
			continue
		}
		name := fields[0]
		rota := fields[1]
		rem := fields[2]
		speed := "-"
		if len(fields) >= 4 {
			speed = fields[3]
		}
		if rota == "-" {
			rota = ""
		}
		if rem == "-" {
			rem = ""
		}
		if speed == "-" {
			speed = ""
		}
		out[name] = classifyLinuxBlockFields(name, rota, rem, speed)
	}
	return out
}

func classifyLinuxBlockFields(base, rota, rem, speed string) models.Volume {
	var got models.Volume
	switch {
	case strings.HasPrefix(base, "nvme"):
		got.Bus = "nvme"
		got.Class = "nvme"
	case strings.HasPrefix(base, "mmcblk"):
		got.Bus = "mmc"
		got.Class = "ssd"
	default:
		got.Bus = "sata"
		got.Class = "unknown"
	}
	if got.Bus != "nvme" {
		switch strings.TrimSpace(rota) {
		case "1":
			got.Class = "hdd"
		case "0":
			got.Class = "ssd"
		}
	}
	if strings.TrimSpace(rem) == "1" {
		got.Removable = true
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(speed), 10, 64); err == nil && n > 0 {
		got.Bus = "usb"
		got.LinkMbit = n
	}
	return got
}

// ApplySysfsBlockTable copies table observations onto local volumes.
func ApplySysfsBlockTable(vols []models.Volume, table string) {
	obs := ParseSysfsBlockTable(table)
	if len(obs) == 0 {
		return
	}
	for i := range vols {
		v := &vols[i]
		if v.Kind == "network" {
			continue
		}
		base := fallbackLinuxBlockBase(v.Device)
		o, ok := obs[base]
		if !ok {
			continue
		}
		if o.Bus != "" {
			v.Bus = o.Bus
		}
		if o.Class != "" {
			v.Class = o.Class
		}
		if o.Removable {
			v.Removable = true
		}
		if o.LinkMbit > 0 {
			v.LinkMbit = o.LinkMbit
		}
	}
}
