package modelplan

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/toasterbook88/axis/internal/models"
	"github.com/toasterbook88/axis/internal/placement"
	"github.com/toasterbook88/axis/internal/state"
)

const (
	defaultMaxSnapshotAge = 5 * time.Minute
	defaultMaxLinkAge     = 5 * time.Minute
	defaultMaxNodes       = 8
	allowedClockSkew      = 30 * time.Second
	mib                   = int64(1024 * 1024)
	floatEpsilon          = 1e-9
)

// Planner turns observed cluster state into a deterministic advisory model
// placement. The zero value is usable and applies the default freshness limits.
type Planner struct {
	Now            func() time.Time
	MaxSnapshotAge time.Duration
	MaxLinkAge     time.Duration
}

// NewPlanner returns a planner with the default freshness limits.
func NewPlanner() Planner {
	return Planner{
		Now:            time.Now,
		MaxSnapshotAge: defaultMaxSnapshotAge,
		MaxLinkAge:     defaultMaxLinkAge,
	}
}

// Plan builds a minimum-node contiguous pipeline plan. Existing AXIS placement
// admission remains authoritative for node eligibility; this package adds exact
// per-shard memory fitting and fresh pairwise-link requirements.
func (p Planner) Plan(
	snapshot *models.ClusterSnapshot,
	st *state.ClusterState,
	manifest ModelManifest,
	links []LinkObservation,
	opts PlanOptions,
) (DistributedPlacementPlan, error) {
	now := p.currentTime()
	if err := p.validateSnapshot(snapshot, now); err != nil {
		return DistributedPlacementPlan{}, err
	}

	normalizedManifest, layerMemory, totalLayerMemory, minLayerMemory, err := normalizeManifest(manifest)
	if err != nil {
		return DistributedPlacementPlan{}, err
	}
	if err := validateOptions(opts); err != nil {
		return DistributedPlacementPlan{}, err
	}

	reqs := models.TaskRequirements{
		Description:   fmt.Sprintf("distributed model plan for %s", normalizedManifest.Name),
		Workload:      models.WorkloadProfileMatch{Class: models.ClassLocalLLMInference},
		RequiredTools: append([]string(nil), normalizedManifest.RequiredTools...),
		MinFreeRAMMB:  normalizedManifest.PerNodeOverheadMB + minLayerMemory,
	}
	explanation := placement.ExplainPlacement(reqs, snapshot.Nodes, st)
	excluded := convertExclusions(explanation.Excluded)
	candidates, capacityExcluded := eligibleCandidates(snapshot.Nodes, explanation.Eligible, normalizedManifest.PerNodeOverheadMB, minLayerMemory)
	excluded = append(excluded, capacityExcluded...)
	sortExclusions(excluded)

	if len(candidates) == 0 {
		return DistributedPlacementPlan{}, fmt.Errorf("model plan: no placement-eligible nodes have enough allocatable memory for overhead plus one layer")
	}

	maxNodes := opts.MaxNodes
	if maxNodes <= 0 {
		maxNodes = defaultMaxNodes
	}
	if maxNodes > len(candidates) {
		maxNodes = len(candidates)
	}
	if maxNodes > normalizedManifest.TotalLayers {
		maxNodes = normalizedManifest.TotalLayers
	}
	if maxNodes <= 0 {
		return DistributedPlacementPlan{}, fmt.Errorf("model plan: max_nodes leaves no usable planning nodes")
	}

	coordinator := strings.TrimSpace(opts.CoordinatorNode)
	if coordinator != "" && !containsCandidate(candidates, coordinator) {
		return DistributedPlacementPlan{}, fmt.Errorf("model plan: coordinator node %q is not placement-eligible", coordinator)
	}

	linkIndex := p.indexLinks(links, now, opts)
	best, ok := choosePlan(candidates, layerMemory, totalLayerMemory, maxNodes, coordinator, linkIndex)
	if !ok {
		totalUsable := int64(0)
		for _, candidate := range candidates {
			totalUsable += candidate.usableMemoryMB
		}
		if totalUsable < totalLayerMemory {
			return DistributedPlacementPlan{}, fmt.Errorf(
				"model plan: insufficient aggregate memory: need %dMB of layer memory, have %dMB usable across %d eligible nodes",
				totalLayerMemory, totalUsable, len(candidates),
			)
		}
		return DistributedPlacementPlan{}, fmt.Errorf(
			"model plan: no feasible contiguous plan within %d nodes; multi-node plans require fresh directional topology measurements satisfying the configured link constraints",
			maxNodes,
		)
	}

	status, warnings := planConfidence(snapshot)
	strategy := StrategyPipeline
	if len(best.shards) == 1 {
		strategy = StrategySingleNode
	}
	coordinator = best.shards[0].Node
	overheadTotal, err := checkedMul(int64(len(best.shards)), normalizedManifest.PerNodeOverheadMB)
	if err != nil {
		return DistributedPlacementPlan{}, fmt.Errorf("model plan: estimated overhead overflow: %w", err)
	}
	estimatedTotal, err := checkedAdd(totalLayerMemory, overheadTotal)
	if err != nil {
		return DistributedPlacementPlan{}, fmt.Errorf("model plan: estimated total memory overflow: %w", err)
	}

	plan := DistributedPlacementPlan{
		Status:                 status,
		Strategy:               strategy,
		Model:                  normalizedManifest.Name,
		Runtime:                normalizedManifest.Runtime,
		Quantization:           normalizedManifest.Quantization,
		SnapshotTimestamp:      snapshot.Timestamp.UTC(),
		PlannedAt:              now,
		CoordinatorNode:        coordinator,
		TotalLayers:            normalizedManifest.TotalLayers,
		EstimatedLayerMemoryMB: totalLayerMemory,
		EstimatedTotalMemoryMB: estimatedTotal,
		Shards:                 best.shards,
		Links:                  best.links,
		Excluded:               excluded,
		Warnings:               warnings,
		Reasoning: []string{
			fmt.Sprintf("used snapshot collected at %s", snapshot.Timestamp.UTC().Format(time.RFC3339)),
			fmt.Sprintf("%d of %d nodes passed existing AXIS placement admission", len(candidates), len(snapshot.Nodes)),
			fmt.Sprintf("selected the minimum feasible node count: %d", len(best.shards)),
			"assigned exact contiguous half-open layer ranges without overlap",
			"plan is advisory only; no runtime was launched and no reservation was mutated",
		},
	}
	if len(best.links) > 0 {
		plan.Reasoning = append(plan.Reasoning,
			fmt.Sprintf("validated %d adjacent pipeline hops against fresh directional measurements", len(best.links)))
	}
	return plan, nil
}

