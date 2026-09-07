# Deadcode triage — 190 symbols on main @ e93fcc7

Categories: DELETE (truly dead), KEEP-ALLOW (intentional surface, goes in allowfile),
FIX-LATER (partially used, needs wiring decision)

## DELETE candidates — no callers anywhere, no roadmap doc reference

### cmd/axis (13)

```text
cmd/axis/noun_registry.go            # all 4 funcs dead: entire file is a registry no one reads
cmd/axis/exit.go:52                  # Fatal — cobra has its own error printing
cmd/axis/model.go:833 resolveModelNode        # superseded by resolveModelNodeFromSnapshot (#385+)
cmd/axis/model.go:987 shellStop               # superseded by shellStopTarget
cmd/axis/task.go reservationMBForRequirements # orphaned reservation math
cmd/axis/task.go ensureReservationCapacity
cmd/axis/task.go remoteExecPrefix
cmd/axis/tui/logo.go RenderLogoWithVersion    # RenderLogo still used
cmd/axis/tui/logo.go RenderHeaderLine
cmd/axis/agent_console.go draining            # half-finished launcher cleanup path
```

## KEEP-ALLOW — intentional API surface or consumed via reflection/generics/build tags

- **internal/llmrouter (55)**: cloud.go provider stack + engine + registry are the
  Tier-3 cloud fallback from docs/future/hybrid-orchestration-spec.md §4.1 and
  roadmap item "local/cloud model routing". Engine.ClassifyWorkload satisfies
  workload.Classifier (compile-time asserted); deadcode can't see interface
  satisfaction. Entire package goes in the allowfile until the routing phase lands.
- **internal/modelplan/planner.go (30)**: PlanSingleNode is live; the Planner struct is
  the snapshot-backed distributed planner from #175. Allowfile pending decision:
  wire it or fold into modelplan plan command.
- **internal/fleettest/guard.go (10)**: consumed by fleet-tagged tests
  (`go:build fleet`, make test-fleet) — deadcode without tags can't see them.
- **internal/console/entries.go (9)**: Thinking/Stream/Diff entries are console render
  variants; Bridge currently emits Notice/Tool/Error only. Keep via allowfile or
  delete if console scope is frozen.
- **internal/facts/pairwise.go (6)**: BuildPairwiseLinkMatrix was shipped in #266 for
  "network topology enrichment" (roadmap line 123/326); MCP surface never wired.
  Decision needed: expose via MCP or delete.
- **internal/events**: SetLogPath/RegisterInterest/GetInterests/SetEventBufferSize/
  ShutdownWebhooks are extension API for MCP subscribers (observational event
  surface per AGENTS.md). Allowfile.
- **internal/ui**: spinner (6), errors (4), color (3), select (2), table (1) —
  presentational API used by tests only today. Allowfile or delete per taste.
- **internal/mcpclient (5)**: Registry/CallToolWithProgress — client SDK surface for
  agent MCP integration not yet wired. Allowfile or delete.
- **internal/transport/local.go (4)**: LocalExecutor implements transport.Executor for
  local-node symmetry; used by tests. Allowfile.
- **internal/skills/autodiscover.go (3)**: AutoDiscoverSkills not called since skills
  were made operator-managed. Delete or wire into axis skills command.
- **internal/chat (9) + internal/cortex client.go NewClient/AcquireLock/ReleaseLock (3)**:
  chat engine/ollama client parts and cortex lock primitives are library surface;
  cortex locks have no CLI path yet. Mostly allowfile; cortex locks = decide.

## Ambiguous singles — quick manual check each

```text
internal/api/server.go Serve            # api.Run used instead? verify
internal/daemon/client.go RunGuarded    # RunGuardedStream is the live path; RunGuarded wrapper dead
internal/daemon/daemon.go CanReserve, ApplyReservationView
internal/agent/agent.go MCPRegistry, Backend   # accessor methods, likely keep as API
internal/agent/tools.go ExecuteShell    # ExecuteShellWithTimeout is the live path
internal/chat/fallback_tools.go ExtractReasoning
internal/cortex/client.go NewClient     # NewClientWithTimeout is live
internal/discovery/discovery.go DiscoverWithWarnings, DiscoverSeeded
internal/events/webhooks.go ShutdownWebhooks
internal/facts/disk_weights.go ParseDiskWeightFindTSV
internal/lockutil/lock_unix.go File.LockEx
internal/mesh/mesh.go verifyHMAC        # check: is HMAC verification wired?
internal/models/memory.go AllocatableRAMMB
internal/models/types.go PlacementError.Error  # error() impl — deadcode artifact if type never constructed
internal/netutil/url.go ResetInternalAllowlist
internal/placement/ranker.go Headroom, clusterPressurePenalty
internal/secrets/secrets.go IsConfigured
internal/transport/ssh.go NewSSHExecutorWithDialTarget
```
