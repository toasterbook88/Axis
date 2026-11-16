# Confine auto-approved `run_shell` under `--autonomy full`

Date: 2026-11-16. Status: revision 1 (draft for spec review).

Operator decisions (2026-11-16): Landlock in-process through a re-exec helper, with bubblewrap
(`bwrap`) layered on when installed; fail closed only where commands can run without a prompt
(`--autonomy full`); writes limited to the working directory plus a private temp directory, with
network left open.

## Problem

`run_shell` is an `execTool` (`internal/agent/scope.go:87-94`) visible only in `ScopeExec`, which
`ScopeFor` maps from `AutonomyFull` alone (`internal/agent/scope.go:23-34`). In that mode
`autonomyConfirm` approves every command scored below 75 without asking
(`internal/agent/autonomy.go`; `docs/superpowers/specs/2026-11-05-full-autonomy-named-ask-design.md`).
Approved commands run through `guardedAgentShellRunner` (`cmd/axis/agent.go:512-560`) into guarded
execution, which spawns `bash -lc <command>` with the full authority of the operator's user
(`internal/execution/run_remote.go:47-82`, `RunLocalShell` / `StreamLocalShell`). The fallback
runner does the same with `sh -c` (`internal/agent/tools.go:692-719`).

Nothing between the safety scorer and the kernel limits what an auto-approved command can touch.
The scorer is pattern-based; a command it misjudges can write anywhere the user can (`~/.ssh`,
`~/.axis`, shell rc files, other repos) and can reach root-equivalent local sockets. Measured on
2026-11-16 across five maintainer Linux hosts (`id -nG`): the operator user was in the `docker`
group on all five (two also had `lxd` or `libvirt`), so `docker.sock` alone is a full host escape
from any auto-approved command.

The repository has no process confinement today: `grep -rniE 'bwrap|landlock|seccomp|unshare|sandbox-exec'`
over non-test Go at `b20ab7b0` finds nothing (the only `SysProcAttr` is `Setpgid` for the daemon).

## Non-goals

- Confining operator-confirmed surfaces: console `!cmd` (`cmd/axis/agent_console.go:262`),
  `axis_run_task` (prompts on every call), `axis run`, and anything outside `axis agent` / `axis tui`
  agent sessions. Those commands are typed or confirmed per call by the operator.
- Confining remote execution (`run_on_node`, `fleet_exec`, `remote_*`, `run_background`), which stay
  `neverTool` (`internal/agent/scope.go`) and unchanged.
- Network isolation. Landlock TCP rules (ABI 4+) and `bwrap --unshare-net` stay off by decision so
  builds, package fetches and API calls keep working.
- Read confinement. Confined commands can still read any file the user can; this spec bounds writes,
  pathname/abstract UNIX socket connects and signals, not secrecy.
- macOS `sandbox-exec` (present on a macOS 27.3 host, but deprecated) and seccomp syscall filtering.
  On hosts without a sufficient mechanism, full mode refuses `run_shell` (Design 4), and the
  operator can opt out per session (`--confine off`).
- Changing scores, rules or prompts in `internal/safety` or `autonomyConfirm`.
- Writable tool caches (`GOCACHE`, `~/.cache`, `~/.npm`). Confined commands get a private `TMPDIR`
  only; builds that must write a cache outside the working directory fail inside confinement and
  report the denied path (Design 3).

## Design

1. **New Support-tier package `internal/confine` (Linux implementation; other GOOS stubs report
   "unavailable").** Pure Go over `golang.org/x/sys/unix` (`v0.50.0`, already a dependency; it ships
   `SYS_LANDLOCK_*` and `LandlockRulesetAttr`). Add it to the AGENTS.md §4 Support row so
   `hack/verify-import-tiers.go` accepts it; `internal/execution` (Layer 4) and `internal/agent`
   (Layer 5) import it downward.
   - `Probe() Capability`: Landlock ABI from `landlock_create_ruleset(NULL, 0,
     LANDLOCK_CREATE_RULESET_VERSION)`, and whether `bwrap` is on `PATH` and can create a user
     namespace (one `bwrap --ro-bind / / true` probe, cached for the process).
   - `Capability.Sufficient()` is true when Landlock ABI ≥ 9 (filesystem writes plus
     `LANDLOCK_ACCESS_FS_RESOLVE_UNIX`), or when `bwrap` is usable (read-only root, socket
     directories masked; Design 2). Landlock ABI 1–8 alone is **not** sufficient: it cannot stop a
     connect to `/var/run/docker.sock`.
   - `Policy{Writable []string, TempDir string, Mode Mode}` with `Mode` `required` (refuse when not
     sufficient) or `off`.
   - `WithPolicy(ctx, Policy)` / `FromContext(ctx)` carry the policy to the spawn site, matching the
     existing `reportLocalPID(ctx, …)` pattern in `internal/execution/vram.go:26-30`.
   - `Command(ctx, policy, shell string, args ...string) (*exec.Cmd, error)` builds the confined
     command or returns `ErrUnavailable` with the probe summary.