func (p Planner) currentTime() time.Time {
	if p.Now == nil {
		return time.Now().UTC()
	}
	return p.Now().UTC()
}

func (p Planner) validateSnapshot(snapshot *models.ClusterSnapshot, now time.Time) error {
	if snapshot == nil {
		return fmt.Errorf("model plan: snapshot is required")
	}
	if snapshot.Timestamp.IsZero() {
		return fmt.Errorf("model plan: snapshot timestamp is required")
	}
	age := now.Sub(snapshot.Timestamp)
	if age < -allowedClockSkew {
		return fmt.Errorf("model plan: snapshot timestamp is %s in the future", (-age).Round(time.Second))
	}
	if age < 0 {
		age = 0
	}
	maxAge := p.MaxSnapshotAge
	if maxAge <= 0 {
		maxAge = defaultMaxSnapshotAge
	}
	if age > maxAge {
		return fmt.Errorf("model plan: snapshot is stale: age %s exceeds %s", age.Round(time.Second), maxAge)
	}
	return nil
}

func validateOptions(opts PlanOptions) error {
	if opts.MaxNodes < 0 {
		return fmt.Errorf("model plan: max_nodes cannot be negative")
	}
	if opts.MinBandwidthMBps < 0 {
		return fmt.Errorf("model plan: min_bandwidth_mbps cannot be negative")
	}
	if opts.MaxLatencyP95MS < 0 {
		return fmt.Errorf("model plan: max_latency_p95_ms cannot be negative")
	}
	return nil
}

