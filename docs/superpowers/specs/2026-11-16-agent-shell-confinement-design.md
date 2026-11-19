# Confine auto-approved `run_shell` under `--autonomy full`

Date: 2026-11-16; revision 2: 2026-11-19.
Status: revision 2 — approved (strict combined contract adopted 2026-11-19; review findings resolved; ready for implementation).

## Decision history

The recorded 2026-11-16 choice was an in-process Landlock re-exec helper, with bubblewrap
(`bwrap`) added when installed; full-mode shell would fail closed on an insufficient host, allow
writes beneath cwd and a private temp directory, and leave network access open. That choice is
preserved here in the decision record.

The completed review of revision 1 returned **revise**. Its findings required a stronger contract
to retain the promised filesystem modification and UNIX-socket boundaries. On **2026-11-19**, the
operator adopted the **strict combined contract** for revision 2: require BOTH Landlock ABI >= 9
and a usable read-only `bwrap` layer for every confined full-mode `run_shell`, failing closed with
no automatic single-layer or unconfined fallback, even though this deliberately reduces
availability. No implementation starts until a fresh-context §8 spec review approves revision 2.

## Problem

`run_shell` is an `execTool` (`internal/agent/scope.go:87-94`) visible only in `ScopeExec`, which
`ScopeFor` maps from `AutonomyFull` alone (`internal/agent/scope.go:23-34`). In that mode
`autonomyConfirm` approves every command scored below 75 without asking
(`internal/agent/autonomy.go`; `docs/superpowers/specs/2026-11-05-full-autonomy-named-ask-design.md`).
Approved commands run through `guardedAgentShellRunner` (`cmd/axis/agent.go`) into guarded
execution, which spawns `bash -lc <command>` with the full authority of the operator's user
(`internal/execution/run_remote.go:47-82`, `RunLocalShell` / `StreamLocalShell`). The fallback
runner does the same with `sh -c` (`internal/agent/tools.go`, `ExecuteShellWithTimeout`). The guarded
runner normally tries daemon HTTP first; caller context values are not serialized with that request.

Nothing between the safety scorer and the kernel limits what an auto-approved command can touch.
The scorer is pattern-based; a command it misjudges can modify unrelated user files and reach
privileged local UNIX sockets where the user has access. These are generic threats, not assertions
about any maintainer host. At base `b55931b8`, the local shell spawn sites above contain
no per-command filesystem, socket or signal confinement.

## Non-goals

- Confining operator-confirmed surfaces: console `!cmd` (`cmd/axis/agent_console.go:262`),
  `axis_run_task` (prompts on every call), `axis task run`, and anything outside `axis agent` / `axis tui`
  agent sessions. Those commands are typed or confirmed per call by the operator.
- Confining remote execution (`run_on_node`, `fleet_exec`, `remote_*`, `run_background`), which stay
  `neverTool` (`internal/agent/scope.go`) and unchanged.
- Network or read isolation. Existing IP networking, including loopback TCP, remains available;
  no TCP/UDP rules or network namespace are introduced. Readable files and inherited environment
  values may contain credentials. Commands can read those credentials and use authenticated TCP
  APIs to modify remote or local services, including services with filesystem authority. This
  proposal provides a local kernel filesystem/UNIX-socket/signal boundary, not secrecy or protection
  against authority exercised through those APIs. This limitation must be visible to the operator.
- macOS or other non-Linux confinement, and seccomp syscall filtering. Unsupported hosts refuse
  confined full-mode `run_shell`; an operator can explicitly start a session with `--confine off`.
- Complete device, IPC or denial-of-service containment. Standard streams and the private minimal
  device filesystem are limited exceptions described below. This is not a general hostile-code VM.
- Inode-alias isolation. A file writable through cwd may have pre-existing hard links or mount
  aliases elsewhere; changing that file can affect those aliases. The boundary restricts filesystem
  paths and mount views, not every other name for an allowed inode.