2. **Re-exec helper and optional bwrap layer.**
   - `confine.MaybeRunHelper()` is the first statement of `main()` in `cmd/axis/main.go`. When
     `os.Args[1] == "__axis-confine-exec"` it decodes the policy, calls `runtime.LockOSThread`,
     `prctl(PR_SET_NO_NEW_PRIVS)`, builds a ruleset whose handled access is every write right the ABI
     supports (`WRITE_FILE`, `REMOVE_*`, `MAKE_*`, `REFER` ≥2, `TRUNCATE` ≥3, `RESOLVE_UNIX` ≥9) with
     scopes `ABSTRACT_UNIX_SOCKET | SIGNAL` (≥6), adds path-beneath rules granting those rights on each
     `Writable` directory and `TempDir` plus file rules for `/dev/null` and `/dev/tty`, calls
     `landlock_restrict_self`, and `syscall.Exec`s the shell. It never returns. This is not a Cobra
     command, so the AGENTS.md "29 top-level commands" fact and `hack/verify-doc-facts.sh` stay
     unchanged. The helper only adds restrictions; invoking it by hand grants nothing.
   - When `bwrap` is usable, `Command` prefixes the helper with
     `bwrap --ro-bind / / --dev /dev --proc /proc --tmpfs /run --ro-bind-try /run/systemd/resolve /run/systemd/resolve --bind <dir> <dir>… --unshare-pid --die-with-parent --new-session --`
     so `/run` (and the `/var/run` symlink) hides `docker.sock`, the D-Bus session socket and
     `ssh-agent` sockets under `/run/user`, while DNS keeps working through the read-only resolved
     stub. Landlock still applies inside when available.
   - Exit status, stdout/stderr streaming, `reportLocalPID` and peak RSS are unchanged: without bwrap
     the helper `exec`s into the shell under the same PID; with bwrap the reported PID is bwrap's
     and `--die-with-parent` ties the sandbox to it.

3. **Spawn sites honour the policy.** `RunLocalShell` and `StreamLocalShell`
   (`internal/execution/run_remote.go:47-82`) and `ExecuteShellWithTimeout`
   (`internal/agent/tools.go:692`) read `confine.FromContext(ctx)`. With no policy or `Mode == off`
   they behave exactly as today. With `Mode == required` they build the command through
   `confine.Command`; on `ErrUnavailable` they return an error before any process starts. When a
   confined command fails with `EACCES`/`EROFS`, the tool result keeps the shell's own error text
   (which names the path) and appends one line: `[confined: writes allowed only under <cwd> and
   $TMPDIR]`.

4. **Agent attaches the policy only for auto-approvable shell.** In `Agent.dispatchShell`
   (`internal/agent/dispatch.go:280`), when the active scope is `ScopeExec` and confinement mode is
   `auto`, the agent creates a per-call private temp dir (`os.MkdirTemp`, removed after the call),
   sets `TMPDIR` for the command, and wraps `ctx` with `Policy{Writable: [process cwd], TempDir,
   Mode: required}` before calling the shell runner.
   - The policy is attached for every `run_shell` in full mode, including commands the operator
     confirmed after a ≥75 score, so one session has one write boundary.
   - When `Probe().Sufficient()` is false, `run_shell` returns `confinement unavailable on this host
     (<summary>); run_shell is disabled under --autonomy full. Re-run with --confine off to accept
     unconfined shell, or use a lower autonomy mode.` The session continues; other tools work.
   - When the process cwd is `/` or exactly `$HOME`, `run_shell` is refused the same way (a writable
     home is no boundary).
   - `axis_run_task`, console `!cmd` and every non-exec scope never get a policy.

5. **Operator control and visibility.**
   - `axis agent --confine auto|off` (default `auto`), wired through `agent.Config.Confine`,
     `agentSessionParams` / `buildAgentSessionConfig`, and the console path `agentStartupInputs` →
     `consoleStartupJob.buildAgent`, the same two startup paths as `--eager-tools`
     (`cmd/axis/agent.go`). `axis tui` keeps its flag set and uses `auto`.
   - When a session enters `ScopeExec` (startup or `/autonomy full`), the agent emits one notice:
     `shell confinement: landlock v9 + bwrap (writes: <cwd>, $TMPDIR)`, `… landlock v10`, or
     `shell confinement: unavailable (<reason>); run_shell disabled` / `shell confinement: off
     (--confine off)`. The daily session log records the same summary once per session.

## Tests