func normalizeManifest(manifest ModelManifest) (ModelManifest, []int64, int64, int64, error) {
	manifest.Name = strings.TrimSpace(manifest.Name)
	manifest.Runtime = strings.TrimSpace(manifest.Runtime)
	manifest.Quantization = strings.TrimSpace(manifest.Quantization)
	manifest.RequiredTools = normalizeStrings(manifest.RequiredTools)
	if manifest.Name == "" {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: manifest name is required")
	}
	if manifest.TotalLayers <= 0 {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: total_layers must be positive")
	}
	if manifest.PerNodeOverheadMB < 0 || manifest.DefaultLayerMemoryMB < 0 || manifest.KVCacheBytesPerTokenPerLayer < 0 {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: manifest memory values cannot be negative")
	}
	if manifest.ContextWindowTokens < 0 || manifest.Concurrency < 0 {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: context_window_tokens and concurrency cannot be negative")
	}
	if manifest.Concurrency == 0 {
		manifest.Concurrency = 1
	}
	if len(manifest.LayerMemoryMB) > 0 && len(manifest.LayerMemoryMB) != manifest.TotalLayers {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf(
			"model plan: layer_memory_mb has %d entries, want total_layers=%d",
			len(manifest.LayerMemoryMB), manifest.TotalLayers,
		)
	}
	if len(manifest.LayerMemoryMB) == 0 && manifest.DefaultLayerMemoryMB <= 0 {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: provide layer_memory_mb or a positive default_layer_memory_mb")
	}

	kvBytes, err := checkedMul(manifest.KVCacheBytesPerTokenPerLayer, int64(manifest.ContextWindowTokens))
	if err != nil {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: KV-cache estimate overflow: %w", err)
	}
	kvBytes, err = checkedMul(kvBytes, int64(manifest.Concurrency))
	if err != nil {
		return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: KV-cache estimate overflow: %w", err)
	}
	kvMB := ceilDiv(kvBytes, mib)

	layers := make([]int64, manifest.TotalLayers)
	total := int64(0)
	minimum := int64(math.MaxInt64)
	for i := range layers {
		base := manifest.DefaultLayerMemoryMB
		if len(manifest.LayerMemoryMB) > 0 {
			base = manifest.LayerMemoryMB[i]
		}
		if base <= 0 {
			return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: layer %d memory must be positive", i)
		}
		layer, err := checkedAdd(base, kvMB)
		if err != nil {
			return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: layer %d memory overflow: %w", i, err)
		}
		layers[i] = layer
		total, err = checkedAdd(total, layer)
		if err != nil {
			return ModelManifest{}, nil, 0, 0, fmt.Errorf("model plan: total layer memory overflow: %w", err)
		}
		if layer < minimum {
			minimum = layer
		}
	}
	manifest.LayerMemoryMB = append([]int64(nil), layers...)
	return manifest, layers, total, minimum, nil
}

func normalizeStrings(values []string) []string {
	seen := make(map[string]string, len(values))
	for _, value := range values {
		trimmed := strings.TrimSpace(value)
		if trimmed == "" {
			continue
		}
		key := strings.ToLower(trimmed)
		if _, exists := seen[key]; !exists {
			seen[key] = trimmed
		}
	}
	out := make([]string, 0, len(seen))
	for _, value := range seen {
		out = append(out, value)
	}
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i]) < strings.ToLower(out[j])
	})
	return out
}

func planConfidence(snapshot *models.ClusterSnapshot) (PlanStatus, []string) {
	status := PlanComplete
	warnings := make([]string, 0, 2)
	if snapshot.Status != models.SnapshotHealthy {
		status = PlanPartial
		warnings = append(warnings, fmt.Sprintf("cluster snapshot status is %s; only complete placement-eligible nodes were used", snapshot.Status))
	}
	if snapshot.Freshness != nil {
		if !snapshot.Freshness.CompletedWindow {
			status = PlanPartial
			warnings = append(warnings, "discovery accumulation window was incomplete")
		}
		if warning := strings.TrimSpace(snapshot.Freshness.Warning); warning != "" {
			status = PlanPartial
			warnings = append(warnings, warning)
		}
	}
	return status, warnings
}