- Changing scores, rules or prompts in `internal/safety` or `autonomyConfirm`.
- Writable tool caches (`GOCACHE`, `~/.cache`, `~/.npm`). Confined commands get a private `TMPDIR`
  only; builds that must write a cache outside the working directory fail inside confinement with
  the command's own error and the confinement notice (Design 3).

## Design

1. **New Support-tier package `internal/confine` (Linux implementation; other GOOS stubs report
   "unavailable").** Pure Go over `golang.org/x/sys/unix` (`v0.50.0`, already a dependency; it ships
   `SYS_LANDLOCK_*` and `LandlockRulesetAttr`). Add it to the AGENTS.md §4 Support row so
   `hack/verify-import-tiers.go` accepts it; `internal/execution` (Layer 4) and `internal/agent`
   (Layer 5) import it downward.
   - `Probe() Capability` reports the Landlock ABI and whether the pinned bwrap executable can
     establish the required user/mount/PID namespaces, read-only root and writable bind layout.
     Presence on `PATH` or a successful `bwrap ... true` alone does not establish sufficiency.
   - `Capability.Sufficient()` requires BOTH ABI >= 9 and that usable bwrap layer. Landlock-only,
     bwrap-only, ABI < 9, missing/disabled mechanisms and unsupported platforms are insufficient.
     Runtime ruleset or mount failure also refuses the command; cached availability cannot turn
     a failed setup into an unconfined retry. A cached probe is tied to pinned executable identity.
   - `Policy{Writable []string, TempDir string, Mode Mode}` with `Mode` `required` (refuse when not
     sufficient) or `off`.
   - `WithPolicy(ctx, Policy)` / `FromContext(ctx)` carry the policy to the spawn site, matching the
     existing `reportLocalPID(ctx, …)` pattern in `internal/execution/vram.go:26-30`.
   - `Command(ctx context.Context, policy Policy, baseEnv []string, shell string, args ...string) (*exec.Cmd, error)`
     builds the confined command or returns `ErrUnavailable` with the probe summary. It clones
     `baseEnv` (or `os.Environ()` if `nil`), strips loader/bootstrap env vars (`LD_*`, `GCONV_PATH`,
     `LOCPATH`, `HOSTALIASES`, `MALLOC_TRACE`, `RESOLV_HOST_CONF`, `TMPDIR`) before spawning
     `bwrap`, and sets `TMPDIR` to canonical `policy.TempDir` without mutating process-global env.

