# AXIS

A lightweight, local-first coordination substrate for cluster-aware AI execution.

AXIS discovers nodes and tools, collects hardware facts, performs RAM-aware task placement, and emits compact [ClusterSnapshots](docs/white_paper_v1.md) that reduce redundant reasoning overhead for frontier models.

## Status

**Phase 1** — CLI bootstrap & node discovery.

## Quick Start

```bash
# Build
go build -o axis ./cmd/axis/

# Local facts
./axis facts

# Cluster snapshot (requires ~/.axis/nodes.yaml)
./axis status --format json
```

## Configuration

Create `~/.axis/nodes.yaml` (see [nodes.example.yaml](nodes.example.yaml)):

```yaml
nodes:
  - name: node-a
    hostname: node-a.local
    ssh_user: user
    role: primary
  - name: node-b
    hostname: node-b.local
    ssh_user: user
    role: worker
```

## Architecture

See [Phase 1 Build Spec](docs/phase1_spec.md) and [White Paper](docs/white_paper_v1.md).

## License

MIT