func convertExclusions(in []models.PlacementExclusion) []NodeExclusion {
	out := make([]NodeExclusion, 0, len(in))
	for _, exclusion := range in {
		out = append(out, NodeExclusion{
			Node:    exclusion.Node,
			Reasons: append([]string(nil), exclusion.Reasons...),
		})
	}
	return out
}

func sortExclusions(excluded []NodeExclusion) {
	sort.Slice(excluded, func(i, j int) bool {
		return strings.ToLower(excluded[i].Node) < strings.ToLower(excluded[j].Node)
	})
}

type candidate struct {
	node           models.NodeFacts
	allocatableMB  int64
	usableMemoryMB int64
}

func eligibleCandidates(nodes []models.NodeFacts, eligible []models.PlacementCandidateExplanation, overheadMB, minLayerMB int64) ([]candidate, []NodeExclusion) {
	byName := make(map[string]models.NodeFacts, len(nodes))
	for _, node := range nodes {
		byName[strings.ToLower(strings.TrimSpace(node.Name))] = node
	}

	out := make([]candidate, 0, len(eligible))
	excluded := make([]NodeExclusion, 0)
	for _, admitted := range eligible {
		node, ok := byName[strings.ToLower(strings.TrimSpace(admitted.Node))]
		if !ok {
			continue
		}
		allocatable := effectiveAllocatable(node)
		usable := allocatable - overheadMB
		if usable < minLayerMB {
			excluded = append(excluded, NodeExclusion{
				Node: node.Name,
				Reasons: []string{fmt.Sprintf(
					"insufficient shard capacity after %dMB per-node overhead: %dMB usable, smallest layer requires %dMB",
					overheadMB, maxInt64(usable, 0), minLayerMB,
				)},
			})
			continue
		}
		out = append(out, candidate{node: node, allocatableMB: allocatable, usableMemoryMB: usable})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].usableMemoryMB != out[j].usableMemoryMB {
			return out[i].usableMemoryMB > out[j].usableMemoryMB
		}
		return strings.ToLower(out[i].node.Name) < strings.ToLower(out[j].node.Name)
	})
	return out, excluded
}

func effectiveAllocatable(node models.NodeFacts) int64 {
	if node.RAMAllocatableMB > 0 {
		return node.RAMAllocatableMB
	}
	if node.Resources == nil {
		return 0
	}
	allocatable := node.Resources.RAMAllocatableMB - node.RAMReservedMB
	if allocatable <= 0 {
		allocatable = node.ReservableRAM() - node.RAMReservedMB
	}
	if allocatable < 0 {
		return 0
	}
	return allocatable
}

func containsCandidate(candidates []candidate, name string) bool {
	for _, candidate := range candidates {
		if strings.EqualFold(candidate.node.Name, name) {
			return true
		}
	}
	return false
}

type linkKey struct {
	source      string
	destination string
}

type indexedLinks map[linkKey]LinkObservation

func (p Planner) indexLinks(links []LinkObservation, now time.Time, opts PlanOptions) indexedLinks {
	maxAge := p.MaxLinkAge
	if maxAge <= 0 {
		maxAge = defaultMaxLinkAge
	}
	indexed := make(indexedLinks)
	for _, link := range links {
		link.SourceNode = strings.TrimSpace(link.SourceNode)
		link.DestinationNode = strings.TrimSpace(link.DestinationNode)
		link.Source = strings.TrimSpace(link.Source)
		if link.SourceNode == "" || link.DestinationNode == "" || strings.EqualFold(link.SourceNode, link.DestinationNode) {
			continue
		}
		if link.Source == "" || link.MeasuredAt.IsZero() || link.BandwidthMBps <= 0 || link.LatencyP95MS < 0 {
			continue
		}
		age := now.Sub(link.MeasuredAt)
		if age < -allowedClockSkew || age > maxAge {
			continue
		}
		if !link.ExpiresAt.IsZero() && !link.ExpiresAt.After(now) {
			continue
		}
		if opts.MinBandwidthMBps > 0 && link.BandwidthMBps < opts.MinBandwidthMBps {
			continue
		}
		if opts.MaxLatencyP95MS > 0 && link.LatencyP95MS > opts.MaxLatencyP95MS {
			continue
		}
		key := linkKey{source: strings.ToLower(link.SourceNode), destination: strings.ToLower(link.DestinationNode)}
		current, exists := indexed[key]
		if !exists || link.MeasuredAt.After(current.MeasuredAt) || (link.MeasuredAt.Equal(current.MeasuredAt) && betterLink(link, current)) {
			indexed[key] = link
		}
	}
	return indexed
}