2. **Mandatory bwrap layer and pinned re-exec helper.**
   - Before the first untrusted shell call, retain an executable image of the running axis process
     opened through `/proc/self/exe`, not a pathname returned by `os.Executable` or a later `PATH`
     lookup. Preserve its identity for the session, including when the installed pathname is
     replaced, renamed or unlinked between calls. The concrete candidate is a sealed executable
     memfd snapshot, with write/grow/shrink/seal seals, mounted read-only at a sandbox-private
     helper path. Open an independent read-only file descriptor per `confine.Command` call via
     `/proc/self/fd/<mfd>` (closed in the parent after child start or setup failure) so up to 6
     concurrent `run_shell` calls (`internal/agent/agent.go:470`) never race on a shared open file
     description offset when `bwrap --ro-bind-data` reads the helper image. Seal or
     executable-memory policy failure is unavailable, never pathname fallback.
     The bwrap image used for probing and spawning is likewise pinned before shell execution;
     bootstrap loader/environment inputs must not be replaceable through writable policy roots.
     Close bootstrap image/policy/root descriptors before the payload shell starts.
   - `confine.MaybeRunHelper()` is the first statement of `main()` in `cmd/axis/main.go`. When
     `os.Args[1] == "__axis-confine-exec"` it decodes the policy, calls `runtime.LockOSThread`,
     `prctl(PR_SET_NO_NEW_PRIVS)`, and installs an explicitly vetted ABI-9 ruleset: filesystem
     content/create/remove/rename/truncate rights plus `RESOLVE_UNIX` and `IOCTL_DEV`, with
     `ABSTRACT_UNIX_SOCKET | SIGNAL` scopes. Grant ordinary filesystem mutation rights only below
     the validated cwd and private temp roots. Do NOT grant `RESOLVE_UNIX` on those roots: an
     existing host socket inside cwd must remain blocked. Same-domain child sockets may work.
     Add only a `WRITE_FILE` exception for the private `/dev/null`; ordinary reads of the minimal
     private devices remain possible. Do not grant general device-ioctl authority or grant mutation
     rights to the whole device tree. Newer ABI bits are not silently enabled.
     After successful restriction it execs the shell; on any error it exits without shell execution.
     This internal entry point does not add a Cobra command. Invoking it manually grants nothing.
   - Required layout: recursively read-only host root, writable binds only for validated cwd and
     private temp, private minimal `/dev`, private `/proc` under a PID namespace, and an empty
     private `/run`. Use `--unshare-pid --die-with-parent --new-session` and no network unshare.
     Preserve usable DNS resolver files: when host `/etc/resolv.conf` resolves through a symlink
     into `/run` (e.g. `../run/systemd/resolve/stub-resolv.conf`), read its regular-file bytes
     before spawn and supply them read-only at `/etc/resolv.conf` via `--ro-bind-data`; never bind
     host `/run` directories or UNIX sockets. Resolver incompatibility is reported as a command
     failure. `/run` masking is defense in depth; Landlock provides the pathname/abstract socket
     boundary at all locations.
   - Read-only mounts supply the missing metadata boundary: outside allowed roots, content writes,
     chmod, chown, xattr and timestamp changes through host filesystem paths must fail. Nested
     mounts must also be read-only; failure to establish that layout is unavailable. No host
     device directory is bound into the sandbox. Only private pseudo-filesystems and required
     harmless minimal devices are exceptions to the host-path filesystem statement.
   - Stdin is `/dev/null` or a parent-managed pipe; stdout/stderr are parent-managed pipes, never
     inherited host terminals or arbitrary preopened writable files/sockets. Close all other
     inherited descriptors after setup. Output still reaches the parent, which can log it outside
     cwd; it is not sanitized or secret. No blanket `/dev/tty` write/ioctl exception is granted.
     This descriptor discipline is required because preopened file/socket authority is not revoked
     by a new path ruleset. Device and special-stream behavior is limited, not a complete device
     security policy.
   - Preserve exit status and stdout/stderr streaming. The reported PID is bwrap's; cancellation
     must terminate/reap the payload tree, including background children, before temp cleanup.
     Peak RSS attribution must be tested and documented rather than assumed identical through
     the extra namespace/reaper processes.

3. **Spawn sites honour the policy.** `RunLocalShell` and `StreamLocalShell`
   (`internal/execution/run_remote.go:47-82`) pass `env` directly into `confine.Command` (never
   overwriting `cmd.Env = env` afterwards) and `ExecuteShellWithTimeout`
   (`internal/agent/tools.go:872-899`) passes `os.Environ()`. With no policy or `Mode == off`
   they behave exactly as today. With `Mode == required` they build the command through
   `confine.Command`; on `ErrUnavailable` they return an error before any payload starts. Trusted
   setup/probe processes may start, but no user shell starts before both layers succeed. Because
   `cmd.Wait()` returns `*exec.ExitError` rather than kernel `errno`, on non-zero exit under
   `Mode == required` when captured output contains permission/read-only failure markers
   (`Read-only file system`, `Permission denied`, `Operation not permitted`, `EROFS`, `EACCES`),
   all three shell runners append `[confined: host filesystem mutations limited to <cwd> and $TMPDIR;
   reads/network remain available]` to the tool result (preserved inside `GuardedExecutionResult`
   by `guardedAgentShellRunner` at `cmd/axis/agent.go:595-607`), without fabricating an unprinted
   denied pathname.
   - **Skip optional Nix wrapper under confinement.** In `PrepareExecution`
     (`internal/execution/prepare.go:432`), when `confine.FromContext(ctx)` carries
     `Mode == required`, skip optional `PlanNixExecution` wrapping and append reasoning note
     `"nix ephemeral wrapper skipped under shell confinement"`, rather than opening host Nix
     daemon sockets or breaking ordinary tools (`go`, `git`, `python3`) on NixOS hosts.
   - **Required policy bypasses daemon HTTP.** In `guardedAgentShellRunner`, branch on the required
     policy before `tryGuardedAgentShellViaDaemon`, then call the in-process guarded execution path
     with the same context. Preserve safety checks, canonical local-target checks, reservations,
     owner/provenance and cleanup; this is not direct shell execution around guarded execution.
     No daemon probe or request is made for that call, and neither local failure nor setup failure
     falls back to daemon HTTP, remote dispatch or an unconfined runner. No protocol change is
     proposed. Calls without a required policy retain existing daemon-first behavior.

