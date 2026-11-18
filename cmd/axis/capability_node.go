package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/toasterbook88/axis/internal/api"
	"github.com/toasterbook88/axis/internal/capability"
	"github.com/toasterbook88/axis/internal/config"
	"github.com/toasterbook88/axis/internal/daemon"
	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/transport"
)

// nodeProbeScript reads a machine's hardware with plain shell tools, for the
// --node checks. It prints key=value lines and changes nothing.
const nodeProbeScript = `os=$(uname -s); echo "os=$os"; echo "arch=$(uname -m)"
if [ "$os" = Linux ]; then
  echo "cores=$(getconf _NPROCESSORS_ONLN)"
  awk '/^MemTotal:/{print "ram_total_kb=" $2}' /proc/meminfo
  df -Pk / | awk 'NR==2{print "disk_total_kb=" $2}'
  if [ -d /proc/driver/nvidia/gpus ]; then
    echo "nvidia_driver=present"
    for f in /proc/driver/nvidia/gpus/*/information; do
      [ -e "$f" ] || continue
      if [ -r "$f" ]; then
        awk -F: '/^Model:/{sub(/^[ \t]+/, "", $2); print "nvidia_gpu=" $2}' "$f"
      else
        echo "nvidia_unreadable=1"
      fi
    done
  else
    echo "nvidia_driver=absent"
  fi
else
  echo "cores=$(sysctl -n hw.ncpu)"
  echo "ram_total_bytes=$(sysctl -n hw.memsize)"
  df -Pk / | awk 'NR==2{print "disk_total_kb=" $2}'
  echo "nvidia_driver=absent"
fi`

// nodeReading is what the probe script saw on one machine.
type nodeReading struct {
	Hardware     hardwareReading `json:"hardware"`
	NVIDIADriver string          `json:"nvidia_driver"`
	NVIDIAGPUs   []string        `json:"nvidia_gpus"`
	// NVIDIAUnreadable is set when the driver lists a GPU whose details this
	// user cannot read, so the GPU count is not known.
	NVIDIAUnreadable bool `json:"nvidia_unreadable,omitempty"`
}

func parseNodeProbe(out string) (nodeReading, error) {
	var r nodeReading
	for _, line := range strings.Split(out, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok {
			continue
		}
		value = strings.TrimSpace(value)
		n, _ := strconv.ParseInt(value, 10, 64)
		switch key {
		case "os":
			r.Hardware.OS = strings.ToLower(value)
		case "arch":
			r.Hardware.Arch = normalizeArch(value)
		case "cores":
			r.Hardware.Cores = int(n)
		case "ram_total_kb":
			r.Hardware.RAMTotalMB = n / 1024
		case "ram_total_bytes":
			r.Hardware.RAMTotalMB = n >> 20
		case "disk_total_kb":
			r.Hardware.DiskTotalGB = n >> 20
		case "nvidia_driver":
			r.NVIDIADriver = value
		case "nvidia_gpu":
			if value != "" {
				r.NVIDIAGPUs = append(r.NVIDIAGPUs, value)
			}
		case "nvidia_unreadable":
			r.NVIDIAUnreadable = true
		}
	}
	if r.Hardware.OS == "" || r.NVIDIADriver == "" {
		return r, errors.New("the probe printed nothing usable")
	}
	return r, nil
}

// Seams for tests: Axis's published facts for a node, the node list, and the
// probe run on a node ("" is this machine).
var (
	capabilitySnapshotNodes = func(ctx context.Context) ([]models.NodeFacts, error) {
		snap, _, err := daemon.FetchSnapshot(ctx, api.DefaultAddr())
		if err != nil {
			return nil, fmt.Errorf("reading the AXIS snapshot: %w", err)
		}
		return snap.Nodes, nil
	}
	capabilityRunProbe = runNodeProbe
)

func capabilityNodeFacts(ctx context.Context, name string) (*models.NodeFacts, error) {
	nodes, err := capabilitySnapshotNodes(ctx)
	if err != nil {
		return nil, err
	}
	for i := range nodes {
		if strings.EqualFold(nodes[i].Name, name) {
			return &nodes[i], nil
		}
	}
	return nil, fmt.Errorf("node %q is not in the AXIS snapshot", name)
}

// probeRunner is the part of an AXIS executor the probe needs; the local and
// SSH executors both provide it.
type probeRunner interface {
	Connect(ctx context.Context) error
	Run(ctx context.Context, cmd string) (string, error)
	Close() error
}

