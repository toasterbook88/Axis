package facts

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

// RemoteCollector collects facts from a remote node via SSH.
// The Executor interface keeps the collection path decoupled from the current
// transport implementation.
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
//
// Collection strategy:
//  1. Force bash for all remote probes (avoids non-POSIX login shells for scripts).
//  2. Run a one-shot fact bundle (one SSH session worth of shell work).
//  3. Always run best-effort AI resident discovery (ollama/llama/mlx) + TurboQuant.
func (c *RemoteCollector) Collect(ctx context.Context) (*models.NodeFacts, error) {
	facts := &models.NodeFacts{
		Name: c.NodeName,
		Role: c.Role,
		// SSHTarget is the configured dial address. Hostname is overwritten below
		// with the observed machine hostname after connect; classification must
		// keep using SSHTarget for the route in use.
		SSHTarget:   c.Hostname,
		Hostname:    c.Hostname, // fallback until observed hostname is collected
		Status:      models.StatusComplete,
		CollectedAt: time.Now().UTC(),
	}

	// Wrap executor so every Run uses bash --noprofile --norc.
	c.Exec = withBashForced(c.Exec)

	if err := c.Exec.Connect(ctx); err != nil {
		facts.Status = models.StatusUnreachable
		facts.Error = err.Error()
		return facts, nil
	}
	defer c.Exec.Close()

	if exposer, ok := c.Exec.(interface{ HandshakeLatencyMs() int64 }); ok {
		facts.SSHHandshakeLatencyMs = exposer.HandshakeLatencyMs()
	}
	// Prefer the host that actually connected (may be an endpoint fallback).
	if ch, ok := c.Exec.(interface{ ConnectedHost() string }); ok {
		if host := strings.TrimSpace(ch.ConnectedHost()); host != "" {
			facts.SSHTarget = host
		}
	}

	// Single remote bash script for core facts. Bundle failure is recorded as
	// partial rather than falling back to the old multi-Run collector.
	if !c.tryBundleCollect(ctx, facts) {
		facts.Status = models.StatusPartial
		facts.PartialReasons = append(facts.PartialReasons, models.PartialReason{
			Probe:   "fact_bundle",
			Message: "remote fact bundle failed or returned unparseable output",
		})
	}

	// Best-effort AI discovery (same as before; runs under bash-forced executor).
	// When the bundle path already found tools, merge rather than replace.
	ollamaInfo, residentModels := c.discoverOllamaRobust(ctx)
	if ollamaInfo.Installed {
		facts.Ollama = &ollamaInfo
		facts.ResidentModels = append(facts.ResidentModels[:0], residentModels...)
		facts.Tools = appendToolUnique(facts.Tools, models.ToolInfo{
			Name:    "ollama",
			Path:    ollamaInfo.Path,
			Version: ollamaInfo.Version,
			Class:   models.ToolClassAICLI,
		})
	}
	if llamaResidents := c.discoverLlamaServerRobust(ctx); len(llamaResidents) > 0 {
		facts.ResidentModels = append(facts.ResidentModels, llamaResidents...)
	}
	if mlxResidents := c.discoverMLXRobust(ctx); len(mlxResidents) > 0 {
		facts.ResidentModels = append(facts.ResidentModels, mlxResidents...)
	}
	c.discoverDiskWeights(ctx, facts)
	facts.TurboQuant = detectTurboQuantSupport(ctx, facts.OS, facts.Arch, facts.Tools, facts.Resources, facts.Ollama, func(ctx context.Context, cmd string) (string, error) {
		return c.Exec.Run(ctx, cmd)
	})

	if len(facts.PartialReasons) > 0 && facts.Status == models.StatusComplete {
		facts.Status = models.StatusPartial
	}
	if facts.Status == models.StatusPartial && facts.Error == "" && len(facts.PartialReasons) > 0 {
		facts.Error = models.FormatPartialReasons(facts.PartialReasons)
	}
	facts.PopulateMemoryMetrics()
	return facts, nil
}

func appendToolUnique(tools []models.ToolInfo, add models.ToolInfo) []models.ToolInfo {
	for _, t := range tools {
		if t.Name == add.Name {
			return tools
		}
	}
	return append(tools, add)
}

func (c *RemoteCollector) discoverDiskWeights(ctx context.Context, facts *models.NodeFacts) {
	if facts == nil || c.Exec == nil {
		return
	}
	out, err := c.Exec.Run(ctx, DiskWeightsDiscoveryScript)
	if err != nil {
		return
	}
	res := parseDiskWeightsJSON(out)
	facts.DiskWeights = res.Weights
	facts.DiskWeightsTruncated = res.Truncated
}

func (c *RemoteCollector) discoverOllamaRobust(ctx context.Context) (models.OllamaInfo, []models.ResidentModel) {
	info := models.OllamaInfo{Installed: false}

	out, err := c.Exec.Run(ctx, OllamaDiscoveryScript)
	if err != nil {
		info.Error = err.Error()
		return info, nil
	}

	// parse the JSON blob
	var parsed ollamaDiscoveryPayload
	if json.Unmarshal([]byte(out), &parsed) == nil {
		return parsed.OllamaInfo, parsed.ResidentModels
	}
	return info, nil
}