4. **Agent attaches the policy only for auto-approvable shell.** In `Agent.dispatchShell`
   (`internal/agent/dispatch.go:338-339`), when the active scope is `ScopeExec` and confinement mode is
   `auto`, the agent creates a per-call private temp dir (`os.MkdirTemp`, removed after the call)
   and wraps `ctx` with `Policy{Writable: [process cwd], TempDir, Mode: required}` before calling
   the shell runner (`confine.Command` sets `TMPDIR` cleanly in the child `cmd.Env` without
   mutating process-global env). Expose an `internal/agent` probe-stub seam (`AGENTS.md §7` restore
   pattern) so unit tests run deterministically on non-ABI-9 hosts; update existing non-confinement
   `ScopeExec`/`AutonomyFull` stub-runner tests (`ask_always_test.go`,
   `sessionlog_compaction_test.go`, `scope_test.go`, `deferred_tools_test.go`,
   `session_approval_test.go`) to use explicit `Confine: ConfineOff` or a stubbed sufficient probe.
   - The policy is attached for every `run_shell` in full mode, including commands the operator
     confirmed after a ≥75 score, so one session has one write boundary.
   - When `Probe().Sufficient()` is false or root validation fails, `run_shell` returns `confinement
     unavailable on this host (<summary>); run_shell is disabled under --autonomy full. Re-run
     with --confine off to accept unconfined shell, or use a lower autonomy mode.` The session
     continues; other tools work.
   - Canonicalize cwd, the session's operator HOME and every allowed root, including TempDir,
     using absolute existing directory paths with symlinks resolved. Refuse missing, ambiguous
     or unresolvable roots/HOME. Reject filesystem root, any allowed root equal to HOME or an
     ancestor of HOME, and any canonical root equal to or beneath `/dev`, `/proc`, `/run`, or
     `/sys` (whose private mounts replace or conflict with host paths), using path components rather
     than string-prefix comparison. A repo beneath HOME may be allowed; granting its ancestor must
     not grant all of HOME. Pin validated directory identities through setup and reject
     symlink/rename/mount identity changes; never validate a spelling and then silently bind a
     different directory. Use simulated HOME trees in tests.
   - `axis_run_task`, console `!cmd` and every non-exec scope never get a policy.

5. **Operator control and visibility.**
   - `axis agent --confine auto|off` (default `auto`; empty `agent.Config.Confine` defaults to `auto`),
     wired through `agent.Config.Confine`, `agentSessionParams` / `buildAgentSessionConfig`, and
     the console path `agentStartupInputs` → `consoleStartupJob.buildAgent`, the same two startup
     paths as `--eager-tools` (`cmd/axis/agent.go`). `axis tui` keeps its existing flag set and uses
     `auto`.
   - When a session enters `ScopeExec` (startup or `/autonomy full`), evaluate BOTH
     `Probe().Sufficient()` AND Design 4's canonical root validation (`cwd`, `HOME`, forbidden mount
     trees) **before** emitting one notice:
     `shell confinement: landlock ABI <n> + read-only bwrap (host mutations: <cwd>, $TMPDIR;
     reads/credentials/network remain available)`, or
     `shell confinement: unavailable (<reason>); run_shell disabled` / `shell confinement: off
     (--confine off)`.
     Route the notice cleanly by surface (never writing into `agent.Config.Output` in `-p` or TUI):
     to `errW` (`cmd.ErrOrStderr()`) for `axis agent -p`, plain REPL, and one-shot runs so stdout
     stays clean; to `j.note(console.StatusInfo|StatusFailed, notice)` for Bubble Tea console and
     `axis tui` (`cmd/axis/agent_console_startup_job.go:340-379`); and to `session.Out` right after
     switching mode in `/autonomy full` (`cmd/axis/agent_slash.go:150-165`). Record the same summary
     once per session via `DailySessionLog.LogConfinement(summary string)`
     (`internal/agent/sessionlog.go`, JSONL event `"session.confinement"` with `summary`,
     `autonomy: "full"`, `session_id`, `ts`). There is no Landlock-only "confined" notice. Explicit
     off means unconfined and is never selected by a tool, a failed probe, a confirmation response
     or an automatic retry.