func betterLink(left, right LinkObservation) bool {
	if math.Abs(left.BandwidthMBps-right.BandwidthMBps) > floatEpsilon {
		return left.BandwidthMBps > right.BandwidthMBps
	}
	if math.Abs(left.LatencyP95MS-right.LatencyP95MS) > floatEpsilon {
		return left.LatencyP95MS < right.LatencyP95MS
	}
	return strings.ToLower(left.DestinationNode) < strings.ToLower(right.DestinationNode)
}

type candidatePlan struct {
	shards              []ModelShard
	links               []PlanLink
	bottleneckBandwidth float64
	totalLatency        float64
	maxUtilization      float64
	imbalance           float64
	sequence            string
}

func choosePlan(candidates []candidate, layers []int64, totalLayerMemory int64, maxNodes int, coordinator string, links indexedLinks) (candidatePlan, bool) {
	for nodeCount := 1; nodeCount <= maxNodes; nodeCount++ {
		var best candidatePlan
		found := false
		forEachCombination(candidates, nodeCount, func(subset []candidate) {
			if coordinator != "" && !containsCandidate(subset, coordinator) {
				return
			}
			usable := int64(0)
			for _, candidate := range subset {
				usable += candidate.usableMemoryMB
			}
			if usable < totalLayerMemory {
				return
			}
			for _, ordered := range topologyOrders(subset, coordinator, links) {
				partition, ok := partitionLayers(ordered.nodes, layers)
				if !ok {
					continue
				}
				plan := buildCandidatePlan(ordered, partition, layers)
				if !found || betterPlan(plan, best) {
					best = plan
					found = true
				}
			}
		})
		if found {
			return best, true
		}
	}
	return candidatePlan{}, false
}

func forEachCombination(candidates []candidate, size int, visit func([]candidate)) {
	if size <= 0 || size > len(candidates) {
		return
	}
	selection := make([]candidate, size)
	var walk func(start, depth int)
	walk = func(start, depth int) {
		if depth == size {
			visit(append([]candidate(nil), selection...))
			return
		}
		remaining := size - depth
		for i := start; i <= len(candidates)-remaining; i++ {
			selection[depth] = candidates[i]
			walk(i+1, depth+1)
		}
	}
	walk(0, 0)
}

type orderedNodes struct {
	nodes []candidate
	links []LinkObservation
}

func topologyOrders(subset []candidate, coordinator string, links indexedLinks) []orderedNodes {
	if len(subset) == 1 {
		return []orderedNodes{{nodes: append([]candidate(nil), subset...)}}
	}
	starts := make([]candidate, 0, len(subset))
	for _, candidate := range subset {
		if coordinator == "" || strings.EqualFold(candidate.node.Name, coordinator) {
			starts = append(starts, candidate)
		}
	}
	orders := make([]orderedNodes, 0, len(starts))
	seen := make(map[string]struct{})
	for _, start := range starts {
		remaining := make([]candidate, 0, len(subset)-1)
		for _, candidate := range subset {
			if !strings.EqualFold(candidate.node.Name, start.node.Name) {
				remaining = append(remaining, candidate)
			}
		}
		order := []candidate{start}
		path := make([]LinkObservation, 0, len(subset)-1)
		current := start
		for len(remaining) > 0 {
			nextIndex := -1
			var selected LinkObservation
			for i, candidate := range remaining {
				link, ok := links[linkKey{
					source:      strings.ToLower(current.node.Name),
					destination: strings.ToLower(candidate.node.Name),
				}]
				if !ok {
					continue
				}
				if nextIndex == -1 || betterLink(link, selected) || (sameLinkQuality(link, selected) && betterCandidate(candidate, remaining[nextIndex])) {
					nextIndex = i
					selected = link
				}
			}
			if nextIndex == -1 {
				order = nil
				break
			}
			next := remaining[nextIndex]
			order = append(order, next)
			path = append(path, selected)
			remaining = append(remaining[:nextIndex], remaining[nextIndex+1:]...)
			current = next
		}
		if len(order) != len(subset) {
			continue
		}
		sequence := nodeSequence(order)
		if _, duplicate := seen[sequence]; duplicate {
			continue
		}
		seen[sequence] = struct{}{}
		orders = append(orders, orderedNodes{nodes: order, links: path})
	}
	return orders
}

