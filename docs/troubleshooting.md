# AXIS Troubleshooting Guide

**Last updated:** 2026-08-24  
**Applicable version:** v0.14.14+

This guide provides systematic diagnosis and recovery procedures for common AXIS operational issues.

---

## Diagnostic Workflow

Always start with:

```bash
axis doctor
```

This runs a comprehensive health check:
- Configuration validation
- SSH connectivity to all nodes
- Binary duplicate detection
- Daemon health (if installed)
- Mesh peer status

**Exit codes:**
- `0` — All checks passed
- `1` — One or more warnings (cluster still functional)
- `2+` — Critical failures (cluster impaired)

---

## SSH Connectivity Issues

### Symptom: Node Shows as Unreachable

```bash
axis cluster status
# Output: node "worker-1" status: unreachable
```

### Diagnosis

```bash
# 1. Test SSH manually
ssh -v worker1.example.internal echo "SSH works"

# 2. Check known hosts
ssh-keygen -F worker1.example.internal

# 3. Verify SSH agent
ssh-add -l
```

### Solutions

#### A: SSH Key Permission Issue

```bash
chmod 600 ~/.ssh/id_ed25519
chmod 700 ~/.ssh
```

#### B: Host Key Changed / Verification Failed

```bash
# Remove stale host key
ssh-keygen -R worker1.example.internal

# Re-accept host key
ssh worker1.example.internal
```

#### C: Firewall Blocking SSH

```bash
# Check if port 22 is open
nc -zv worker1.example.internal 22
```

#### D: SSH Agent Not Running

```bash
# Start agent and add key
eval "$(ssh-agent -s)"
ssh-add ~/.ssh/id_ed25519
```

---

## Placement Returns No Nodes

### Symptom: `ExitErrNoNodesFit` (Exit Code 3)

```bash
axis task place "heavy workload"
# Error: no nodes fit requirements
```

### Diagnosis

Use `placement explain` to see why each node was rejected:

```bash
axis placement explain "heavy workload"
```

### Common Causes & Solutions

#### A: Insufficient Allocatable RAM

**Problem:** Workload requests more RAM than any node has free.

**Solution:**
- Free memory on target node
- Release stale reservations: `axis reservations doctor --fix`
- Relax RAM requirement in prompt

#### B: GPU VRAM Mismatch

**Problem:** Workload requests GPU, but no node has sufficient free VRAM.

**Solution:**
- Check GPU status: `axis cluster status --cached=false`
- Stop unused inference servers: `axis model stop --node <name> --port <port>`

#### C: Thermal Throttling / Battery Gate

**Problem:** Target node is thermal throttling or battery < 20%.

**Solution:**
- Check thermal status: `axis node facts`
- Connect laptop to power

#### D: Storage Class Penalty

**Problem:** Workload requests fast storage, but node only has rotational HDD.

**Solution:** Ensure model weights are located on NVMe/SSD mounts.

---

## Daemon Issues

### Symptom: Daemon Won't Start

```bash
axis daemon start
# Error: bind: address already in use OR permission denied
```

### Diagnosis

```bash
# Check if daemon is already running
axis daemon service status

# Check port conflict (default: 8082)
lsof -i :8082

# Check log file
tail -50 ~/.local/share/axis/daemon.log
```

### Solutions

#### A: Port Conflict

```bash
# Start daemon on alternate port
axis daemon start --addr :8083

# Or kill existing process
fuser -k 8082/tcp
```

#### B: Corrupted Cache File

**Problem:** Snapshot cache is corrupted.

**Solution:**
```bash
# Invalidate cache
axis daemon invalidate

# Or remove cache file manually
rm ~/.local/share/axis/snapshot.json
```

#### C: Service Manager Issues

```bash
# macOS (launchd)
launchctl list | grep axis
launchctl unload ~/Library/LaunchAgents/org.axismcp.daemon.plist
axis daemon service install

# Linux (systemd)
systemctl --user status axis-daemon
systemctl --user restart axis-daemon
```

---

## AI Backend Issues

### Symptom: Backend Shows as Down

```bash
axis ai backends
# Output: backend "peer-llama" status: down
```

### Diagnosis