## Tests

- `internal/confine` (all mutation/listener targets are synthetic temporary fixtures):
  - an injected matrix accepts only ABI >= 9 AND usable bwrap; every other combination refuses;
  - `Command` strips loader/bootstrap env vars (`LD_PRELOAD`, `LD_LIBRARY_PATH`, `GCONV_PATH`, etc.)
    from `baseEnv` / `os.Environ()` while setting canonical `TMPDIR`, and concurrent 6-way
    confined invocations open independent `/proc/self/fd/<mfd>` descriptors under `-race` without
    file-offset corruption;
  - combined-layer integration: content write, truncate, chmod, permitted-owner chown, setxattr
    and utime succeed on suitable fixtures inside allowed roots and fail on outside siblings;
    check outside content, mode, owner, xattrs and timestamps afterwards, not only exit status;
  - symlinked `/etc/resolv.conf -> ../run/...` contents remain readable inside the sandbox via
    `--ro-bind-data` without exposing host `/run` directories or UNIX sockets;
  - peak-RSS attribution through `bwrap`/reaper processes is measured and verified against direct
    process usage rather than assumed identical;
  - a writable nested mount outside allowed roots stays read-only; missing privileges to construct
    this fixture are a reported skip, never evidence the nested-mount boundary passed;
  - connect/send to pathname UNIX listeners created by the parent is denied outside roots,
    outside `/run`, and inside cwd; parent-created abstract socket connect is denied too;
    same-domain listeners remain usable; `kill -0` of a parent process fails;
  - `Command` with an injected insufficient `Capability` returns `ErrUnavailable` and starts no
    payload; failures during mount/ruleset/helper setup never execute a payload marker or retry
    with one layer, daemon or a plain shell; explicit `Mode == off` returns the plain command;
  - filesystem root, simulated HOME, its ancestor, symlink aliases, and paths equal to or beneath
    `/dev`, `/proc`, `/run`, and `/sys` are rejected; a component sibling such as `home-other` is not
    mistaken for HOME; a repo beneath HOME is allowed; replaced/renamed roots during setup fail
    closed rather than widening the grant;
  - replace/unlink the launched axis pathname between two shell calls, put a fake helper at the
    old path and change PATH: the second call still uses the original pinned helper image and
    denies an outside mutation, or refuses before payload execution. Repeat for bwrap identity;
  - preopened outside writable files/sockets/TTY descriptors do not reach the payload; stdio
    pipes, `/dev/null`, minimal private devices and descriptor cleanup behave as documented;
  - read a synthetic credential file/environment value and contact a temporary loopback TCP
    server successfully, demonstrating the declared read/network authority without real credentials;
  - background descendants are terminated and reaped on cancel/timeout before TempDir removal;
  - helper argv/policy encoding round-trips, and `MaybeRunHelper` is a no-op for other argv.
  `internal/confine`, `internal/execution`, `internal/agent`, and `cmd/axis/main_test.go` call
  `confine.MaybeRunHelper()` from `TestMain` so test binaries act as the helper. Integration tests
  require both mechanisms; unsupported environments report exact skip reasons. Mocked refusal tests
  do not skip. Before claiming the contract verified, run the combined integration suite on a
  capable isolated Linux environment and retain its output.