- `internal/confine` (Linux; skips with the probed ABI when the kernel lacks the needed feature):
  - write inside `Writable` / `TempDir` succeeds; write to a sibling temp dir outside them fails;
  - with ABI ≥ 9, connecting to a UNIX listener the test creates outside the domain fails, and with
    ABI < 9 and no bwrap `Sufficient()` is false;
  - with ABI ≥ 6, `kill -0 <test pid>` from inside fails;
  - `Command` with an injected insufficient `Capability` returns `ErrUnavailable` and starts no
    process; `Mode == off` returns the plain command;
  - helper argv/policy encoding round-trips, and `MaybeRunHelper` is a no-op for other argv.
  The package and `internal/execution` / `internal/agent` tests call `confine.MaybeRunHelper()` from
  `TestMain` so the test binary can act as the helper.
- `internal/execution`: `RunLocalShell` and `StreamLocalShell` with a required policy confine writes
  (or skip on insufficient hosts); with an insufficient injected probe they return the error and run
  nothing; without a policy their argv is unchanged.
- `internal/agent`: in `AutonomyFull`, an auto-approved `run_shell` reaches the runner with a
  required policy whose `Writable` is the cwd and whose `TempDir` is removed afterwards; with an
  insufficient probe `run_shell` returns the unavailable error and the runner is never called; with
  `--confine off` no policy is attached; `AutonomyDefault` / `AutonomyEdit` and `axis_run_task` never
  attach a policy; cwd `/` or `$HOME` is refused in full mode.
- `cmd/axis`: `--confine` parses `auto|off`, rejects other values, and reaches `agent.Config` through
  both startup paths; the startup notice names the mechanism.

## Alternatives

- **bwrap only.** Strongest isolation per call, but on 2026-11-16 one of six measured Linux hosts
  had it; the other five would fail closed until it is installed, and one blocks unprivileged user
  namespaces outright. Kept as the optional outer layer instead.
- **Apply Landlock to the axis process itself.** Landlock domains are inherited and irreversible; it
  would confine the agent, daemon calls and session log writes for the rest of the session. The
  re-exec helper scopes the domain to one command.
- **Landlock alone at any ABI.** On ABI 3–4 kernels (two of the measured hosts) it leaves `docker.sock`
  connectable for a `docker`-group user, which is a host escape; treating that as "confined" would
  be a false claim, so it is classed insufficient.
- **Warn and run unconfined everywhere.** Rejected by the operator for full mode: a warning does not
  bound an auto-approved command.
- **Third-party module (`github.com/landlock-lsm/go-landlock`).** Works, but adds a dependency for
  three syscalls already exposed by `x/sys/unix`.

## Research

- Landlock ABI history: filesystem writes (ABI 1), `REFER` (2), `TRUNCATE` (3), TCP bind/connect (4),
  `IOCTL_DEV` (5), abstract UNIX socket and signal scoping (6), pathname UNIX socket
  `LANDLOCK_ACCESS_FS_RESOLVE_UNIX` (9) ([landlock(7)](https://man7.org/linux/man-pages/man7/landlock.7.html),
  [kernel docs](https://docs.kernel.org/userspace-api/landlock.html)). Prior art for the re-exec
  pattern: `landlock-lsm/go-landlock` and bubblewrap's `--die-with-parent` model.
- Host measurements, 2026-11-16, read-only probes on six maintainer Linux hosts (kernels 6.6–7.2)
  and one macOS host: Landlock ABI 10, 10, 9, 4 and 3, with one aarch64 host not listing Landlock in
  `/sys/kernel/security/lsm`; `bwrap` 0.11.0 on one host only; unprivileged user namespaces blocked
  on one (the ABI 4 host); macOS 27.3 ships `/usr/bin/sandbox-exec`.
- Throwaway spike on the ABI 9 host (Linux 7.1, x86_64, Go re-exec helper, not committed): inside the domain,
  writes to the working dir, private `TMPDIR` and `/dev/null` succeeded; writes to `$HOME`,
  `/tmp` outside `TMPDIR` and `~/.cache/go-build` failed; reads of `/etc` and `git status` in a repo
  worked; HTTPS to `example.com` returned 200 (DNS fell back from the blocked resolved varlink socket
  to the stub resolver via `/etc/nsswitch.conf` `resolve [!UNAVAIL=return] dns`); `kill -0` on the
  parent failed. Without `RESOLVE_UNIX` (bit `1<<16`) a connect to `/var/run/docker.sock` succeeded;
  with it, the connect failed. `bwrap --ro-bind / / --unshare-net` blocked TCP but still resolved
  names through the resolved socket, which is why Design 2 masks `/run` rather than relying on
  network namespaces.
- No optimization applies: confinement adds one `execve` of the axis binary (plus one bwrap
  process when installed) per `run_shell`; measured cost is reported in the implementation PR.

## Review