func sameLinkQuality(left, right LinkObservation) bool {
	return math.Abs(left.BandwidthMBps-right.BandwidthMBps) <= floatEpsilon &&
		math.Abs(left.LatencyP95MS-right.LatencyP95MS) <= floatEpsilon
}

func betterCandidate(left, right candidate) bool {
	if left.usableMemoryMB != right.usableMemoryMB {
		return left.usableMemoryMB > right.usableMemoryMB
	}
	return strings.ToLower(left.node.Name) < strings.ToLower(right.node.Name)
}

func nodeSequence(nodes []candidate) string {
	parts := make([]string, len(nodes))
	for i, node := range nodes {
		parts[i] = strings.ToLower(node.node.Name)
	}
	return strings.Join(parts, "\x00")
}

type partitionResult struct {
	ends           []int
	maxUtilization float64
	imbalance      float64
}

type partitionMemoEntry struct {
	result partitionResult
	ok     bool
	set    bool
}

func partitionLayers(nodes []candidate, layers []int64) (partitionResult, bool) {
	if len(nodes) == 0 || len(nodes) > len(layers) {
		return partitionResult{}, false
	}
	prefix := make([]int64, len(layers)+1)
	for i, layer := range layers {
		prefix[i+1] = prefix[i] + layer
	}
	memo := make(map[[2]int]partitionMemoEntry)
	var solve func(nodeIndex, startLayer int) (partitionResult, bool)
	solve = func(nodeIndex, startLayer int) (partitionResult, bool) {
		key := [2]int{nodeIndex, startLayer}
		if cached, exists := memo[key]; exists && cached.set {
			return cached.result, cached.ok
		}
		capacity := nodes[nodeIndex].usableMemoryMB
		remainingNodes := len(nodes) - nodeIndex
		remainingLayers := len(layers) - startLayer
		if remainingLayers < remainingNodes {
			memo[key] = partitionMemoEntry{set: true}
			return partitionResult{}, false
		}
		if nodeIndex == len(nodes)-1 {
			memory := prefix[len(layers)] - prefix[startLayer]
			if memory <= 0 || memory > capacity {
				memo[key] = partitionMemoEntry{set: true}
				return partitionResult{}, false
			}
			utilization := float64(memory) / float64(capacity)
			result := partitionResult{ends: []int{len(layers)}, maxUtilization: utilization, imbalance: utilization * utilization}
			memo[key] = partitionMemoEntry{result: result, ok: true, set: true}
			return result, true
		}

		maxEnd := len(layers) - (remainingNodes - 1)
		var best partitionResult
		found := false
		for end := startLayer + 1; end <= maxEnd; end++ {
			memory := prefix[end] - prefix[startLayer]
			if memory > capacity {
				break
			}
			next, ok := solve(nodeIndex+1, end)
			if !ok {
				continue
			}
			utilization := float64(memory) / float64(capacity)
			candidate := partitionResult{
				ends:           append([]int{end}, next.ends...),
				maxUtilization: math.Max(utilization, next.maxUtilization),
				imbalance:      utilization*utilization + next.imbalance,
			}
			if !found || betterPartition(candidate, best) {
				best = candidate
				found = true
			}
		}
		memo[key] = partitionMemoEntry{result: best, ok: found, set: true}
		return best, found
	}
	return solve(0, 0)
}

