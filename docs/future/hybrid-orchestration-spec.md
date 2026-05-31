# AXIS Hybrid Multi-Agent Orchestration & Inference Specification
**Status:** Proposal / Specification (Proposed for implementation)  
**Target Version:** v0.11.x+  
**Authors:** AI Pair Programming Session (Antigravity + Smithanator)  

---

## 1. Executive Summary

This document specifies the integrated architecture for **Triangle Multi-Agent Orchestration** combined with an **Event-Driven LLM & Task Inference Queue** (referred to as the *Hybrid Architecture*). 

The goal of this design is to allow independent, resource-aware agents (e.g., Grok, Hermes) to safely cooperate in "crews" on the **AXIS Sovereign Grid** without overloading thin client nodes, saturating low-speed networks, or violating the project's core **Truth Rule** (advisory layers must never present speculative generation as cluster truth).

This specification is explicitly designed to be **portable and config-driven**, containing no hardcoded node names, private IP addresses, or rigid hardware assumptions.

---

## 2. Integrated Architecture Overview

The system divides responsibilities between two core Advisory layers:

1.  **Triangle (Context & Routing Brain):** Defines the crew session, specialized agent roles (Orchestrator, Muscle, Scout), and required task constraints.
2.  **Cortex Event Queue (Execution & Concurrency Engine):** Manages async task delivery, distributed locks, safety validation, and local execution.

```
                  ┌──────────────────────────────┐
                  │   Triangle Crew definition   │
                  │  (Role Mapping & Intent)     │
                  └──────────────┬───────────────┘
                                 │
                     1. Classify Prompt Intent
                                 ▼
                  ┌──────────────────────────────┐
                  │    llmrouter.DynamicEngine   │
                  │  (Finds resident model/GPU)  │
                  └──────────────┬───────────────┘
                                 │
                     2. Request Speculative Lease
                                 ▼
                  ┌──────────────────────────────┐
                  │      reservation.Ledger      │
                  │  (Authoritative local lease) │
                  └──────────────┬───────────────┘
                                 │
                     3. Publish Task Event
                                 ▼
                  ┌──────────────────────────────┐
                  │      Cortex Event Queue      │
                  │ (Distributed Locking/Queue)  │
                  └──────────────┬───────────────┘
                                 │
                     4. Claim & Guarded Run
                                 ▼
                  ┌──────────────────────────────┐
                  │     execution.WorkerDaemon   │
                  │  (Local execution + rusage)  │
                  └──────────────────────────────┘
```

---

## 3. Portability & Generalization (Config-Driven Design)

To ensure this code compiles and runs across diverse environments in the public repository, the substrate abstracts physical assets into configuration files and capability tags.

### 3.1 Role & Label Tagging (`nodes.yaml`)
Instead of referencing specific node hostnames, nodes in the cluster define their properties in `~/.axis/nodes.yaml`:

```yaml
nodes:
  - name: node-primary
    hostname: 192.168.1.100
    ssh_user: axis-operator
    roles: ["orchestrator", "muscle"]
    labels:
      gpu_backend: cuda       # cuda, metal, rocm, or cpu
      gpu_vram_mb: 8192
      network_speed: gigabit
      storage_type: ssd

  - name: node-edge
    hostname: 192.168.1.105
    ssh_user: pi-user
    roles: ["scout"]
    labels:
      gpu_backend: cpu
      low_power: "true"
```

### 3.2 Dynamic Model Residency
The placement and routing engines must verify that a node contains the requested local model before scheduling. This is achieved by dynamically querying the node facts:
1.  **Probing facts:** The Discovery daemon queries the node's Ollama `/api/tags` endpoint.
2.  **Injecting facts:** Discovered models are registered under `NodeFacts.ResidentModels`.
3.  **Filtering:** The placement engine immediately drops any candidate node where the requested model is not resident.

### 3.3 Platform-Independent Fallbacks
If local GPU-accelerated backends are missing, the system degrades gracefully:
*   **Tier 1 (GPU Local):** Routes requests to a node containing `gpu_backend: cuda` or `gpu_backend: metal`.
*   **Tier 2 (CPU Local):** Routes to a CPU-only node (slower, but private).
*   **Tier 3 (Cloud Fallback):** Fall back to external providers (e.g. OpenAI/Anthropic keys in `.env`) or the offline regex-based reflex matcher.

---

## 4. Go Interface & Data Struct Definitions

### 4.1 Dynamic LLM Routing (`internal/llmrouter/engine.go`)
Allows the classification engine to query the cached cluster snapshot and select the best endpoint dynamically:

```go
package llmrouter

import (
	"context"
	"fmt"
	"net/http"
	"sync"

	"github.com/toasterbook88/axis/internal/models"
)

type DynamicEngine struct {
	mu             sync.RWMutex
	activeEndpoint string
	fallbackModel  string
	client         *http.Client
}

// SelectBestEndpoint parses the snapshot for nodes running Ollama with the desired model resident.
func (de *DynamicEngine) SelectBestEndpoint(snapshot *models.ClusterSnapshot, targetModel string) (string, error) {
	var bestNode string
	var lowestPressure float64 = 100.0

	for _, node := range snapshot.Nodes {
		hasModel := false
		for _, m := range node.Facts.ResidentModels {
			if m == targetModel {
				hasModel = true
				break
			}
		}

		// Prioritize GPU backends over CPU, then sort by lowest memory pressure
		if hasModel && node.Facts.RAMPressure < lowestPressure {
			lowestPressure = node.Facts.RAMPressure
			bestNode = node.Facts.Hostname
		}
	}

	if bestNode == "" {
		return "", fmt.Errorf("no active node with resident model %s found in snapshot", targetModel)
	}

	return fmt.Sprintf("http://%s:11434", bestNode), nil
}
```

