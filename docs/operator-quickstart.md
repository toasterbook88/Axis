# AXIS Operator Quickstart

**Last updated:** 2026-08-24  
**Applicable version:** v0.14.14+

This guide gets you from zero to a working AXIS cluster in under 15 minutes.

---

## Prerequisites

### Hardware
- **Minimum 2 machines** (can be laptops, desktops, servers, or VMs)
- **Network connectivity** between machines (LAN, Tailscale, or SSH-accessible)
- **At least one machine** with:
  - Go 1.26+ (for building from source)
  - Or: `curl` + `bash` (for binary install)

### Software
- **SSH access** to all nodes (key-based auth recommended)
- **Linux, macOS, or BSD** on each node
- **Optional but recommended:**
  - Tailscale for secure mesh networking
  - NVIDIA GPU drivers (for ML workloads)

---

## Step 1: Install AXIS

### Option A: Binary Install (Recommended)

On your **primary workstation**:

```bash
# Download and install to /usr/local/bin
curl -fsSL https://raw.githubusercontent.com/toasterbook88/axis/main/install.sh | bash

# Verify installation
axis version
```

Expected output:
```
AXIS v0.14.14
commit: <sha>
go: go1.26.x
platform: linux/amd64 (or darwin/arm64, etc.)
```

### Option B: Build from Source

```bash
# Clone the repository
git clone https://github.com/toasterbook88/axis.git
cd axis

# Build and install
make build
sudo make install-system  # Installs to /usr/local/bin

# Verify
axis version
```

---

## Step 2: Configure Your Cluster

Run the interactive setup wizard:

```bash
axis init
```

This will:
1. Detect your local machine's stable identity (if available)
2. Prompt you to add cluster nodes
3. Configure SSH connectivity
4. Optionally enable UDP discovery beacons

### Manual Configuration (Alternative)

Edit `~/.axis/nodes.yaml`:

```yaml
nodes:
  - name: primary-node
    hostname: workstation.local
    ssh_user: your-username
    role: primary  # Optional: marks this as the primary node

  - name: worker-1
    hostname: worker1.example.internal
    ssh_user: axis
    role: worker

  - name: gpu-box
    hostname: 192.0.2.10
    ssh_user: your-username
    ssh_port: 22  # Optional, defaults to 22
```

**Important:** Unknown YAML keys are rejected at load time — only use documented fields.

---

## Step 3: Verify Connectivity

```bash
# Test SSH to all configured nodes
axis doctor

# Expected output should show:
# - All nodes reachable
# - SSH key verification passed
# - No duplicate binaries detected
```

If any node fails:
- Check SSH key permissions (`chmod 600 ~/.ssh/id_ed25519`)
- Verify hostname resolves correctly
- Ensure SSH agent is running (`ssh-add -l`)

---

## Step 4: Inspect Your Cluster

### View Local Hardware

```bash
axis node facts
```

This shows:
- CPU cores, RAM, storage
- GPU inventory (NVIDIA/Apple Silicon)
- Network interfaces
- Installed tools (Ollama, llama.cpp, etc.)
- Thermal and battery status (if applicable)

### View Full Cluster

```bash
# Live snapshot (fresh SSH collect)
axis cluster status

# Cached snapshot (instant, from daemon)
axis cluster status --cached

# Summary view
axis cluster summary
```

---

## Step 5: Run Your First Task

### Placement Advisory

```bash
# Ask where to run a workload
axis task place "run ollama inference on a 7b model"
```

Output includes:
- **Selected node** with FitScore (0–100)
- **Reasoning** for the choice
- **Alternative nodes** that were considered

### Get Detailed Explanation

```bash
axis placement explain "run ollama inference on a 7b model"
```

Shows per-node scoring breakdown:
- Allocatable RAM
- GPU VRAM match
- Model locality bonus
- TurboQuant suitability (Apple Silicon)

### Execute the Task

```bash
# Guarded execution with safety checks
axis task run "run ollama inference on a 7b model"
```

Safety gates check:
- No thermal throttling
- Battery ≥ 20% (laptops)
- No active tombstones
- Storage class (HDD penalty for inference)

---

## Step 6: Set Up the Daemon (Optional but Recommended)

The daemon provides:
- Background snapshot refresh
- Cached cluster state
- HTTP API access
- Mesh gossip participation

### Install as a User Service

```bash
# Install native systemd (Linux) or launchd (macOS) service
axis daemon service install

# Check status
axis daemon service status

# View cached snapshot
axis daemon status
```

### Manual Start (Development)

```bash
# Start daemon in background
axis daemon start

# Trigger refresh
axis daemon refresh

# Invalidate cache
axis daemon invalidate
```

---

## Step 7: Configure AI Backends (Optional)

Edit `~/.axis/ai.yaml`:

```yaml
backends:
  - name: local-ollama
    base_url: http://127.0.0.1:11434
    node: primary-node  # Optional: ties backend to a cluster node

  - name: peer-llama
    base_url: http://worker1.example.internal:8080
    node: worker-1
    advertise_url: https://llama.example.internal  # For reverse-proxied access

roles:
  default: local-ollama
  chat: peer-llama
```

### Verify Backends

```bash
# Probe all backends, show locality
axis ai backends

# Output shows:
# - Backend health
# - Locality: here / peer / cloud
# - Advertised URL being probed
```

---

## Common Next Steps

### Set Up Mesh Gossip

```bash
# Start HTTP API with mesh enabled
axis serve --addr :8082 --refresh 30s

# Check mesh peers
axis mesh status
axis mesh peers
```

### Manage Reservations

```bash
# View active reservations
axis reservations list

# Inspect a specific reservation
axis reservations inspect <reservation-id>

# Release a reservation
axis reservations release <reservation-id>

# Run doctor to find stale entries
axis reservations doctor --fix
```

### Use the Agent

```bash
# Start interactive agent session
axis agent

# In-session commands:
# /facts     - View local hardware
# /cluster   - View cluster snapshot
# /exec      - Run guarded commands
# /exit      - End session
```

---

## Troubleshooting Quick Reference

| Problem | Command | Solution |
|---------|---------|----------|
| SSH connection refused | `axis doctor` | Check SSH keys, firewall, hostname |
| Node shows as unreachable | `axis cluster status --cached=false` | Force live collect |
| Placement returns no nodes | `axis placement explain "<prompt>"` | Check filter criteria |
| Daemon won't start | `axis daemon service status` | Check logs in `~/.local/share/axis/` |
| Duplicate binary warning | `axis doctor` | Remove shadow installs manually |
| Backend shows as down | `axis ai backends` | Verify `base_url` is reachable |

---

## What's Next?

- **Read:** [`docs/architecture.md`](docs/architecture.md) — Deep dive into the 5-layer stack
- **Explore:** [`docs/runbooks/`](docs/runbooks/) — Operational procedures
- **Reference:** [`AGENTS.md`](AGENTS.md) — Canonical repo knowledge
- **Roadmap:** [`docs/future-roadmap.md`](docs/future-roadmap.md) — Planned features

---

## Getting Help

- **GitHub Issues:** https://github.com/toasterbook88/axis/issues
- **Release Notes:** https://github.com/toasterbook88/axis/releases
- **CHANGELOG:** [`CHANGELOG.md`](CHANGELOG.md) — Version history

**Truth Rule Reminder:** No generated output may present itself as cluster truth unless backed by a real snapshot or live probe (`axis facts`, `axis status`, `axis task place`).
