package facts

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

// RemoteCollector collects facts from a remote node via SSH.
// SSH is a Phase 1 temporary transport — the Executor interface
// provides a clean seam for future axisd-based collection.
type RemoteCollector struct {
	NodeName string
	Role     string
	Hostname string
	Exec     transport.Executor
}

// NewRemoteCollector creates a remote fact collector.
func NewRemoteCollector(name, role, hostname string, exec transport.Executor) *RemoteCollector {
	return &RemoteCollector{NodeName: name, Role: role, Hostname: hostname, Exec: exec}
}

// Collect gathers facts from the remote node.
// Maps failures precisely:
//   - connect/first-command fail → unreachable
//   - subsequent command fail → partial
func (c *RemoteCollector) Collect(ctx context.Context) (*models.NodeFacts, error) {
	facts := &models.NodeFacts{
		Name:        c.NodeName,
		Role:        c.Role,
		Hostname:    c.Hostname,
		Status:      models.StatusComplete,
		CollectedAt: time.Now().UTC(),
	}

	partial := false

	// Detect OS — if this fails, the node is unreachable at command level
	osOut, err := c.Exec.Run(ctx, "uname -s")
	if err != nil {
		facts.Status = models.StatusUnreachable
		facts.Error = err.Error()
		return facts, nil
	}
	osName := strings.ToLower(strings.TrimSpace(osOut))
	facts.OS = osName

	// Arch
	if archOut, err := c.Exec.Run(ctx, "uname -m"); err != nil {
		partial = true
	} else {
		facts.Arch = strings.TrimSpace(archOut)
	}

	// OS version
	var verCmd string
	if osName == "darwin" {
		verCmd = "sw_vers -productVersion"
	} else {
		verCmd = "uname -r"
	}
	if verOut, err := c.Exec.Run(ctx, verCmd); err != nil {
		partial = true
	} else {
		facts.OSVersion = strings.TrimSpace(verOut)
	}

	// Resources
	res, resPartial := c.remoteResources(ctx, osName)
	facts.Resources = res
	if resPartial {
		partial = true
	}

	// Tools
	facts.Tools = c.remoteTools(ctx)

	if partial {
		facts.Status = models.StatusPartial
	}
	return facts, nil
}

func (c *RemoteCollector) remoteResources(ctx context.Context, osName string) (*models.Resources, bool) {
	r := &models.Resources{Pressure: "none"}
	partial := false

	// CPU cores
	var coresCmd, modelCmd string
	if osName == "darwin" {
		coresCmd = "sysctl -n hw.ncpu"
		modelCmd = "sysctl -n machdep.cpu.brand_string 2>/dev/null || sysctl -n hw.model"
	} else {
		coresCmd = "nproc"
		modelCmd = "grep -m1 'model name' /proc/cpuinfo 2>/dev/null | cut -d: -f2"
	}

	if out, err := c.Exec.Run(ctx, coresCmd); err != nil {
		partial = true
	} else {
		r.CPUCores, _ = strconv.Atoi(strings.TrimSpace(out))
	}

	if out, err := c.Exec.Run(ctx, modelCmd); err == nil {
		r.CPUModel = strings.TrimSpace(out)
	}

	// RAM
	if osName == "darwin" {
		if out, err := c.Exec.Run(ctx, "sysctl -n hw.memsize"); err != nil {
			partial = true
		} else {
			totalBytes, _ := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
			r.RAMTotalMB = totalBytes / (1024 * 1024)
		}
		if out, err := c.Exec.Run(ctx, "vm_stat"); err != nil {
			partial = true
		} else {
			r.RAMFreeMB = parseDarwinFreeRAM(out)
		}
	} else {
		if out, err := c.Exec.Run(ctx, `grep -E 'MemTotal|MemAvailable' /proc/meminfo`); err != nil {
			partial = true
		} else {
			total, avail, _ := parseLinuxMeminfo(out)
			r.RAMTotalMB = total
			r.RAMFreeMB = avail
		}
	}

	if r.RAMTotalMB > 0 {
		r.Pressure = computePressure(r.RAMTotalMB, r.RAMFreeMB)
	}

	// Disk
	if out, err := c.Exec.Run(ctx, "df -k /"); err != nil {
		partial = true
	} else {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		if len(lines) >= 2 {
			fields := strings.Fields(lines[1])
			if len(fields) >= 4 {
				totalKB, _ := strconv.ParseInt(fields[1], 10, 64)
				freeKB, _ := strconv.ParseInt(fields[3], 10, 64)
				r.DiskTotalGB = totalKB / (1024 * 1024)
				r.DiskFreeGB = freeKB / (1024 * 1024)
			}
		}
	}

	// GPU (best-effort)
	var gpuCmd string
	if osName == "darwin" {
		gpuCmd = `system_profiler SPDisplaysDataType 2>/dev/null | grep 'Chipset Model:' | sed 's/.*Chipset Model: //'`
	} else {
		gpuCmd = `lspci 2>/dev/null | grep -iE 'vga|3d' | sed 's/.*: //'`
	}
	if out, err := c.Exec.Run(ctx, gpuCmd); err == nil {
		for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
			if line = strings.TrimSpace(line); line != "" {
				r.GPUs = append(r.GPUs, line)
			}
		}
	}

	return r, partial
}

func (c *RemoteCollector) remoteTools(ctx context.Context) []models.ToolInfo {
	toolDefs := defaultToolDefs()
	var tools []models.ToolInfo

	for _, td := range toolDefs {
		pathOut, err := c.Exec.Run(ctx, fmt.Sprintf("command -v %s 2>/dev/null", td.name))
		if err != nil {
			continue
		}
		path := strings.TrimSpace(pathOut)
		if path == "" {
			continue
		}

		ti := models.ToolInfo{
			Name:  td.name,
			Path:  path,
			Class: td.class,
		}

		if td.versionCmd != "" {
			if vOut, err := c.Exec.Run(ctx, td.versionCmd+" 2>/dev/null"); err == nil {
				ti.Version = parseVersionString(vOut)
			}
		}
		tools = append(tools, ti)
	}
	return tools
}
