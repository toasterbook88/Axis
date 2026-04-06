package models

import "strings"

// NodeIdentity carries stable observed identity data when the host exposes it.
// It is additive advisory truth used to make locality and node matching less
// dependent on transient hostnames and IP addresses.
type NodeIdentity struct {
	StableID string `json:"stable_id,omitempty" yaml:"stable_id,omitempty"`
	Source   string `json:"source,omitempty" yaml:"source,omitempty"`
}

// ExecutionOrigin captures the local AXIS runtime identity that initiated an
// execution reservation. It is additive provenance, not an authority channel.
type ExecutionOrigin struct {
	Node     string `json:"node,omitempty" yaml:"node,omitempty"`
	Hostname string `json:"hostname,omitempty" yaml:"hostname,omitempty"`
	StableID string `json:"stable_id,omitempty" yaml:"stable_id,omitempty"`
}

// NormalizeStableID canonicalizes a stable identity token for comparisons.
func NormalizeStableID(id string) string {
	return strings.ToLower(strings.TrimSpace(id))
}

// NewNodeIdentity returns a normalized identity or nil when id is empty.
func NewNodeIdentity(id, source string) *NodeIdentity {
	normalized := NormalizeStableID(id)
	if normalized == "" {
		return nil
	}
	return &NodeIdentity{
		StableID: normalized,
		Source:   strings.TrimSpace(source),
	}
}

// NewExecutionOrigin returns a normalized execution origin.
func NewExecutionOrigin(node, hostname, stableID string) ExecutionOrigin {
	return ExecutionOrigin{
		Node:     strings.TrimSpace(node),
		Hostname: strings.TrimSpace(hostname),
		StableID: NormalizeStableID(stableID),
	}.Normalized()
}

// Normalized canonicalizes execution-origin fields for persistence and
// comparison.
func (o ExecutionOrigin) Normalized() ExecutionOrigin {
	o.Node = strings.TrimSpace(o.Node)
	o.Hostname = strings.TrimSpace(o.Hostname)
	o.StableID = NormalizeStableID(o.StableID)
	return o
}

// IsZero reports whether the execution origin has any usable identity signal.
func (o ExecutionOrigin) IsZero() bool {
	o = o.Normalized()
	return o.Node == "" && o.Hostname == "" && o.StableID == ""
}

// ExecutionOriginFromNode derives an execution origin from observed node facts.
func ExecutionOriginFromNode(n NodeFacts) ExecutionOrigin {
	stableID := ""
	if n.Identity != nil {
		stableID = n.Identity.StableID
	}
	return NewExecutionOrigin(n.Name, n.Hostname, stableID)
}

// ParseDarwinPlatformUUID extracts a platform UUID from either ioreg or
// system_profiler output.
func ParseDarwinPlatformUUID(out string) string {
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}

		if strings.Contains(trimmed, "IOPlatformUUID") {
			if parts := strings.Split(trimmed, `"`); len(parts) >= 4 {
				return NormalizeStableID(parts[3])
			}
			if idx := strings.LastIndex(trimmed, "="); idx >= 0 {
				return NormalizeStableID(strings.Trim(strings.TrimSpace(trimmed[idx+1:]), `"`))
			}
		}

		if strings.HasPrefix(strings.ToLower(trimmed), "hardware uuid:") {
			return NormalizeStableID(strings.TrimSpace(trimmed[len("Hardware UUID:"):]))
		}
	}
	return ""
}
