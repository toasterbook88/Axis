package facts

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/models"
)

// ParseDFVolumes parses POSIX `df -kP` output into named volumes.
// Virtual filesystems (tmpfs, overlay, proc, …) are omitted.
func ParseDFVolumes(out string) ([]models.Volume, error) {
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return nil, fmt.Errorf("unexpected df output")
	}
	var vols []models.Volume
	for _, line := range lines[1:] {
		fields := strings.Fields(line)
		if len(fields) < 6 {
			continue
		}
		device := fields[0]
		mount := strings.Join(fields[5:], " ")
		if skipDFFilesystem(device, mount) {
			continue
		}
		totalKB, err := strconv.ParseInt(fields[1], 10, 64)
		if err != nil {
			continue
		}
		freeKB, err := strconv.ParseInt(fields[3], 10, 64)
		if err != nil {
			continue
		}
		v := models.Volume{
			Device:  device,
			Mount:   mount,
			TotalGB: totalKB / (1024 * 1024),
			FreeGB:  freeKB / (1024 * 1024),
		}
		classifyVolume(&v)
		if v.Kind != "network" && v.TotalGB == 0 && v.FreeGB == 0 {
			continue
		}
		vols = append(vols, v)

	}
	return vols, nil
}

func skipDFFilesystem(device, mount string) bool {
	d := strings.ToLower(device)
	switch {
	case d == "tmpfs", d == "devtmpfs", d == "devfs", d == "overlay",
		d == "squashfs", d == "proc", d == "sysfs", d == "cgroup", d == "cgroup2",
		d == "none", d == "udev", d == "dev", d == "efivarfs", d == "map":
		return true
	}
	if strings.HasPrefix(d, "/dev/loop") || strings.HasPrefix(d, "/dev/zram") ||
		strings.HasPrefix(d, "zram") {
		return true
	}

	if strings.HasPrefix(mount, "/snap/") ||
		strings.HasPrefix(mount, "/var/lib/docker/") ||
		strings.HasPrefix(mount, "/run/") ||
		strings.HasPrefix(mount, "/sys/") ||
		strings.HasPrefix(mount, "/System/") ||
		strings.HasPrefix(mount, "/Library/Developer/") ||
		strings.HasPrefix(mount, "/private/var/run/") ||
		mount == "/boot" || strings.HasPrefix(mount, "/boot/") {
		return true
	}
	return false
}

// DFSourceIsNetwork reports whether a df device field names a network filesystem.
// Keep the remote disk-weight Python scanner in sync with this list.
func DFSourceIsNetwork(device string) bool {
	if device == "" {
		return false
	}
	if strings.HasPrefix(device, "//") || strings.Contains(device, ":/") {
		return true
	}
	d := strings.ToLower(device)
	for _, tok := range []string{"nfs", "cifs", "smb", "sshfs", "rclone", "9p", "afs", "ceph", "gluster"} {
		if strings.Contains(d, tok) {
			return true
		}
	}
	return false
}

func classifyVolume(v *models.Volume) {
	dev := strings.ToLower(v.Device)
	if DFSourceIsNetwork(v.Device) {
		v.Kind = "network"
		v.Class = "network"
		if strings.HasPrefix(v.Device, "//") || strings.Contains(dev, "cifs") || strings.Contains(dev, "smb") {
			v.Bus = "cifs"
		} else {
			v.Bus = "nfs"
		}
	} else {
		v.Kind = "local"
		switch {
		case strings.Contains(dev, "nvme"):
			v.Class = "nvme"
			v.Bus = "nvme"
		case strings.Contains(dev, "mmcblk"):
			v.Class = "ssd"
			v.Bus = "mmc"
		default:
			v.Class = "unknown"
			v.Bus = "unknown"

		}
	}
	switch {
	case v.Mount == "/":
		v.Role = "root"
	default:
		v.Role = "other"
	}

}

// ParseMountNetworkVolumes reads `mount` or /proc/mounts and returns
// CIFS/NFS/SMB rows with sizes left at 0 (no df stat of remote servers).
func ParseMountNetworkVolumes(out string) []models.Volume {
	var vols []models.Volume
	for _, line := range strings.Split(out, "\n") {
		if v, ok := parseMountNetworkLine(line); ok {
			vols = append(vols, v)
		}
	}
	return vols
}

func parseMountNetworkLine(line string) (models.Volume, bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return models.Volume{}, false
	}
	const onTok = " on "
	if i := strings.Index(line, onTok); i > 0 {
		rest := line[i+len(onTok):]
		j := strings.Index(rest, " (")
		if j < 0 {
			return models.Volume{}, false
		}
		device := unescapeMountField(line[:i])
		mount := unescapeMountField(rest[:j])
		typ := strings.TrimSpace(strings.Split(rest[j+2:], ",")[0])
		typ = strings.TrimSuffix(typ, ")")
		if skipDFFilesystem(device, mount) {
			return models.Volume{}, false
		}
		if !isNetworkFS(typ) && !looksNetworkDevice(device) {
			return models.Volume{}, false
		}
		return networkVolume(device, mount, typ), true

	}
	fields := strings.Fields(line)
	if len(fields) < 3 || !isNetworkFS(fields[2]) {
		return models.Volume{}, false
	}
	device := unescapeMountField(fields[0])
	mount := unescapeMountField(fields[1])
	if skipDFFilesystem(device, mount) {
		return models.Volume{}, false
	}
	return networkVolume(device, mount, fields[2]), true

}

func networkVolume(device, mount, typ string) models.Volume {
	v := models.Volume{
		Device: device,
		Mount:  mount,
		Kind:   "network",
		Class:  "network",
		Role:   "other",
	}
	switch strings.ToLower(typ) {
	case "nfs", "nfs4":
		v.Bus = "nfs"
	default:
		v.Bus = "cifs"
	}
	if v.Bus == "cifs" && strings.Contains(device, ":/") && !strings.HasPrefix(device, "//") {
		v.Bus = "nfs"
	}
	return v
}

func isNetworkFS(typ string) bool {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "cifs", "smb", "smbfs", "nfs", "nfs4", "afpfs":
		return true
	}
	return false
}

func looksNetworkDevice(device string) bool {
	return strings.HasPrefix(device, "//") || strings.Contains(device, ":/")
}

func unescapeMountField(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && s[i+1] >= '0' && s[i+1] <= '7' {
			n := int(s[i+1]-'0')*64 + int(s[i+2]-'0')*8 + int(s[i+3]-'0')
			b.WriteByte(byte(n))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func mergeVolumes(local, network []models.Volume) []models.Volume {
	seen := make(map[string]struct{}, len(local))
	out := make([]models.Volume, 0, len(local)+len(network))
	for _, v := range local {
		seen[v.Mount] = struct{}{}
		out = append(out, v)
	}
	for _, v := range network {
		if _, ok := seen[v.Mount]; ok {
			continue
		}
		out = append(out, v)
	}
	return out
}