// discoverLlamaServerRobust probes for a running llama-server process on the
// remote node via a single SSH command and returns its resident models.
func (c *RemoteCollector) discoverLlamaServerRobust(ctx context.Context) []models.ResidentModel {
	out, err := c.Exec.Run(ctx, LlamaServerDiscoveryScript)
	if err != nil {
		return nil
	}
	var parsed llamaServerDiscoveryPayload
	if json.Unmarshal([]byte(out), &parsed) == nil && parsed.Installed {
		return withResidentPort(parsed.ResidentModels, parsed.Port)
	}
	return nil
}

// discoverMLXRobust probes for a running mlx_lm.server process on the remote
// node via a single SSH command and queries its /v1/models endpoint to
// enumerate resident models.
func (c *RemoteCollector) discoverMLXRobust(ctx context.Context) []models.ResidentModel {
	out, err := c.Exec.Run(ctx, MLXDiscoveryScript)
	if err != nil {
		return nil
	}
	var parsed mlxDiscoveryPayload
	if json.Unmarshal([]byte(out), &parsed) == nil && parsed.Installed {
		return withResidentPort(parsed.ResidentModels, parsed.Port)
	}
	return nil
}

func (c *RemoteCollector) remoteStorageClass(ctx context.Context, osName string) string {
	switch strings.ToLower(strings.TrimSpace(osName)) {
	case "darwin":
		out, err := c.Exec.Run(ctx, "diskutil info / 2>/dev/null")
		if err != nil {
			return "unknown"
		}
		return parseDiskutilStorageClass(out)
	case "linux":
		out, err := c.Exec.Run(ctx, `findmnt -n -o SOURCE / 2>/dev/null`)
		if err != nil {
			return "unknown"
		}
		return resolveLinuxStorageClass(
			strings.TrimSpace(out),
			func(device string) (linuxBlockDeviceInfo, error) {
				return c.remoteLinuxBlockDeviceInfo(ctx, device)
			},
			func(info linuxBlockDeviceInfo) ([]string, error) {
				return c.remoteLinuxBlockDeviceSlaves(ctx, info)
			},
			func(device string) (string, error) {
				return c.remoteLinuxRotational(ctx, device)
			},
		)
	default:
		return "unknown"
	}
}

func (c *RemoteCollector) remoteLinuxBlockDeviceInfo(ctx context.Context, device string) (linuxBlockDeviceInfo, error) {
	out, err := c.Exec.Run(ctx, fmt.Sprintf("lsblk -J -n -p -o NAME,KNAME,PKNAME,TYPE,ROTA %q 2>/dev/null", strings.TrimSpace(device)))
	if err != nil {
		return linuxBlockDeviceInfo{}, err
	}
	return parseLinuxBlockDeviceInfo(out)
}

func (c *RemoteCollector) remoteLinuxBlockDeviceSlaves(ctx context.Context, info linuxBlockDeviceInfo) ([]string, error) {
	sysfsName := linuxSysfsBlockName(info)
	if sysfsName == "" {
		return nil, fmt.Errorf("no sysfs block name for %+v", info)
	}
	out, err := c.Exec.Run(ctx, fmt.Sprintf("ls -1 /sys/class/block/%s/slaves 2>/dev/null", sysfsName))
	if err != nil {
		return nil, err
	}

	var parents []string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		name := filepath.Base(strings.TrimSpace(line))
		if name == "" {
			continue
		}
		parents = append(parents, filepath.Join("/dev", name))
	}
	sort.Strings(parents)
	return parents, nil
}

func (c *RemoteCollector) remoteLinuxRotational(ctx context.Context, device string) (string, error) {
	base := fallbackLinuxBlockBase(device)
	if base == "" {
		return "", fmt.Errorf("no block device base for %q", device)
	}
	return c.Exec.Run(ctx, fmt.Sprintf("cat /sys/block/%s/queue/rotational 2>/dev/null", base))
}

// parseRemoteAddrLine parses an address line from `ip -o addr` or fallback output.
func parseRemoteAddrLine(line string) models.NetworkAddress {
	fields := strings.Fields(line)

	var addrField, ifName string
	for i, f := range fields {
		if f == "inet" || f == "inet6" {
			if i+1 < len(fields) {
				addrField = fields[i+1]
			}
			if i >= 2 {
				ifName = strings.TrimSuffix(fields[1], ":")
			}
			break
		}
	}

	if addrField == "" {
		switch len(fields) {
		case 1:
			addrField = fields[0]
		case 2:
			ifName = fields[0]
			addrField = fields[1]
		}
	}

	ip, subnet := parseAddressWithOptionalCIDR(addrField)
	if ip == nil {
		return models.NetworkAddress{}
	}

	kind := "ipv4"
	if ip.To4() == nil {
		kind = "ipv6"
	}
	return models.NetworkAddress{
		Kind:       kind,
		Address:    ip.String(),
		Interface:  ifName,
		Subnet:     subnet,
		SpeedClass: classifyInterfaceSpeed(ifName, ip),
	}
}