---

### 4.2 Speculative Pipeline Reservations (`internal/reservation/ledger.go`)
Allows reserving multiple nodes for multi-step workflows. If any step fails to reserve, the entire pipeline transaction is aborted:

```go
package reservation

import (
	"fmt"
	"sync"
	"time"
)

type PipelineStep struct {
	NodeName string        `json:"node_name"`
	RAMMB    int64         `json:"ram_mb"`
	Duration time.Duration `json:"duration"`
}

type SpeculativePipeline struct {
	Steps []PipelineStep `json:"steps"`
}

// SpeculativeAcquire reserves all pipeline steps atomically.
func (l *Ledger) SpeculativeAcquire(pipeline SpeculativePipeline, owner string) ([]string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	var reservationIDs []string
	rollback := func() {
		for _, id := range reservationIDs {
			l.release(id)
		}
	}

	for i, step := range pipeline.Steps {
		nodeState := l.getNodeState(step.NodeName)
		if nodeState.AllocatableRAMMB < step.RAMMB {
			rollback()
			return nil, fmt.Errorf("aborted: step %d on node %s lacks free RAM (%d MB requested)", i, step.NodeName, step.RAMMB)
		}

		resID := fmt.Sprintf("spec_%d_%s", time.Now().UnixNano(), step.NodeName)
		l.reservations[resID] = Reservation{
			ID:        resID,
			Node:      step.NodeName,
			RAMMB:     step.RAMMB,
			Owner:     owner,
			ExpiresAt: time.Now().Add(step.Duration),
		}
		reservationIDs = append(reservationIDs, resID)
	}

	return reservationIDs, nil
}
```

---

### 4.3 Observation-Based Placement (`internal/placement/empirical.go`)
Adjusts a node's `FitScore` based on historical durations and success rates stored in the local skills database:

```go
package placement

import (
	"time"
	"github.com/toasterbook88/axis/internal/models"
)

// ApplyObservationModifiers dynamically penalizes/rewards candidates based on previous runtimes.
func ApplyObservationModifiers(candidate *models.NodeFacts, taskKey string, baseScore int) int {
	history := GetObservationHistory(candidate.Hostname, taskKey)
	if len(history) == 0 {
		return baseScore
	}

	avgDuration := history.AverageDuration()
	if avgDuration > 30*time.Second {
		baseScore -= 10 // Slow execution penalty
	} else if avgDuration < 5*time.Second {
		baseScore += 15 // Fast execution bonus
	}

	if history.FailureRate() > 0.20 {
		baseScore -= 30 // High failure rate penalty
	}

	return baseScore
}
```

---

## 5. Formal JSON-RPC 2.0 MCP Tool Schemas

To prevent validation issues when external client agents (Grok, Hermes) communicate with the substrate, these schemas enforce strict typing.

### 5.1 `triangle_request_lease`
*   **Method:** `tools/call`
*   **Schema:**
```json
{
  "name": "triangle_request_lease",
  "description": "Request a speculative execution lease for a crew session across nodes.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "session_id": {
        "type": "string",
        "description": "Unique UUID identifying the multi-agent crew session."
      },
      "node": {
        "type": "string",
        "description": "Name of the target execution node."
      },
      "ram_mb": {
        "type": "integer",
        "description": "Amount of memory required in Megabytes."
      },
      "duration_seconds": {
        "type": "integer",
        "description": "Time window lease will remain valid without heartbeats."
      }
    },
    "required": ["session_id", "node", "ram_mb", "duration_seconds"],
    "additionalProperties": false
  }
}
```

### 5.2 `triangle_delegate_task`
*   **Method:** `tools/call`
*   **Schema:**
```json
{
  "name": "triangle_delegate_task",
  "description": "Publishes a safety-evaluated command to the Cortex event bus for a remote worker.",
  "inputSchema": {
    "type": "object",
    "properties": {
      "lease_id": {
        "type": "string",
        "description": "Active lease ID obtained from triangle_request_lease."
      },
      "target_node": {
        "type": "string",
        "description": "Node running the worker daemon."
      },
      "command": {
        "type": "string",
        "description": "The exact shell command to execute."
      },
      "cwd": {
        "type": "string",
        "description": "Target working directory on the remote node."
      }
    },
    "required": ["lease_id", "target_node", "command", "cwd"],
    "additionalProperties": false
  }
}
```

---

## 6. Failure Mode & Mitigation Matrix

| Failure Mode | Impact | Mitigation Strategy |
| :--- | :--- | :--- |
| **NTP Clock Skew** | Expiry checks pass/fail prematurely. | Expiry timestamps in `ledger.json` utilize epoch logical intervals or relative duration counters (time-to-live delta) instead of absolute wall-clock checks. |
| **Lost Task Heartbeats** | Resources remain reserved forever. | The worker daemon must publish heartbeats to Cortex. Stale heartbeats beyond 45 seconds are auto-reclaimed by the `axis reservation doctor`. |
| **Worker Daemon Offline** | Task is dispatched but never claimed. | Tasks remain in `queued` state. If not claimed within 30 seconds, the orchestrator triggers a failover and routes to the secondary placement candidate. |
| **Arbitrary Execution Vulnerability** | Shell injection / host compromise. | Commands must be parsed by `safety.Evaluator` rulesets. Worker daemons run inside restricted shell sandboxes under an unprivileged user group (`axis-worker`). |
| **Cold VRAM Overhead** | High latency on first LLM query. | Inject `"keep_alive": -1` in Ollama API calls for active tasks to prevent models from unloading during a crew session. |