// runNodeProbe runs nodeProbeScript on a node through Axis's own transport:
// SSH for another machine, a local shell for this one or for "".
func runNodeProbe(ctx context.Context, name string) (string, string, error) {
	var ex probeRunner = transport.NewLocalExecutor()
	ref := "this machine: uname, getconf or sysctl, /proc/meminfo, df, /proc/driver/nvidia"
	if name != "" {
		cfg, err := config.Load(config.DefaultConfigPath())
		if err != nil {
			return "", "", fmt.Errorf("loading the AXIS config: %w", err)
		}
		node, ok := cfg.FindNode(name)
		if !ok {
			return "", "", fmt.Errorf("node %q is not in the AXIS config", name)
		}
		ex, ref = probeRunnerFor(ctx, node)
	}
	if err := ex.Connect(ctx); err != nil {
		return "", ref, fmt.Errorf("connecting: %w", err)
	}
	defer ex.Close()
	out, err := ex.Run(ctx, nodeProbeScript)
	if err != nil {
		return out, ref, fmt.Errorf("running the probe: %w", err)
	}
	return out, ref, nil
}

// nodeSubject is the one spelling of a machine in the record, so a failure on it
// can only be closed by a pass on the same machine.
func nodeSubject(name string) string { return "node:" + strings.ToLower(strings.TrimSpace(name)) }

// probeRunnerFor picks how to run the probe on node: a local shell when node is
// this machine, by its config name or hostname or by the hostname the snapshot
// recorded for it (an OS hostname can match neither config field), else AXIS SSH.
func probeRunnerFor(ctx context.Context, node config.NodeConfig) (probeRunner, string) {
	local := capabilityNodeIsLocal(node)
	if !local {
		if host, err := os.Hostname(); err == nil && host != "" {
			if nodes, err := capabilitySnapshotNodes(ctx); err == nil {
				for _, n := range nodes {
					if strings.EqualFold(n.Name, node.Name) && shortHostname(n.Hostname) == shortHostname(host) {
						local = true
					}
				}
			}
		}
	}
	if local {
		return transport.NewLocalExecutor(), "this machine: uname, getconf or sysctl, /proc/meminfo, df, /proc/driver/nvidia"
	}
	spec := node.SSHDialSpec()
	return transport.NewSSHExecutorFromDial(spec.Host, spec.Port, spec.User, spec.DialTimeoutSec, spec.Fallbacks),
		"ssh to " + node.Name + ": uname, getconf or sysctl, /proc/meminfo, df, /proc/driver/nvidia"
}

func capabilityNodeIsLocal(node config.NodeConfig) bool {
	host, err := os.Hostname()
	if err != nil || host == "" {
		return false
	}
	host = shortHostname(host)
	return host == shortHostname(node.Name) || host == shortHostname(node.Hostname)
}

// capabilityTargets turns --node, --all-nodes and --except into the list of
// machines to check, "" meaning this machine.
func capabilityTargets(ctx context.Context, nodes []string, all bool, except []string) ([]string, error) {
	if !all && len(nodes) == 0 {
		return []string{""}, nil
	}
	skip := map[string]bool{}
	for _, n := range except {
		skip[strings.ToLower(strings.TrimSpace(n))] = true
	}
	var out []string
	if all {
		snapshot, err := capabilitySnapshotNodes(ctx)
		if err != nil {
			return nil, err
		}
		for _, n := range snapshot {
			out = append(out, n.Name)
		}
	}
	out = append(out, nodes...)
	seen := map[string]bool{}
	var targets []string
	for _, n := range out {
		key := strings.ToLower(strings.TrimSpace(n))
		if key == "" || skip[key] || seen[key] {
			continue
		}
		seen[key] = true
		targets = append(targets, strings.TrimSpace(n))
	}
	sort.Strings(targets)
	if len(targets) == 0 {
		return nil, errors.New("no machines left to check")
	}
	return targets, nil
}

// checkFactsHardwareNode compares Axis's published facts for a node with the
// probe script run on it.
func checkFactsHardwareNode(ctx context.Context, name string) capability.Observation {
	obs := capability.Observation{Capability: "facts.hardware", Checker: factsHardwareChecker + " --node",
		Outcome: capability.OutcomeInconclusive, Subject: nodeSubject(name)}
	nf, err := capabilityNodeFacts(ctx, name)
	if err != nil {
		obs.Claim = capabilityClaim("AXIS has no facts for it: " + err.Error())
		return obs
	}
	name, obs.Subject = nf.Name, nodeSubject(nf.Name)
	if nf.Status == models.StatusUnreachable || nf.Resources == nil {
		obs.Claim = capabilityClaim(fmt.Sprintf("AXIS reports it %s; there are no facts to check", nf.Status))
		return obs
	}
	out, ref, err := capabilityRunProbe(ctx, name)
	obs.Evidence = capability.Evidence{Kind: capability.EvidenceLiveProbe, Ref: ref}
	if err != nil {
		obs.Claim = capabilityClaim("the probe could not run: " + err.Error())
		return obs
	}
	r, err := parseNodeProbe(out)
	if err != nil {
		obs.Claim = capabilityClaim(err.Error())
		return obs
	}
	axis := hardwareReading{OS: strings.ToLower(nf.OS), Arch: normalizeArch(nf.Arch), Cores: nf.Resources.CPUCores,
		RAMTotalMB: nf.Resources.RAMTotalMB, DiskTotalGB: nf.Resources.DiskTotalGB}
	return judgeHardware(obs, axis, r.Hardware)
}