- `internal/execution`: `RunLocalShell` and `StreamLocalShell` with a required policy confine writes
  (or skip on insufficient hosts), preserve caller `env` while keeping scrubbed `LD_*`/`TMPDIR`,
  append the `[confined: ...]` notice on permission/read-only non-zero exit without fabricating a
  path, and skip `PlanNixExecution` in `PrepareExecution` when `Mode == required` with `nix` in
  `NodeFacts.Tools` (emitting `"nix ephemeral wrapper skipped under shell confinement"`); with an
  insufficient injected probe they return the error and run nothing; without a policy their argv is
  unchanged.
- `internal/agent`: in `AutonomyFull` (with stubbed sufficient probe where needed on non-ABI-9 hosts,
  and existing non-confinement `ScopeExec` tests updated to `ConfineOff`/stubbed probe), an
  auto-approved `run_shell` reaches `ExecuteShellWithTimeout` with a required policy whose
  `Writable` is cwd and whose `TempDir` is removed afterwards, appends `[confined: ...]` on
  permission/read-only exit errors, and scrubs loader env vars; with an insufficient probe
  `run_shell` returns the unavailable error and the runner is never called; with `--confine off` no
  policy is attached; `AutonomyDefault` / `AutonomyEdit` and `axis_run_task` never attach a policy;
  invalid/HOME/`/run`/`/dev`/`/proc`/`/sys` roots are refused; `DailySessionLog.LogConfinement`
  writes `session.confinement` once per session.
- `cmd/axis`: `--confine` parses `auto|off`, rejects other values, and reaches `agent.Config` through
  both startup paths; startup in valid cwd emits the combined notice on `errW` in `-p` mode
  (leaving stdout clean) and via `j.note` in console/TUI; starting in `$HOME` emits `unavailable`;
  `/autonomy full` emits the confinement notice on `session.Out`. With a reachable stub daemon, a
  required policy never invokes daemon metadata/execution clients: it reaches in-process guarded
  execution, preserves `[confined: ...]` inside `GuardedExecutionResult`, and the actual confined
  spawn denies a sibling write (via `cmd/axis/main_test.go` `MaybeRunHelper`). Inject local/setup
  errors and assert no daemon, remote or plain-shell fallback. Keep the existing daemon-first
  regression for no-policy calls. Stub safety/reservation/local-target seams separately to prove that
  bypassing HTTP preserves guarded execution rather than bypassing its checks.

## Alternatives

- **bwrap only.** Rejected for this proposed contract: read-only mounts and `/run` masking do not
  prevent all host pathname or abstract UNIX socket access while network remains shared.
- **Apply Landlock to the axis process itself.** Landlock domains are inherited and irreversible; it
  would confine the agent, daemon calls and session log writes for the rest of the session. The
  re-exec helper scopes the domain to one command.
- **Landlock alone.** ABI < 9 misses the required pathname socket control. Even ABI >= 9 does not
  enforce chmod/chown/xattr/timestamp restrictions; it cannot support the filesystem modification
  promise alone. A weaker content-only contract would need a new operator choice and revised
  notices, non-goals and tests before review; it is not an accepted fallback in this proposal.
- **Carry policy over daemon HTTP.** A possible future authenticated, enforced protocol change,
  but larger than this local-shell proposal. In-process guarded execution avoids losing context
  policy at the current serialization boundary without granting new transport authority.
- **Warn and run unconfined everywhere.** Rejected by the operator for full mode: a warning does not
  bound an auto-approved command.
- **Third-party module (`github.com/landlock-lsm/go-landlock`).** Works, but adds a dependency for
  three syscalls already exposed by `x/sys/unix`.

## Research

