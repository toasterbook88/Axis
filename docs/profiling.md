# Profiling Workflow

## 1. Enabling pprof Endpoints

The `axis serve` and `axis daemon` commands expose an optional `--pprof` flag that registers Go's standard profiling HTTP endpoints.

```bash
axis serve --pprof
axis serve --addr 127.0.0.1:7373 --pprof
axis daemon start --pprof
```

By default, profiling is **disabled**. When enabled, the following endpoints are mounted under `/debug/pprof/`:
They use the same bearer token as the rest of the AXIS API. The examples below
load the generated token from `~/.axis/token`:

```bash
TOKEN=$(cat ~/.axis/token)
```

| Endpoint | Description |
|----------|-------------|
| `/debug/pprof/` | Index page listing all available profiles |
| `/debug/pprof/cmdline` | The command-line invocation of the program |
| `/debug/pprof/profile` | CPU profile (responds to `?seconds=N`) |
| `/debug/pprof/symbol` | Symbol lookup for addresses |
| `/debug/pprof/trace` | Execution trace (responds to `?seconds=N`) |

Standard Go runtime profiles are also available via query parameters on `/debug/pprof/`:

| Profile | URL |
|---------|-----|
| Heap | `/debug/pprof/heap` |
| Goroutines | `/debug/pprof/goroutine` |
| Thread creation | `/debug/pprof/threadcreate` |
| Blocking | `/debug/pprof/block` |
| Mutex contention | `/debug/pprof/mutex` |
| Allocs | `/debug/pprof/allocs` |

---

## 2. Capturing a CPU Profile

Use `curl` or a browser to trigger the CPU profiler, then analyze the result with `go tool pprof`.

### Step 1 — Collect the profile

```bash
# Default 30-second CPU profile
curl -H "Authorization: Bearer $TOKEN" -o cpu.prof http://localhost:7373/debug/pprof/profile

# Custom duration (e.g., 60 seconds)
curl -H "Authorization: Bearer $TOKEN" -o cpu.prof 'http://localhost:7373/debug/pprof/profile?seconds=60'
```

For a Unix socket listener (the default):

```bash
curl --unix-socket ~/.axis/axis.sock -H "Authorization: Bearer $TOKEN" -o cpu.prof http://localhost/debug/pprof/profile?seconds=30
```

### Step 2 — Analyze interactively

```bash
go tool pprof cpu.prof
```

Common pprof commands:

```
(pprof) top          # Show hottest functions
(pprof) top -cum     # Show highest cumulative time
(pprof) list <func>  # Show annotated source for a function
(pprof) web          # Open an interactive graph in a browser
(pprof) png          # Render a call graph to a PNG file
(pprof) quit
```

### Step 3 — Generate a flame graph

```bash
go tool pprof -http=:8080 cpu.prof
```

This starts a local web server on port 8080 with an interactive flame graph and source view.

---

## 3. Capturing a Heap Profile

Heap profiles are instantaneous snapshots; they do not require a sampling duration.

### Step 1 — Collect the profile

```bash
# TCP listener
curl -H "Authorization: Bearer $TOKEN" -o heap.prof http://localhost:7373/debug/pprof/heap

# Unix socket listener
curl --unix-socket ~/.axis/axis.sock -H "Authorization: Bearer $TOKEN" -o heap.prof http://localhost/debug/pprof/heap
```

### Step 2 — Analyze in-use memory

```bash
go tool pprof heap.prof
```

Common commands:

```
(pprof) top              # Top allocations by in-use space
(pprof) top -cum         # Top allocations by cumulative space
(pprof) alloc_space      # Switch to cumulative allocations
(pprof) inuse_space      # Switch to current in-use allocations
(pprof) list <func>      # Annotated source
(pprof) web
```

### Step 3 — Compare two heap snapshots

```bash
# Capture baseline
curl -H "Authorization: Bearer $TOKEN" -o heap1.prof http://localhost:7373/debug/pprof/heap

# ... run workload ...

# Capture after workload
curl -H "Authorization: Bearer $TOKEN" -o heap2.prof http://localhost:7373/debug/pprof/heap

# Diff them
go tool pprof -http=:8080 --diff_base=heap1.prof heap2.prof
```

---

## 4. Capturing an Execution Trace

Execution traces capture goroutine scheduling, blocking, and syscall events.

```bash
curl -H "Authorization: Bearer $TOKEN" -o trace.out 'http://localhost:7373/debug/pprof/trace?seconds=5'
go tool trace trace.out
```

This opens a browser with a detailed timeline of goroutine activity.

---

## 5. Security Warning

> **Only enable `--pprof` in trusted environments.**

The profiling endpoints expose:

- The full command-line invocation (`/debug/pprof/cmdline`), which may contain sensitive flags or paths.
- Stack traces and symbol tables that reveal internal code structure.
- The ability to trigger arbitrary-duration CPU profiling and execution tracing, which can be used as a denial-of-service vector.

**Best practices:**

- Do not expose `--pprof` on publicly reachable interfaces.
- Prefer Unix sockets or `127.0.0.1` bindings when profiling is needed.
- Enable `--pprof` only during active debugging sessions; remove the flag from production startup scripts.
- If you must expose the API externally, place `--pprof` behind an authenticating reverse proxy or VPN.

---

## 6. Quick Reference

| Goal | Command |
|------|---------|
| Start with profiling | `axis serve --pprof` |
| Load API token | `TOKEN=$(cat ~/.axis/token)` |
| CPU profile (30s) | `curl -H "Authorization: Bearer $TOKEN" -o cpu.prof http://localhost:7373/debug/pprof/profile` |
| CPU profile (custom) | `curl -H "Authorization: Bearer $TOKEN" -o cpu.prof 'http://localhost:7373/debug/pprof/profile?seconds=60'` |
| Heap snapshot | `curl -H "Authorization: Bearer $TOKEN" -o heap.prof http://localhost:7373/debug/pprof/heap` |
| Execution trace (5s) | `curl -H "Authorization: Bearer $TOKEN" -o trace.out 'http://localhost:7373/debug/pprof/trace?seconds=5'` |
| Interactive analysis | `go tool pprof cpu.prof` |
| Web UI | `go tool pprof -http=:8080 cpu.prof` |
| Trace viewer | `go tool trace trace.out` |
