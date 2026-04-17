# Repository Structure — Go Community Standards Audit

## Current vs Recommended

```
axis/                           STATUS    NOTES
├── .github/
│   ├── copilot-instructions.md ✅         Agent orientation
│   └── workflows/
│       ├── ci.yml              ✅         Push/PR test pipeline
│       └── release.yml         🆕 NEW    Tag-triggered release pipeline
├── cmd/
│   └── axis/                   ✅         Single binary entry point (Go standard)
│       ├── *.go                ✅         Cobra command files
│       └── dashboard.go        🆕 NEW    Rich CLI views (v0.10.0)
├── docs/                       ✅         Design docs, specs, roadmap
│   ├── current-state.md        ✅         CI-validated truth
│   ├── future-roadmap.md       ✅         Planned work
│   └── architecture.md         🆕 ADD    5-layer architecture reference
├── hack/                       ✅         Developer scripts (Go convention)
│   ├── coverage-check.sh       ✅
│   ├── verify-repo-truth.sh    ✅
│   └── refresh-current-state.sh ✅
├── internal/                   ✅         Private packages (Go enforced)
│   ├── agent/                  ✅         Tool-calling agent loop
│   ├── api/                    ✅         HTTP API server
│   │   ├── server.go           ✅         v1 routes
│   │   └── v2.go               🆕 NEW    v2 enhanced routes
│   ├── auth/                   ✅         Token authentication
│   ├── buildinfo/              ✅         Version constants
│   ├── chat/                   ✅         Structured chat client
│   ├── config/                 ✅         YAML config loader
│   ├── cortex/                 ✅         Distributed vector memory
│   ├── daemon/                 ✅         Background cache + refresh
│   ├── discovery/              ✅         SSH + UDP node discovery
│   ├── execution/              ✅         Guarded task execution
│   ├── facts/                  ✅         Hardware/tool collection
│   ├── failures/               ✅         Failure classification
│   ├── knowledge/              ✅         Cluster knowledge base
│   ├── llmrouter/              ✅         LLM routing layer
│   ├── mcp/                    ✅         MCP server (stdio)
│   ├── mesh/                   🆕 NEW    Gossip peer discovery
│   ├── models/                 ✅         Shared types
│   ├── persist/                ✅         File persistence
│   ├── placement/              ✅         Placement algorithm
│   ├── reservation/            🆕 NEW    Resource accounting ledger
│   ├── runtimectx/             ✅         Runtime context
│   ├── safety/                 ✅         Command safety gates
│   │   └── structured.go       🆕 NEW    Structured rule engine
│   ├── scripts/                ✅         Built-in script catalog
│   ├── secrets/                ✅         Secret management
│   ├── skills/                 ✅         Learned skills
│   ├── snapshot/               ✅         ClusterSnapshot assembly
│   ├── snapshotview/           ✅         Snapshot rendering
│   ├── state/                  ✅         Persistent state
│   ├── transport/              ✅         SSH execution layer
│   ├── turboexec/              ✅         Fast-path execution
│   ├── ui/                     ✅         CLI rendering
│   ├── versioncmp/             ✅         Version comparison
│   └── workload/               ✅         Workload classification
├── .gitignore                  ✅
├── .goreleaser.yml             🔄 UPDATE  Add Version ldflags + changelog groups
├── AGENTS.md                   ✅         AI agent instructions
├── CHANGELOG.md                ✅         Release history
├── CONTRIBUTING.md             ✅         Contribution guide
├── LICENSE                     ✅         MIT
├── Makefile                    ✅         Build/test/lint targets
├── README.md                   🔄 REWRITE Enterprise-grade with architecture
├── SECURITY.md                 ✅         Security policy
├── flake.lock                  ✅         Nix lock
├── flake.nix                   ✅         Nix build
├── go.mod                      ✅         Module definition
├── go.sum                      ✅         Dependency checksums
├── install.sh                  ✅         Quick install script
└── nodes.example.yaml          ✅         Example config
```

## Audit Findings

### ✅ Already Excellent (Go Community Standards)
1. **`internal/` for private packages** — enforced by Go toolchain
2. **`cmd/axis/` single entry point** — canonical Go layout
3. **`hack/` for dev scripts** — Kubernetes-established convention
4. **`docs/` for design material** — clean separation from code
5. **`Makefile` as build entry** — documented in AGENTS.md
6. **CI-validated truth** — `verify-repo-truth.sh` prevents doc drift
7. **`.goreleaser.yml` at root** — standard placement
8. **No `/pkg` directory** — correct choice for CLI-only projects
9. **No `/vendor` directory** — Go modules, no vendoring needed

### 🔄 Recommended Adjustments (3 items)

#### 1. Add `docs/architecture.md`
Reference document for the 5-layer architecture. Not a design doc —
a stable orientation reference for contributors and integrators.

#### 2. Consider `examples/` directory
Move `nodes.example.yaml` → `examples/nodes.yaml` and add:
- `examples/mesh.yaml` — mesh discovery config
- `examples/reservation-policy.yaml` — overcommit policy
- `examples/safety-rules.yaml` — custom safety rules

This is optional — the current placement at root is also fine for a
single example file. Only worth doing when you have 3+ examples.

#### 3. No structural changes needed for new packages
The 3 new packages (`mesh/`, `reservation/`, and v2.go in `api/`)
slot cleanly into the existing `internal/` layout. No reorganization required.

## Verdict

**Your repo structure is already enterprise-grade.** The layout follows
Go community standards closely. The only structural addition needed
is the release pipeline workflow — no folder reorganization required.