```bash
# 1. Test backend directly
curl -v http://<backend-host>:<port>/v1/models

# 2. Check locality
axis ai backends --format json | jq '.[] | {name, locality, url}'

# 3. Verify node binding
cat ~/.axis/ai.yaml
```

### Solutions

#### A: Backend Service Not Running

```bash
# Start Ollama
ollama serve

# Or llama-server
llama-server --models /path/to/models --port 8080
```

#### B: Wrong Node Binding

**Problem:** Backend configured with wrong `node` name

**Solution:**
```yaml
# ~/.axis/ai.yaml
backends:
  - name: peer-llama
    base_url: http://worker1.example.internal:8080
    node: worker-1  # Must match nodes.yaml name
```

#### C: advertise_url Misconfiguration

**Problem:** Off-box backend probing wrong URL

**Solution:**
```yaml
backends:
  - name: peer-llama
    base_url: http://192.0.2.10:8080  # Direct IP for local network
    advertise_url: https://llama.example.internal  # URL for external/peer access
    node: worker-1
```

#### D: Timeout Too Short

**Problem:** Backend probe times out

**Solution:**
```yaml
# ~/.axis/ai.yaml
backend_probe_timeout_sec: 30  # Default is 5s
```

---

## Reservation Issues

### Symptom: Stale Reservation Blocking Execution

```bash
axis task run "workload"
# Error: No available capacity (all nodes reserved)
```

### Diagnosis

```bash
# List all reservations
axis reservations list

# Find stale entries
axis reservations doctor --stale-window 1h

# Inspect specific reservation
axis reservations inspect <reservation-id>
```

### Solutions

#### A: Release Stale Reservation

```bash
# Release by ID
axis reservations release <reservation-id>

# Or bulk fix
axis reservations doctor --stale-window 1h --fix
```

#### B: Reservation Heartbeat Lost

**Problem:** Executing process died without releasing

**Solution:**
```bash
# Doctor will detect and fix
axis reservations doctor --fix

# Manually release if needed
axis reservations release --force <reservation-id>
```

#### C: Ledger Corruption

**Problem:** `~/.axis/ledger.json` corrupt

**Solution:**
```bash
# Backup
cp ~/.axis/ledger.json ~/.axis/ledger.json.corrupt

# AXIS will auto-recover on next write
# Or clear manually (loses all reservations)
rm ~/.axis/ledger.json
```

---

## Mesh Gossip Issues

### Symptom: No Mesh Peers Discovered

```bash
axis mesh status
# Output: peers: 0
```

### Diagnosis

```bash
# Check if serve is running
axis daemon status

# Verify mesh is enabled
cat ~/.axis/nodes.yaml | grep -A5 discovery

# Check UDP port
ss -ulnp | grep 42424
```

### Solutions

#### A: Discovery Not Enabled

```yaml
# ~/.axis/nodes.yaml
discovery:
  enabled: true
  udp_port: 42424
  beacon_interval_sec: 3
  secret: your-shared-secret  # Optional, for HMAC auth
```

#### B: Firewall Blocking UDP

```bash
# Allow UDP beacons
sudo ufw allow 42424/udp

# Or iptables
sudo iptables -A INPUT -p udp --dport 42424 -j ACCEPT
```

#### C: Secret Mismatch

**Problem:** Nodes have different `discovery.secret`

**Solution:** Ensure all nodes share the same secret value.

---

## Model Lifecycle Issues

### Symptom: `axis model start` Fails

```bash
axis model start --node primary-node --weights /path/to/model.gguf --port 8080
# Error: port already in use OR weights not found
```

### Diagnosis

```bash
# Check port
lsof -i :8080

# Verify weights file
ls -lh /path/to/model.gguf

# Check llama-server binary
which llama-server
```

### Solutions

#### A: Port Already in Use

```bash
# Find and kill process
lsof -i :8080
kill <PID>

# Or use different port
axis model start --port 8081 ...
```

#### B: Weights File Not Found

```bash
# Verify path is absolute and on named volume
axis model start --node primary-node --weights /Volumes/Models/model.gguf --port 8080
```

#### C: llama-server Not Installed

