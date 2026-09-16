package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
)

func localVolumes(ctx context.Context) []models.Volume {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "df", "-kPl").Output()
	if err != nil {
		return nil
	}
	local, err := ParseDFVolumes(string(out))
	if err != nil {
		local = nil
	}
	vols := mergeVolumes(local, ParseMountNetworkVolumes(localMountTable(ctx)))
	switch runtime.GOOS {
	case "linux":
		for i := range vols {
			applyLinuxBlockObservation(&vols[i], linuxSysfsRoot)
		}
	case "darwin":
		for i := range vols {
			if vols[i].Kind == "network" || vols[i].Device == "" {
				continue
			}
			out, err := runDiskutilInfo(ctx, vols[i].Device)
			if err != nil {
				continue
			}
			applyDiskutilObservation(&vols[i], out)
		}
	}
	return vols
}

func localMountTable(ctx context.Context) string {
	if data, err := os.ReadFile("/proc/mounts"); err == nil {
		return string(data)
	}
	out, err := exec.CommandContext(ctx, "mount").Output()
	if err != nil {
		return ""
	}
	return string(out)
}

func localStorageClass(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		return localStorageClassDarwin(ctx)
	case "linux":
		return localStorageClassLinux(ctx)
	default:
		return "unknown"
	}
}

func localStorageClassDarwin(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "diskutil", "info", "/").Output()
	if err != nil {
		return "unknown"
	}
	return parseDiskutilStorageClass(string(out))
}

func parseDiskutilStorageClass(out string) string {
	isSolid := false
	isNVMe := false
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "Solid State:") && strings.Contains(strings.ToLower(trimmed), "yes") {
			isSolid = true
		}
		if strings.HasPrefix(trimmed, "Protocol:") && strings.Contains(strings.ToLower(trimmed), "nvme") {
			isNVMe = true
		}
		if strings.HasPrefix(trimmed, "Device / Media Name:") && strings.Contains(strings.ToLower(trimmed), "nvme") {
			isNVMe = true
		}
	}
	if isNVMe {
		return "nvme"
	}
	if isSolid {
		return "ssd"
	}
	return "unknown"
}

func localStorageClassLinux(ctx context.Context) string {
	out, err := exec.CommandContext(ctx, "bash", "-c", `findmnt -n -o SOURCE / 2>/dev/null`).Output()
	if err != nil {
		return "unknown"
	}
	return resolveLinuxStorageClass(
		strings.TrimSpace(string(out)),
		func(device string) (linuxBlockDeviceInfo, error) {
			return localLinuxBlockDeviceInfoContext(ctx, device)
		},
		localLinuxBlockDeviceSlaves,
		localLinuxRotational,
	)
}

type linuxBlockDeviceInfo struct {
	Name   string `json:"name"`
	KName  string `json:"kname"`
	PKName string `json:"pkname"`
	Type   string `json:"type"`
	ROTA   *int   `json:"rota"`
}

type linuxBlockDevicesResponse struct {
	Blockdevices []linuxBlockDeviceInfo `json:"blockdevices"`
}

func resolveLinuxStorageClass(
	source string,
	query func(string) (linuxBlockDeviceInfo, error),
	slaves func(linuxBlockDeviceInfo) ([]string, error),
	fallbackReadRotational func(string) (string, error),
) string {
	source = strings.TrimSpace(source)
	if source == "" {
		return "unknown"
	}

	classes := linuxStorageAncestorClasses(source, query, slaves)
	if class := aggregateLinuxStorageClass(classes); class != "unknown" {
		return class
	}

	return fallbackLinuxStorageClass(source, fallbackReadRotational)
}

func linuxStorageAncestorClasses(
	source string,
	query func(string) (linuxBlockDeviceInfo, error),
	slaves func(linuxBlockDeviceInfo) ([]string, error),
) []string {
	queue := []string{strings.TrimSpace(source)}
	seen := map[string]struct{}{}
	var classes []string

	for len(queue) > 0 && len(seen) < 16 {
		current := strings.TrimSpace(queue[0])
		queue = queue[1:]
		if current == "" {
			continue
		}
		if _, ok := seen[current]; ok {
			continue
		}
		seen[current] = struct{}{}

		info, err := query(current)
		if err != nil {
			continue
		}

		if info.Type == "disk" {
			if class := classifyLinuxBlockDevice(info); class != "unknown" {
				classes = append(classes, class)
			}
			continue
		}

		parents := linuxParentDevicePaths(info)
		if len(parents) == 0 {
			if discovered, err := slaves(info); err == nil {
				parents = discovered
			}
		}
		queue = append(queue, parents...)
	}

	return classes
}

func aggregateLinuxStorageClass(classes []string) string {
	hasNVMe := false
	hasSSD := false

	for _, class := range classes {
		switch strings.ToLower(strings.TrimSpace(class)) {
		case "hdd":
			return "hdd"
		case "nvme":
			hasNVMe = true
		case "ssd":
			hasSSD = true
		}
	}

	switch {
	case hasNVMe:
		return "nvme"
	case hasSSD:
		return "ssd"
	default:
		return "unknown"
	}
}

func classifyLinuxBlockDevice(info linuxBlockDeviceInfo) string {
	if strings.HasPrefix(blockDeviceName(info.Name), "nvme") {
		return "nvme"
	}
	if info.ROTA == nil {
		return "unknown"
	}
	switch *info.ROTA {
	case 0:
		return "ssd"
	case 1:
		return "hdd"
	default:
		return "unknown"
	}
}

func parseLinuxBlockDeviceInfo(out string) (linuxBlockDeviceInfo, error) {
	var resp linuxBlockDevicesResponse
	if err := json.Unmarshal([]byte(out), &resp); err != nil {
		return linuxBlockDeviceInfo{}, err
	}
	if len(resp.Blockdevices) == 0 {
		return linuxBlockDeviceInfo{}, fmt.Errorf("lsblk returned no block devices")
	}
	return resp.Blockdevices[0], nil
}

func localLinuxBlockDeviceInfoContext(ctx context.Context, device string) (linuxBlockDeviceInfo, error) {
	out, err := exec.CommandContext(ctx, "lsblk", "-J", "-n", "-p", "-o", "NAME,KNAME,PKNAME,TYPE,ROTA", device).Output()
	if err != nil {
		return linuxBlockDeviceInfo{}, err
	}
	return parseLinuxBlockDeviceInfo(string(out))
}

func localLinuxBlockDeviceSlaves(info linuxBlockDeviceInfo) ([]string, error) {
	sysfsName := linuxSysfsBlockName(info)
	if sysfsName == "" {
		return nil, fmt.Errorf("no sysfs block name for %+v", info)
	}
	entries, err := os.ReadDir(filepath.Join("/sys/class/block", sysfsName, "slaves"))
	if err != nil {
		return nil, err
	}

	parents := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := filepath.Base(strings.TrimSpace(entry.Name()))
		if name == "" {
			continue
		}
		parents = append(parents, filepath.Join("/dev", name))
	}
	sort.Strings(parents)
	return parents, nil
}

func localLinuxRotational(device string) (string, error) {
	base := fallbackLinuxBlockBase(device)
	if base == "" {
		return "", fmt.Errorf("no block device base for %q", device)
	}
	data, err := os.ReadFile(fmt.Sprintf("/sys/block/%s/queue/rotational", base))
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(data)), nil
}