- Primary sources, consulted for this proposed revision: [Linux Landlock documentation](https://docs.kernel.org/userspace-api/landlock.html)
  documents metadata syscall limitations and retained authority on preopened descriptors;
  [landlock(7)](https://man7.org/linux/man-pages/man7/landlock.7.html) documents ABI 9 pathname
  socket support, ABI 6 scopes and inherited rulesets. These limitations motivate requiring both
  mechanisms rather than treating an ABI number as the complete boundary.
- [bubblewrap security model](https://github.com/containers/bubblewrap#sandbox-security) makes
  the caller responsible for its argument policy. Its [manual](https://github.com/containers/bubblewrap/blob/main/bwrap.xml)
  describes read-only mounts, private devices, parent-death/session handling and read-only files
  supplied from descriptors. These are building blocks, not evidence the combined AXIS policy works.
- [memfd_create(2)](https://man7.org/linux/man-pages/man2/memfd_create.2.html) and
  [execveat(2)](https://man7.org/linux/man-pages/man2/execveat.2.html) provide sealed anonymous-image
  and descriptor-based execution building blocks. Executable-memory policy and bwrap compatibility
  must be established by implementation tests; a mutable executable pathname is not a safe substitute.
- Synthetic capability matrix (requirements, **not observed host inventory**):

  | Synthetic case | ABI | Required bwrap layout usable | Proposed result |
  | --- | --- | --- | --- |
  | A | unavailable | either | refuse |
  | B | 1–8 | yes | refuse |
  | C | >= 9 | no | refuse |
  | D | >= 9 | yes | attempt both; refuse on setup failure |

- No fleet measurements or host-correlated spike results are published here. No combined-layer
  acceptance or performance measurement was performed for this drafting revision. Confinement is
  chosen for authority reduction, not a speed optimization; setup latency, memory and peak-RSS
  attribution must be measured in the implementation PR before making performance claims.

## Review

Kind: spec
Reviewer: fresh-context reviewer (supplied completed report; harness not recorded)
Model: OpenAI
Base: fe94db6a881cc6709e8a2d305ae63c6ee0ebdf87
Head: a987874b9694e90466a523bb7c5f5108090f3ecb
Date: 2026-11-18
Verdict: revise

1. Blocker: caller context policy does not reach daemon HTTP execution. Fixed in
   0b02d491ee377b1fc3167f40e84d0a7231f9c536 (proposed spec text only): Design 3 requires in-process
   guarded execution and daemon-available no-fallback regressions; implementation is pending.
2. Blocker: bwrap-only acceptance leaves host UNIX sockets reachable. Fixed in
   0b02d491ee377b1fc3167f40e84d0a7231f9c536 (proposed spec text only): Design 1 requires ABI >= 9
   AND bwrap; Design 2 denies host socket grants even under cwd; implementation is pending.
3. Major: Landlock-only acceptance leaves filesystem metadata mutable. Fixed in
   0b02d491ee377b1fc3167f40e84d0a7231f9c536 (proposed spec text only): Design 2 requires read-only
   mounts and Tests cover metadata changes outside allowed roots; implementation is pending.
4. Blocker: the public document included real maintainer fleet inventory. Fixed in
   0b02d491ee377b1fc3167f40e84d0a7231f9c536: Problem, Alternatives and Research now use generic
   threats, primary docs and a synthetic matrix; existing Git history was not rewritten.
5. Major: an allowed ancestor of HOME grants access to HOME. Fixed in
   0b02d491ee377b1fc3167f40e84d0a7231f9c536 (proposed spec text only): Design 4 canonicalizes and
   rejects root/HOME-containing roots; Tests cover ancestors and aliases; implementation is pending.

Questions:
- How is helper identity preserved across executable pathname replacement? Open: Design 2 proposes
  pinned executable images and replacement/unlink regressions, but implementation and those
  regressions do not exist yet; fresh review must assess the proposed mechanism.

Note: This is the supplied completed revision-1 review, not approval of revision 2. Its isolated
  probes support its findings, not acceptance of the new combined policy. The operator adopted the
  strict combined contract on 2026-11-19; a new fresh-context review against the committed
  revision-2 SHA remains pending. The drafter did not rerun or invent review probes. Private host
  inventory and artifact locations are omitted.

Kind: spec
Reviewer: fresh-context subagent ec892853-1d64-4192-93e0-ce91a7d13015
Model: gemini
Base: b55931b81192ec3d4d39541dfb498257ca12864a
Head: 3ba204a916a308771028b646d618166b13c47474
Date: 2026-11-19
Verdict: approve-with-changes

1. Major: `confine.Command` signature collided with `RunLocalShell`/`StreamLocalShell` env handling and loader scrubbing because `ld-linux` loads dynamic `bwrap` before `--unsetenv` and `cmd.Env = env` overwrote scrubbed env vars. Design 1 and Design 3 now pass `baseEnv []string` into `confine.Command` to clone and strip `LD_*`/bootstrap vars and set `TMPDIR` without process-global mutation. fixed in 99ebc3c587a4c41873cad35611fda000bffdfe70.
2. Major: defaulting empty `agent.Config.Confine` to `auto` broke five existing non-confinement `internal/agent` `ScopeExec`/`AutonomyFull` stub-runner unit tests on `ubuntu-latest` CI and non-ABI-9 hosts. Design 4 and Tests now specify an `internal/agent` probe-stub seam and explicit `ConfineOff`/stubbed probe updates for those suites. fixed in 99ebc3c587a4c41873cad35611fda000bffdfe70.
3. Major: `ScopeExec` entry notice ignored canonical root validation (misleading in `$HOME`) and lacked routing for `-p`, console/TUI, `/autonomy full`, and `DailySessionLog`. Design 5 now runs canonical root validation before emitting notices, routes by surface (`errW`, `j.note`, `session.Out`) without touching `agent.Config.Output`, and adds `DailySessionLog.LogConfinement`. fixed in 99ebc3c587a4c41873cad35611fda000bffdfe70.
4. Major: `Tests` omitted `cmd/axis/main_test.go` `MaybeRunHelper()` wiring and explicit coverage for peak RSS, `[confined: ...]` permission-exit detection via `*exec.ExitError` / `GuardedExecutionResult`, `LD_*` env scrubbing, symlinked `/etc/resolv.conf` preservation, and notice/`DailySessionLog` routing. Design 2, Design 3, and Tests now cover all of these explicitly. fixed in 99ebc3c587a4c41873cad35611fda000bffdfe70.
5. Minor: Non-goals claimed confined cache-write failures "report the denied path", contradicting Design 3's rule not to invent unprinted paths. Non-goals now specifies failing with the command's own error and the confinement notice. fixed in 99ebc3c587a4c41873cad35611fda000bffdfe70.
6. Minor: root validation did not reject `cwd` equal to or beneath `/dev`, `/proc`, `/run`, or `/sys` despite private/empty mounts hiding or conflicting with them. Design 4 and Tests now reject canonical `cwd` at or beneath `/dev`, `/proc`, `/run`, and `/sys`. fixed in 99ebc3c587a4c41873cad35611fda000bffdfe70.

Questions:
- When `nix` is observed in `NodeFacts.Tools`, should confined `run_shell` skip `PlanNixExecution` wrapping (`internal/execution/prepare.go:432`) or fail closed? Design 3 and Tests specify skipping optional `PlanNixExecution` when `Mode == required` and recording `"nix ephemeral wrapper skipped under shell confinement"`. verified: Design 3 documents skipping the wrapper in `PrepareExecution` rather than punching holes for Nix daemon sockets.
- Is reusing a single sealed `memfd` `*os.File` safe across up to 6 concurrent `run_shell` calls (`internal/agent/agent.go:470`) with `bwrap --ro-bind-data`? Design 2 and Tests now require opening an independent read-only file descriptor per `confine.Command` call via `/proc/self/fd/<mfd>` (closed in parent after start/error) and testing 6-way concurrent invocations under `-race`. verified: Design 2 avoids any shared open-file-description offset race.

Note: Isolated ABI-9 Landlock + `bwrap 0.11.0` + sealed memfd probes verified core filesystem, pathname/abstract UNIX socket, signal, `/dev/tty`, and `memfd` re-exec feasibility at `3ba204a9`. All six findings and both open questions are resolved in `99ebc3c587a4c41873cad35611fda000bffdfe70`, completing §8 spec approval prior to implementation.
