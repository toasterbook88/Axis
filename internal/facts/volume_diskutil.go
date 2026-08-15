package facts

import (
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// ParseDiskutilVolume reads Protocol, Solid State, Removable Media, and
// Device/Link Speed from `diskutil info` text.
func ParseDiskutilVolume(out string) models.Volume {
	var got models.Volume
	if strings.TrimSpace(out) == "" {
		return got
	}
	solid := ""
	protocol := ""
	for _, line := range strings.Split(out, "\n") {
		key, val, ok := diskutilField(line)
		if !ok {
			continue
		}
		switch key {
		case "protocol":
			protocol = strings.ToLower(val)
		case "solid state":
			solid = strings.ToLower(val)
		case "removable media":
			if strings.EqualFold(val, "yes") {
				got.Removable = true
			}
		case "device speed", "link speed":
			if n := parseDiskutilSpeedMbit(val); n > 0 {
				got.LinkMbit = n
			}
		}
	}
	switch {
	case strings.Contains(protocol, "nvme"):
		got.Bus = "nvme"
		got.Class = "nvme"
	case strings.Contains(protocol, "usb"):
		got.Bus = "usb"
	case strings.Contains(protocol, "thunderbolt"):
		got.Bus = "thunderbolt"
	case strings.Contains(protocol, "sata"):
		got.Bus = "sata"
	}
	if got.Class == "" {
		switch {
		case strings.HasPrefix(solid, "yes"):
			got.Class = "ssd"
		case strings.HasPrefix(solid, "no"):
			got.Class = "hdd"
		}
	}
	return got
}

func diskutilField(line string) (key, val string, ok bool) {
	line = strings.TrimSpace(line)
	i := strings.Index(line, ":")
	if i < 0 {
		return "", "", false
	}
	key = strings.ToLower(strings.TrimSpace(line[:i]))
	val = strings.TrimSpace(line[i+1:])
	if key == "" {
		return "", "", false
	}
	return key, val, true
}

func parseDiskutilSpeedMbit(val string) int64 {
	val = strings.TrimSpace(val)
	if val == "" {
		return 0
	}
	fields := strings.Fields(val)
	if len(fields) == 0 {
		return 0
	}
	n, err := strconv.ParseFloat(fields[0], 64)
	if err != nil || n <= 0 {
		return 0
	}
	unit := ""
	if len(fields) > 1 {
		unit = strings.ToLower(fields[1])
	}
	switch {
	case strings.HasPrefix(unit, "gb"):
		return int64(n * 1000)
	case strings.HasPrefix(unit, "mb"):
		return int64(n)
	default:
		return int64(n)
	}
}

func applyDiskutilObservation(v *models.Volume, out string) {
	if v == nil || v.Kind == "network" {
		return
	}
	obs := ParseDiskutilVolume(out)
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

func posixSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}
