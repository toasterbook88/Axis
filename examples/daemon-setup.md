# AXIS Daemon Setup

The AXIS daemon maintains a background snapshot cache, serves the HTTP API,
and watches config / state / skills files for changes. Running the daemon
makes `axis status --cached`, `axis task place --cached`, and the HTTP API
available.

## Quick start

### 1. Start the daemon

```bash
axis serve
```

By default this listens on a Unix socket at `~/.axis/axis.sock` and refreshes
the cluster snapshot every minute.

Listen on TCP instead:

```bash
axis serve --addr localhost:8080
```

Change the refresh interval:

```bash
axis serve --refresh 30s
```

### 2. Check daemon health

```bash
axis daemon status
```

Shows version, whether the cache is stale, and the last refresh time.

### 3. Use cached reads

```bash
axis status --cached
axis task place --cached "run ollama inference"
axis task context --cached "compile a rust project"
```

### 4. Trigger a manual refresh

```bash
axis daemon refresh
```

### 5. Invalidate the cache

```bash
axis daemon invalidate
```

Forces the daemon to rebuild the snapshot on the next request.

### 6. Restart the daemon

```bash
axis daemon restart
```

Sends SIGTERM to the existing daemon and starts a fresh one from the current
binary.

## Daemon subcommands summary

| Command | Purpose |
|---------|---------|
| `axis serve` | Start the HTTP API + background refresh |
| `axis daemon start` | Alias for `axis serve` |
| `axis daemon status` | Health and staleness check |
| `axis daemon refresh` | Immediate snapshot refresh |
| `axis daemon invalidate` | Clear in-memory cache |
| `axis daemon restart` | Graceful restart from current binary |

## Config-driven refresh

The daemon polls `~/.axis/nodes.yaml` every 500 ms. When the file changes:

1. The cache is invalidated.
2. A refresh is triggered with the new config.
3. If UDP discovery is enabled, the beacon listener restarts with new settings.

This means you can edit nodes, roles, or discovery settings without restarting
the daemon.

## Cache files

| File | Purpose |
|------|---------|
| `~/.axis/snapshot.json` | Last cached cluster snapshot |
| `~/.axis/state.json` | Placement decisions, observations, failure records |
| `~/.axis/ledger.json` | Reservation ledger |
| `~/.axis/skills.json` | Learned skills and failures |

These files are updated automatically. You should not edit `snapshot.json` or
`ledger.json` manually.

## Staleness

A snapshot is considered stale when it is older than the staleness threshold
(default 5 minutes). When stale:

- `axis daemon status` prints a warning.
- `axis status --cached` appends a warning line.
- The HTTP API `/snapshot/meta` sets `stale: true`.

If your cluster changes frequently, decrease the refresh interval:

```bash
axis serve --refresh 30s
```

## Running as a background service

### Using systemd (Linux)

Create `~/.config/systemd/user/axis-daemon.service`:

```ini
[Unit]
Description=AXIS daemon
After=network.target

[Service]
Type=simple
ExecStart=%h/go/bin/axis serve
Restart=on-failure
RestartSec=5

[Install]
WantedBy=default.target
```

Then:

```bash
systemctl --user daemon-reload
systemctl --user enable --now axis-daemon
```

### Using launchd (macOS)

Create `~/Library/LaunchAgents/com.axismcp.daemon.plist`:

```xml
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN"
  "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>Label</key>
  <string>com.axismcp.daemon</string>
  <key>ProgramArguments</key>
  <array>
    <string>/usr/local/bin/axis</string>
    <string>serve</string>
  </array>
  <key>RunAtLoad</key>
  <true/>
  <key>KeepAlive</key>
  <true/>
</dict>
</plist>
```

Then:

```bash
launchctl load ~/Library/LaunchAgents/com.axismcp.daemon.plist
```

## HTTP API

When the daemon is running, the v1 API is available at the configured address.

### Unix socket example

```bash
curl --unix-socket ~/.axis/axis.sock http://localhost/health
```

### TCP example

```bash
curl http://localhost:8080/health
```

### Common endpoints

| Route | Auth | Description |
|-------|------|-------------|
| `GET /health` | No | Daemon health |
| `GET /snapshot` | Yes | Full cluster snapshot |
| `GET /snapshot/meta` | Yes | Cache metadata |
| `POST /refresh` | Yes | Trigger refresh |
| `POST /invalidate` | Yes | Invalidate cache |

The API token is stored in `~/.axis/token` and is generated automatically on
first daemon start. You can also set it via the `AXIS_API_TOKEN` environment
variable.
