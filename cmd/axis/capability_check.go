package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/facts"
	"github.com/toasterbook88/axis/internal/models"
)

// capabilityCheckers are Axis's own checkers: the only source of passes that
// count toward promotion. Each runs its probe itself.
var capabilityCheckers = map[string]func(ctx context.Context, node string) capability.Observation{
	"facts.hardware": checkFactsHardware,
	"facts.gpu":      checkFactsGPU,
}

func capabilityCheckerNames() []string {
	names := make([]string, 0, len(capabilityCheckers))
	for name := range capabilityCheckers {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// hardwareReading is one view of this machine's hardware.
type hardwareReading struct {
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Cores       int    `json:"cores"`
	RAMTotalMB  int64  `json:"ram_total_mb"`
	DiskTotalGB int64  `json:"disk_total_gb"`
}

// Seams for tests: the AXIS fact collector under check, and the independent probe.
var (
	capabilityCollectLocal = func(ctx context.Context) (*models.NodeFacts, error) {
		host, _ := os.Hostname()
		return facts.NewLocalCollector(host, "", nil).Collect(ctx)
	}
	capabilityProbeHardware = probeHardware
)

const factsHardwareChecker = "axis capability check facts.hardware"

// checkFactsHardware compares what the AXIS fact collector reports for this
// machine with a reading the probe takes through system calls. The evidence
// hash covers both readings.
func checkFactsHardware(ctx context.Context, node string) capability.Observation {
	if node != "" {
		return checkFactsHardwareNode(ctx, node)
	}
	obs := capability.Observation{Capability: "facts.hardware", Checker: factsHardwareChecker, Outcome: capability.OutcomeInconclusive}
	probe, ref, probeErr := capabilityProbeHardware()
	obs.Evidence = capability.Evidence{Kind: capability.EvidenceLiveProbe, Ref: ref}
	if probeErr != nil && probe == (hardwareReading{}) {
		obs.Claim = capabilityClaim("the independent probe failed: " + probeErr.Error())
		return obs
	}
	nf, err := capabilityCollectLocal(ctx)
	if err != nil || nf == nil || nf.Resources == nil {
		why := "it returned no resources"
		if err != nil {
			why = err.Error()
		}
		obs.Claim = capabilityClaim("the AXIS fact collector failed: " + why)
		return obs
	}
	obs.Subject = localSubject(ctx, nf.Hostname)
	axis := hardwareReading{
		OS: strings.ToLower(nf.OS), Arch: normalizeArch(nf.Arch), Cores: nf.Resources.CPUCores,
		RAMTotalMB: nf.Resources.RAMTotalMB, DiskTotalGB: nf.Resources.DiskTotalGB,
	}
	// A probe that stopped early still took some readings: a mismatch among
	// them is a failure; otherwise the result is inconclusive.
	obs = judgeHardware(obs, axis, probe)
	if probeErr != nil && obs.Outcome != capability.OutcomeFail {
		obs.Outcome = capability.OutcomeInconclusive
		obs.Claim = capabilityClaim(obs.Claim + "; the independent probe stopped early: " + probeErr.Error())
	}
	return obs
}

// judgeHardware decides facts.hardware from the two readings. The evidence
// hash covers both.
func judgeHardware(obs capability.Observation, axis, probe hardwareReading) capability.Observation {
	digest, _ := json.Marshal(map[string]hardwareReading{"axis": axis, "probe": probe})
	sum := sha256.Sum256(digest)
	obs.Evidence.SHA256 = hex.EncodeToString(sum[:])

	mismatched, unobserved, unmeasured := compareHardware(axis, probe)
	switch {
	case len(mismatched) > 0:
		obs.Outcome = capability.OutcomeFail
		obs.Claim = capabilityClaim("AXIS facts disagree with the probe: " + strings.Join(mismatched, "; "))
	case len(unobserved) > 0 || len(unmeasured) > 0:
		obs.Outcome = capability.OutcomeInconclusive
		var parts []string
		if len(unobserved) > 0 {
			parts = append(parts, "AXIS facts did not observe "+strings.Join(unobserved, ", "))
		}
		if len(unmeasured) > 0 {
			parts = append(parts, "the probe did not measure "+strings.Join(unmeasured, ", "))
		}
		obs.Claim = capabilityClaim(strings.Join(parts, "; "))
	default:
		obs.Outcome = capability.OutcomePass
		obs.Claim = fmt.Sprintf("AXIS facts match the probe: os=%s arch=%s cores=%d ram_total_mb=%d disk_total_gb=%d",
			axis.OS, axis.Arch, axis.Cores, axis.RAMTotalMB, axis.DiskTotalGB)
	}
	return obs
}

// compareHardware lists the fields where AXIS and the probe disagree, and the
// fields AXIS did not observe. Totals may differ by rounding: RAM by 1% or
// 64 MB, disk by 2% or 2 GB, whichever is larger.
func compareHardware(axis, probe hardwareReading) (mismatched, unobserved, unmeasured []string) {
	text := func(name, a, p string) {
		switch {
		case a == "":
			unobserved = append(unobserved, name)
		case p == "":
			unmeasured = append(unmeasured, name)
		case a != p:
			mismatched = append(mismatched, fmt.Sprintf("%s axis=%s probe=%s", name, a, p))
		}
	}
	num := func(name string, a, p, tolerance int64) {
		diff := a - p
		if diff < 0 {
			diff = -diff
		}
		switch {
		case a <= 0:
			unobserved = append(unobserved, name)
		case p <= 0:
			unmeasured = append(unmeasured, name)
		case diff > tolerance:
			mismatched = append(mismatched, fmt.Sprintf("%s axis=%d probe=%d", name, a, p))
		}
	}
	text("os", axis.OS, probe.OS)
	text("arch", axis.Arch, probe.Arch)
	num("cores", int64(axis.Cores), int64(probe.Cores), 0)
	num("ram_total_mb", axis.RAMTotalMB, probe.RAMTotalMB, max(64, probe.RAMTotalMB/100))
	num("disk_total_gb", axis.DiskTotalGB, probe.DiskTotalGB, max(2, probe.DiskTotalGB/50))
	return mismatched, unobserved, unmeasured
}

func normalizeArch(arch string) string {
	arch = strings.ToLower(strings.TrimSpace(arch))
	switch arch {
	case "x86_64", "amd64":
		return "amd64"
	case "aarch64", "arm64":
		return "arm64"
	}
	return arch
}

// localSubject names this machine the way --node checks do: the snapshot name
// of the node whose recorded hostname is this machine's, so a failure found
// locally and a pass found with --node meet on one subject. It falls back to
// the OS hostname when the snapshot is unavailable or lists no such node.
func localSubject(ctx context.Context, hostname string) string {
	host := shortHostname(hostname)
	if nodes, err := capabilitySnapshotNodes(ctx); err == nil {
		for _, n := range nodes {
			if host != "" && (shortHostname(n.Hostname) == host || shortHostname(n.Name) == host) {
				return nodeSubject(n.Name)
			}
		}
	}
	if host == "" {
		return ""
	}
	return "node:" + host
}

func shortHostname(host string) string {
	host = strings.ToLower(strings.TrimSpace(host))
	if i := strings.IndexByte(host, '.'); i > 0 {
		host = host[:i]
	}
	return host
}

func capabilityClaim(s string) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > 480 {
		s = s[:480]
	}
	return s
}