func betterPartition(left, right partitionResult) bool {
	if math.Abs(left.maxUtilization-right.maxUtilization) > floatEpsilon {
		return left.maxUtilization < right.maxUtilization
	}
	if math.Abs(left.imbalance-right.imbalance) > floatEpsilon {
		return left.imbalance < right.imbalance
	}
	for i := range left.ends {
		if left.ends[i] != right.ends[i] {
			return left.ends[i] < right.ends[i]
		}
	}
	return false
}

func buildCandidatePlan(order orderedNodes, partition partitionResult, layers []int64) candidatePlan {
	shards := make([]ModelShard, 0, len(order.nodes))
	start := 0
	for i, end := range partition.ends {
		layerMemory := sumInt64(layers[start:end])
		node := order.nodes[i]
		required := layerMemory + (node.allocatableMB - node.usableMemoryMB)
		shards = append(shards, ModelShard{
			Node:              node.node.Name,
			StartLayer:        start,
			EndLayerExclusive: end,
			LayerMemoryMB:     layerMemory,
			OverheadMB:        node.allocatableMB - node.usableMemoryMB,
			RequiredMemoryMB:  required,
			AllocatableMB:     node.allocatableMB,
			Utilization:       float64(required) / float64(node.allocatableMB),
		})
		start = end
	}

	planLinks := make([]PlanLink, 0, len(order.links))
	bottleneck := math.Inf(1)
	totalLatency := float64(0)
	for _, link := range order.links {
		if link.BandwidthMBps < bottleneck {
			bottleneck = link.BandwidthMBps
		}
		totalLatency += link.LatencyP95MS
		planLinks = append(planLinks, PlanLink{
			SourceNode:      link.SourceNode,
			DestinationNode: link.DestinationNode,
			Interconnect:    link.Interconnect,
			BandwidthMBps:   link.BandwidthMBps,
			LatencyP95MS:    link.LatencyP95MS,
			RDMA:            link.RDMA,
			Source:          link.Source,
			MeasuredAt:      link.MeasuredAt.UTC(),
		})
	}
	if len(order.links) == 0 {
		bottleneck = math.Inf(1)
	}
	return candidatePlan{
		shards:              shards,
		links:               planLinks,
		bottleneckBandwidth: bottleneck,
		totalLatency:        totalLatency,
		maxUtilization:      partition.maxUtilization,
		imbalance:           partition.imbalance,
		sequence:            nodeSequence(order.nodes),
	}
}

func betterPlan(left, right candidatePlan) bool {
	if math.Abs(left.bottleneckBandwidth-right.bottleneckBandwidth) > floatEpsilon {
		return left.bottleneckBandwidth > right.bottleneckBandwidth
	}
	if math.Abs(left.totalLatency-right.totalLatency) > floatEpsilon {
		return left.totalLatency < right.totalLatency
	}
	if math.Abs(left.maxUtilization-right.maxUtilization) > floatEpsilon {
		return left.maxUtilization < right.maxUtilization
	}
	if math.Abs(left.imbalance-right.imbalance) > floatEpsilon {
		return left.imbalance < right.imbalance
	}
	return left.sequence < right.sequence
}

func sumInt64(values []int64) int64 {
	total := int64(0)
	for _, value := range values {
		total += value
	}
	return total
}

func ceilDiv(value, divisor int64) int64 {
	if value <= 0 {
		return 0
	}
	return 1 + (value-1)/divisor
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right {
		return 0, fmt.Errorf("int64 overflow")
	}
	if right < 0 && left < math.MinInt64-right {
		return 0, fmt.Errorf("int64 underflow")
	}
	return left + right, nil
}

func checkedMul(left, right int64) (int64, error) {
	if left == 0 || right == 0 {
		return 0, nil
	}
	if left < 0 || right < 0 {
		return 0, fmt.Errorf("negative multiplication is not supported")
	}
	if left > math.MaxInt64/right {
		return 0, fmt.Errorf("int64 overflow")
	}
	return left * right, nil
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