const factsGPUChecker = "axis capability check facts.gpu"

// checkFactsGPU compares the GPUs Axis lists for a machine with what the NVIDIA
// driver on it shows, and refuses any GPU entry that is an error message.
func checkFactsGPU(ctx context.Context, name string) capability.Observation {
	obs := capability.Observation{Capability: "facts.gpu", Checker: factsGPUChecker, Outcome: capability.OutcomeInconclusive}
	var gpus []models.GPUInfo
	if name == "" {
		nf, err := capabilityCollectLocal(ctx)
		if err != nil || nf == nil || nf.Resources == nil {
			obs.Claim = "the AXIS fact collector returned no resources"
			if err != nil {
				obs.Claim = capabilityClaim("the AXIS fact collector failed: " + err.Error())
			}
			return obs
		}
		obs.Subject = localSubject(ctx, nf.Hostname)
		gpus = nf.Resources.GPUs
	} else {
		obs.Checker += " --node"
		obs.Subject = nodeSubject(name)
		nf, err := capabilityNodeFacts(ctx, name)
		if err != nil {
			obs.Claim = capabilityClaim("AXIS has no facts for it: " + err.Error())
			return obs
		}
		name, obs.Subject = nf.Name, nodeSubject(nf.Name)
		if nf.Status == models.StatusUnreachable || nf.Resources == nil {
			obs.Claim = capabilityClaim(fmt.Sprintf("AXIS reports it %s; there are no facts to check", nf.Status))
			return obs
		}
		gpus = nf.Resources.GPUs
	}
	out, ref, err := capabilityRunProbe(ctx, name)
	obs.Evidence = capability.Evidence{Kind: capability.EvidenceLiveProbe, Ref: ref}
	if err != nil {
		obs.Claim = capabilityClaim("the probe could not run: " + err.Error())
		return obs
	}
	r, err := parseNodeProbe(out)
	if err != nil {
		obs.Claim = capabilityClaim(err.Error())
		return obs
	}
	return judgeGPU(obs, gpus, r)
}

// judgeGPU decides facts.gpu from Axis's GPU list and the probe's reading.
func judgeGPU(obs capability.Observation, gpus []models.GPUInfo, r nodeReading) capability.Observation {
	var nvidia, errorText []string
	for _, g := range gpus {
		model := strings.TrimSpace(g.Model)
		switch {
		case gpuModelIsAnError(model):
			errorText = append(errorText, model)
		case strings.EqualFold(g.Vendor, "nvidia"):
			nvidia = append(nvidia, model)
		}
	}
	digest, _ := json.Marshal(map[string]any{"axis": gpus, "probe": r})
	sum := sha256.Sum256(digest)
	obs.Evidence.SHA256 = hex.EncodeToString(sum[:])
	listed := len(nvidia) + len(errorText)
	switch {
	case len(errorText) > 0:
		obs.Outcome = capability.OutcomeFail
		obs.Claim = capabilityClaim(fmt.Sprintf("AXIS lists an error message as a GPU: %q", errorText[0]))
	case r.NVIDIADriver == "present" && r.NVIDIAUnreadable:
		obs.Outcome = capability.OutcomeInconclusive
		obs.Claim = "the NVIDIA driver lists a GPU this user cannot read, so the GPU count is not known"
	case r.NVIDIADriver != "present" && listed > 0:
		obs.Outcome = capability.OutcomeFail
		obs.Claim = capabilityClaim(fmt.Sprintf("AXIS lists %d NVIDIA GPU(s) (%s); the machine has no NVIDIA driver loaded", listed, strings.Join(nvidia, ", ")))
	case r.NVIDIADriver != "present":
		// Agreement on absence is a checked claim: placement trusts it, and a
		// machine that once listed a phantom GPU can only close its failure
		// by being checked clean.
		obs.Outcome = capability.OutcomePass
		obs.Claim = "AXIS lists no NVIDIA GPU, and the machine has no NVIDIA driver loaded"
	case len(nvidia) != len(r.NVIDIAGPUs):
		obs.Outcome = capability.OutcomeFail
		obs.Claim = capabilityClaim(fmt.Sprintf("AXIS lists %d NVIDIA GPU(s), the driver shows %d (%s)", len(nvidia), len(r.NVIDIAGPUs), strings.Join(r.NVIDIAGPUs, ", ")))
	default:
		obs.Outcome = capability.OutcomePass
		obs.Claim = capabilityClaim(fmt.Sprintf("AXIS lists the %d NVIDIA GPU(s) the driver shows: %s", len(nvidia), strings.Join(nvidia, ", ")))
	}
	return obs
}

var gpuErrorWords = []string{"failed", "error", "couldn't", "could not", "unable", "not found", "no devices", "make sure"}

func gpuModelIsAnError(model string) bool {
	m := strings.ToLower(model)
	for _, w := range gpuErrorWords {
		if strings.Contains(m, w) {
			return true
		}
	}
	return false
}