```bash
# Install via Homebrew (macOS)
brew install llama.cpp

# Or build from source
git clone https://github.com/ggerganov/llama.cpp
cd llama.cpp && make
```

### Symptom: `axis model stop` Fails

```bash
axis model stop --node primary-node --port 8080
# Error: process not found OR permission denied
```

### Solutions

#### A: Process Already Stopped

```bash
# Idempotent - safe to run again
axis model stop --node primary-node --port 8080
```

#### B: Wrong Process Ownership

**Problem:** Port owned by non-llama-server process

**Diagnosis:**
```bash
lsof -i :8080
```

**Solution:** Manually kill the correct process.

#### C: fuser/lsof Not Available

**Problem:** Port tool missing on platform

**Solution:**
```bash
# Install lsof
# macOS: already installed
# Linux: sudo apt install lsof

# Or use netstat
netstat -tlnp | grep 8080
```

---

## Update Issues

### Symptom: `axis update` Fails

```bash
axis update
# Error: permission denied OR binary is managed
```

### Solutions

#### A: Permission Denied

```bash
# Install requires sudo for /usr/local/bin
sudo axis update

# Or use user-local install
AXIS_INSTALL_DIR=$HOME/.local/bin axis update
```

#### B: Binary Managed by Package Manager

```bash
# Check if managed
axis version

# If managed by nix/homebrew, use that instead
nix upgrade
# or
brew upgrade axis
```

#### C: Duplicate Binary Warning

```bash
# Find duplicates
axis doctor

# Remove shadow installs manually
# Keep only /usr/local/bin/axis (canonical)
```

---

## Performance Issues

### Symptom: Slow Placement Decisions

```bash
time axis task place "large workload"
# Takes > 5 seconds
```

### Diagnosis

```bash
# Check if using cached snapshot
time axis cluster status --cached
time axis cluster status --cached=false
```

### Solutions

#### A: Enable Daemon Cache

```bash
# Install daemon
axis daemon service install

# Use cached snapshot
axis task place --cached "workload"
```

#### B: Too Many Nodes

**Problem:** SSH fan-out to many nodes is slow

**Solution:**
- Increase timeout in `nodes.yaml`:
  ```yaml
  nodes:
    - name: node1
      timeout_sec: 30  # Default is 10s
  ```
- Use daemon with background refresh

#### C: Network Latency

**Problem:** High-latency SSH connections

**Solution:**
- Enable SSH multiplexing in `~/.ssh/config`:
  ```
  Host *.example.internal
    ControlMaster auto
    ControlPath ~/.ssh/sockets/%r@%h-%p
    ControlPersist 600
  ```
- Use Tailscale for direct routing

---

## Error Codes Reference

| Code | Constant | Meaning | Recovery |
|------|----------|---------|----------|
| 0 | `ExitOK` | Success | N/A |
| 1 | `ExitErrGeneric` | Generic error | Check stderr |
| 2 | `ExitErrConfigLoad` | Config load failure | Validate `nodes.yaml` |
| 3 | `ExitErrNoNodesFit` | No nodes satisfy requirements | Relax constraints or add nodes |
| 4 | `ExitErrCommandFail` | Command execution failure | Check SSH, logs |
| 5 | `ExitErrContextWrite` | Context write failure | Check disk space |
| 6 | `ExitErrIO` | I/O failure | Check filesystem |
| 7 | `ExitErrModelUnlisted` | Model not listed on backends | Add model or fix backend |

---

## Getting More Help

### Collect Diagnostics

```bash
# Full cluster state
axis cluster status --format json > cluster-state.json

# Local facts
axis node facts --format json > local-facts.json

# Daemon logs
journalctl --user -u axis-daemon > daemon.log  # Linux
log show --predicate 'process == "axis"' > daemon.log  # macOS

# Create issue on GitHub
gh issue create --label bug
```

### Community Resources

- **GitHub Issues:** https://github.com/toasterbook88/axis/issues
- **CHANGELOG:** [`CHANGELOG.md`](CHANGELOG.md) — Known issues per version
- **Architecture:** [`docs/architecture.md`](docs/architecture.md) — System design

---

**Truth Rule Reminder:** Always verify cluster state with live probes (`axis facts`, `axis status`) before making operational changes.
