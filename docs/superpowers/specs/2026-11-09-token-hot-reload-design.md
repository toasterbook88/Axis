# Hot-reload the daemon API token from ~/.axis/token

Date: 2026-11-09. Status: spec for a draft PR. Supersedes the unmerged 2026-10-27 draft, whose line
references had drifted and which missed the forwarded-origin signing key and the regenerate-on-read
trap.

Base: origin/main 01d9733 (#628). The file:line references below were taken at e49d8ab (#625);
`git diff --stat e49d8ab 01d9733 -- cmd/axis/serve.go internal/api internal/auth` is empty, so
they hold at 01d9733.

Grace window: option A (strict). The old token stops working at the first poll after the file
changes. The maintainer delegated the choice ("Choose best", 2026-11-09) and asked for the build
("Do next steps and other items", 2026-11-09, which included "build token reload").

**Auth-path note.** This change sits in the daemon's authentication path on every node. It adds no
new credential, route protection, permission check or access restriction. It only lets an existing
token be rotated without a restart.

## Problem

The daemon reads its API token once and never again, so a rotated token is ignored until restart.

- `cmd/axis/serve.go:53` calls `auth.LoadOrGenerateToken()` once. The string goes by value through
  `serveHTTPAPI` (`serve.go:45-47`, `:116`) into `api.ServeWithContext` (`internal/api/server.go:133`).
- `ServeWithContext` passes it to `registerRoutes` (`server.go:141`) and `registerPprofRoutes`
  (`:146`, `:471-476`). Every handler closes over the string through `withAuth(next, token)`
  (`:195-221`, constant-time compare at `:214`) or `withRequiredAuth` (`:479-486`). That covers the
  v1 routes (`:236-461`), the A2A task and approval routes (`:235-237`, `:559-565`), v2
  (`internal/api/v2.go:18-33`, `v2_models.go:11-14`) and pprof.
- The same string is also the HMAC key for signed forwarded execution origins:
  `auth.ForwardedExecutionOriginFromRequest(r, token, time.Now())` at `server.go:397`
  (`internal/auth/forwarded_origin.go:54-105`, max skew 5 min at `:23`). The signer is
  `internal/daemon/client.go:178`, which signs with the token it read for that call.
- Clients re-read the file on every call: `internal/daemon/client.go:33,135,156`,
  `internal/runtimectx/cache.go:212`, `cmd/axis/reservations.go:61`, `cmd/axis/agent_slash.go:214`,
  `cmd/axis/daemon.go:194`. After someone rewrites `~/.axis/token`, every new CLI call sends the new
  token and the running daemon rejects it with 401 until it restarts. Deleting the file is worse,
  because the next client call regenerates it (`internal/auth/auth.go:30-76`) and the daemon then
  rejects every client.
- `docs/authority-secrets.md:119` documents this as "Restart required" and cites
  `internal/api/server.go:187`, which is now `:195`. The summary table at `:128-129` says the API
  token is rotatable, which is only true with a restart.

## Non-goals

- No grace window: the old token is not accepted after the swap (option A). Options B (fixed
  window) and C (`axis token rotate` overlap) are rejected below.
- No new command, flag, config key, route or state file. No `axis token rotate`.
- No change to `AXIS_API_TOKEN`: when it is set it wins, exactly as `auth.go:31-33` does today, and
  it is never reloaded.
- No change to token format (64 hex chars, 32 bytes), generation, file mode or location.
- No hardening: no permission or ownership checks on the file and no new 401 cases. (The reader
  bounds its reads, §1, and re-reads on demand only when the file's stat changed, §4; neither is
  an access check.)
  The empty-token rules (F9 pass-through on a Unix socket, `server.go:197`; TCP refusal, `:137`;
  `withRequiredAuth` refusal, `:480`) keep today's behavior.
- No SIGHUP handler and no fsnotify dependency.
- Clients are not changed. Long-lived processes that cached the old token (an `AXIS_API_TOKEN`
  holder pointed at a daemon that reads the file, or a request signed just before the swap) get 401
  or a forwarded-origin signature error and must re-read. That is the strict-mode contract.
- Mesh gossip secret and UDP beacon secret are out of scope.

## Design

### 1. Read-only reader in `internal/auth`

`func readTokenFile(path string) ([]byte, string, error)` (unexported; its only caller is the
reloader) reads the file through `readTokenBytes`, trims whitespace and validates that it
hex-decodes to 32 bytes. That rule
lives in one function, `parseToken`, which `LoadOrGenerateToken` also uses (Review 2, finding 1),
so the daemon and its clients cannot disagree about which files are usable. It returns the raw
bytes too, for the change fingerprint. It never creates, regenerates, writes or locks. It returns
distinct errors for missing, empty, invalid, not-a-regular-file and too-large, so the log line can
say which.

`readTokenBytes` is the one bounded reader, shared by the reloader and the startup loader
`LoadOrGenerateToken` (Review 3, S1), so the two can never disagree about a file, including one
whose valid token is followed by more than 4 KiB of whitespace. The read is bounded (Review 2,
finding 3):
- Symlinks are followed (an operator may point `~/.axis/token` at a managed secret file), but the
  target must be a regular file. A directory, FIFO, socket or device (for example a symlink to
  `/dev/zero`) is `ErrTokenFileNotRegular`.
- The file is opened with `O_NONBLOCK`, so opening a FIFO cannot block, and the type check is an
  `fstat` on the opened descriptor, so a path swapped between check and read is still caught.
- Content is read through `io.LimitReader` with a 4096-byte cap (`maxTokenFileSize`); a longer
  file is `ErrTokenFileTooLarge`. A valid file is 64 bytes plus whitespace.
- In the reloader, both map to the unusable-file case in §3: keep the last good token and log once.
- In the loader, both (and any other open or read error) are returned as an error, under the token
  lock, without blocking. They are never regenerated: regenerating would replace what the operator
  put at the path (a symlink, directory or device) with a new file. Only ENOENT generates a token,
  and a regular file with empty or corrupt content is regenerated, both as before. So a FIFO,
  device or oversized file at `~/.axis/token` now makes `axis serve` and clients fail with an error
  where they used to hang, exhaust memory, or (oversized) accept the trimmed token.

The reloader must never call `LoadOrGenerateToken`: on an empty or corrupt file that function
generates a new token and overwrites the file (`auth.go:61-75`). A daemon doing that on a poll would
silently rotate the token under every client.

No lock is needed for reading, because `SaveToken` writes through `persist.WritePrivateFileAtomic`
(temp file plus rename, `internal/persist/privacy.go:49-54`, `recovery.go:60-100`), so a reader sees
either the old file or the new one.

### 2. `TokenSource`

In `internal/auth`:

    type TokenSource interface {
        Current() string
        Refresh() string // re-check the backing store (cheap if unchanged), return the token now in effect
    }

- `StaticToken(string)` returns a fixed value (`Refresh` returns it too). It is used when
  `AXIS_API_TOKEN` is set and in tests.
- `FileToken` holds `atomic.Pointer[string]` with the last good token.
  - It is constructed from the token already loaded at startup (first-run generation included,
    via the file half of `LoadOrGenerateToken`). Startup behavior is unchanged.
  - `Run(ctx)` polls every `tokenPollInterval = 500 * time.Millisecond`, the same interval as the
    daemon's config, state and skills watchers (`internal/daemon/daemon.go:191`,
    `watchConfigPollInterval`). The poll is a separate loop in `auth`, because `internal/daemon`
    imports `internal/auth`.
  - Change detection uses a sha256 fingerprint of the raw bytes plus an exists flag, like
    `configFingerprint` (`daemon.go:185-188`). It doesn't use mtime, which misses same-second
    rewrites on coarse-timestamp filesystems.
  - On a changed fingerprint with valid content that differs from the current token: store it, then
    `slog.Info("api token reloaded", "path", path)`.
  - On a changed fingerprint with valid content equal to the current token (rewrite with the same
    value): do nothing and don't log.
  - It stops when ctx is cancelled.
  - The interval is copied into the `FileToken` at construction (`tokenPollInterval` is a const),
    so tests shorten one instance's interval instead of mutating a package var under `-race`.
  - `poll()` holds a mutex, so concurrent polls (tests) can't interleave the bad-state bookkeeping.
    `Current()` is lock-free (`atomic.Pointer`).
  - `NewFileToken` refuses an empty initial token, so a file source is never empty. The zero
    `FileToken` is not usable (documented); `Current` deliberately does not return "" for it,
    because "" would turn `withAuthSource` into a pass-through.
  - Every read (poll or refresh) first records a `fileStamp` of the path: `stat(2)` following
    symlinks, giving device, inode, size and mtime in ns, or "missing" or "stat error". It is
    stored in an `atomic.Pointer`. The stamp is taken before the read, so a change between the two
    leaves the older stamp and causes one extra read later, never a missed change.
  - `Refresh()` (Review 3, S2) stats the path and compares with that stamp without taking the
    mutex. Unchanged: it returns `Current()` at once, with no read and no lock. Changed: it takes
    the mutex, re-checks the stamp (another caller may have just read), and only then reads. So a
    flood of wrong bearers against an unchanged file costs one `stat` each and never re-reads or
    queues behind a slow read (N1), and concurrent refreshes after one change read once. There is
    no time-based rate limit, so no caller can use up a slot that delays a real rotation. Like a
    poll, it only installs a token that passed `parseToken`.

Choosing the source is `auth.NewDaemonTokenSource(ctx)` (Review 2, finding 2). It owns the whole
decision: `StaticToken` if `AXIS_API_TOKEN` is set (the env var is read once, here), otherwise it
loads or creates `~/.axis/token`, builds a `FileToken` on `auth.TokenPath()` and starts its poller
on ctx. It returns a `*DaemonTokenSource` that wraps the source in an unexported field, forwards
`Current` and `Refresh`, and adds `WaitStopped(ctx)`, so `serve.go` drains the poller alongside the
daemon's other goroutines. `WaitStopped` returns at once when no poller was started (env var or
zero value); the zero value's `Current` panics, failing closed (Review 3, N2). The env token is
used as given, as `LoadOrGenerateToken` does; it is not run through `parseToken`, so existing
non-hex `AXIS_API_TOKEN` values keep working (Review 3, N3).
`serve.go` calls it where it used to call `LoadOrGenerateToken`, before the daemon starts and
before the "listening" banner, so a failure is reported before the daemon says it is up.

### 3. Missing, empty or corrupt file: keep the last good token, log once

- Missing (ENOENT), empty, or invalid content (not 64 hex chars), or another read error (EACCES, EIO):
  the source keeps the last good token. It logs one `slog.Warn("api token file unusable; keeping
  last good token", "path", path, "reason", <missing|empty|invalid|read error>)`.
- "Once" means once per distinct fingerprint, plus "missing" and "read error" as their own states.
  Repeated polls of the same bad state log nothing. A different bad content logs once more.
  Returning to a valid new token logs the "api token reloaded" Info line and resets the state.
  Returning to the same token that is already active logs one Info line, "api token file usable
  again; token unchanged", so the earlier warning has a visible end.
- Never log the token, any prefix of it, or a hash of it. The log carries only the path and the
  reason.
- The source never writes the file. If a client later regenerates a missing file (`auth.go:61-75`),
  the next poll picks up the new valid token. So deleting the file becomes a working rotation path,
  where today it is a fleet-wide 401.

### 4. One token per request, for both auth and forwarded-origin signing

- New `withAuthSource(next, src TokenSource)` reads `tok := src.Current()` once at the top of the
  request. It uses `tok` for the bearer compare and stores it in the request context
  (`context.WithValue` with an unexported key) before calling `next`.
- On a bearer mismatch it calls `src.Refresh()` once and compares again (Review 2, finding 4).
  Clients re-read the file on every call, so right after a rotation a client can hold the new
  token before the daemon's next poll; without the retry it would get 401 for up to 500 ms. If the
  retry matches, the refreshed token becomes the request's snapshot (so the forwarded-origin HMAC
  uses the token that authenticated the request). An empty refreshed value never matches, so the
  retry cannot open a route. A missing or malformed header does not trigger a refresh.
- The remaining 401 window after a rotation, stated exactly (Review 3, S2): `Refresh` sees any
  change to the device, inode, size or mtime of the file the path resolves to. Every atomic
  replace (temp file plus rename: `SaveToken`, the documented rotation, a regenerated file) gets a
  new inode, and a symlink retargeted to another file has a different inode, so these are accepted
  on the client's first request. The one case left to the poll (up to 500 ms) is an in-place
  rewrite of the same inode that keeps the size and lands within the filesystem's mtime
  granularity of the previous read (for example within the same second on a coarse-timestamp
  filesystem). Wrong bearers from other clients do not widen the window. A client that presents a
  token the file no longer holds (a stale client) gets 401, which is the strict contract.
- `server.go:397` reads the token from the request context (helper `requestToken(r)`) instead of
  the closed-over string. Bearer auth and forwarded-origin HMAC verification in one request
  therefore always use the same snapshot, even when a swap lands between them.
- `withRequiredAuthSource` delegates to `withAuthSource` and refuses the request if that request's
  snapshot is empty, so no source can make pprof fail open. It no longer also decides at
  registration (Review 2, nit 5): the per-request check covers it, and `ServeWithTokenSource`
  already refuses an empty token on TCP at startup. The file source can never become empty,
  because empty content is rejected in §3.
- The F9 empty-token pass-through (`server.go:197`) applies only when the source is
  `StaticToken("")`. The TCP refusal at `:137` checks `src.Current()` at startup.
- To keep test churn small (about 60 test call sites use the string signatures), the string-token
  forms `ServeWithContext`, `registerRoutes` and `registerPprofRoutes` move to
  `internal/api/token_compat_test.go` as thin wrappers that build
  `StaticToken(token)`. `withAuth` and `withRequiredAuth` had no test callers and are removed.
  Production code has only the `TokenSource` forms, so the deadcode gate
  (`hack/verify-deadcode.sh`) and staticcheck U1000 stay clean. `ServeWithTokenSource` is the production entry used by
  `serve.go`; the allowlisted `Serve` calls it with `StaticToken`. `cmd/axis/mesh_route_test.go`
  switches to `ServeWithTokenSource`. The internal plumbing (`registerRoutes`, `registerV2Routes`,
  `registerV2CoreRoutes`, `registerA2AApprovalRoutes`, `registerPprofRoutes`, the A2A wrapper at
  `:235-237`) takes a `TokenSource` (`registerRoutesSource`, `registerPprofRoutesSource`).
- `cmd/axis/serve.go`: the `serveHTTPAPI` test seam takes an `auth.TokenSource` instead of a
  string (three test stubs in `command_surface_test.go` change type only). The token source comes
  from `auth.NewDaemonTokenSource(ctx)` (§2); `cmd/axis` holds no precedence or poller logic.

### 5. Documentation

- `docs/authority-secrets.md:119`, API token row: change it to:
  - "Hot-reloaded. Write a new 64-hex token to `~/.axis/token` atomically (or delete it; the next
    client call regenerates it). The daemon swaps to it within one 500 ms poll, or at once when a
    client presents the new token, and the old token is rejected from then on (no grace window). An unusable file (missing, empty, invalid) leaves
    the last good token active and logs one warning. `AXIS_API_TOKEN`, when set, is fixed for the
    daemon's lifetime and needs a restart."
  - Hot-reloadable column: "Yes (file); No (`AXIS_API_TOKEN`)". Drop the stale `server.go:187`
    reference.
- `docs/authority-secrets.md:128-129`, summary table: the API token's in-memory lifetime becomes
  "Until the file changes (file); daemon lifetime (env var)".
- CHANGELOG entry: under Changed, "The daemon now reloads `~/.axis/token` without a restart; the old
  token is rejected immediately."

## Tests

`tokenPollInterval` is a const copied into each `FileToken` at construction. `internal/auth` tests
shorten one instance's unexported `interval` field before `Run` starts, which is race-free, and
`NewDaemonTokenSource`'s tests use the unexported `newDaemonTokenSource(ctx, interval)`, so no
test sleeps on the real 500 ms poll. The `internal/api` strict-swap test still waits on the real
poll with a 3 s deadline. The refresh tests count file reads through the `FileToken`'s
unexported `read` field. Tests use temp `AXIS_HOME` directories.

`internal/auth`:
- `readTokenFile`: valid file returns the token; missing, empty, whitespace-only, 63-char,
  non-hex and 65-char content each return the matching error. The file is byte-identical and its
  mtime unchanged afterwards (it never writes).
- `FileToken`:
  - A rewrite to a new valid token makes `Current()` return it within two poll intervals.
  - Missing, empty or corrupt content keeps the last good token. The file is not recreated or
    rewritten (regression guard against `LoadOrGenerateToken`).
  - Log once: ten polls in the same bad state produce exactly one Warn, a second distinct bad
    content produces one more, and recovery produces one Info. The captured log never contains the
    token string.
  - Same-value rewrite: no swap and no log.
  - Cancelling ctx stops the goroutine (checked with a done channel).
- Race: `go test -race` with 32 goroutines calling `Current()` while the file is rotated 100 times.
- Bounded read: a directory, a FIFO (linux), a symlink to `/dev/zero` and a 4097-byte file each
  return the mapped error within 2 s with no data, keep the last good token and log one warning; a
  symlink to a regular file with a valid token is accepted; a file of exactly 4096 bytes is read.
- Refresh: picks up a rotation without a poll; 1000 refreshes against an unchanged file cause no
  re-read; a refresh just before a rotation does not stop the next one from reading the new token;
  64 concurrent refreshes after one change read the file once; an unusable file keeps the last
  good token.
- Loader (`LoadOrGenerateToken`): a directory, FIFO (linux), symlink to `/dev/zero`, and a valid
  token followed by 4096 spaces each return the mapped error within 2 s and leave the path exactly
  as it was (same file, same mode); missing generates, a corrupt regular file regenerates, and a
  symlink to a regular file with a valid token is returned.
- `NewDaemonTokenSource`: with `AXIS_API_TOKEN` set it returns a `StaticToken`, does not touch the
  file, and `WaitStopped` returns at once; without it, it creates the file on first start, serves
  that token, reloads a rewrite, and `WaitStopped` returns after cancel; an invalid file at start
  is regenerated by the loader (and its poller is drained); a directory at the path is an error;
  the zero value's `WaitStopped` returns at once.

`internal/api`:
- Strict swap: with a `FileToken` on a temp file, the old token gets 200, then the file is
  rewritten. After the next poll the new token gets 200 and the old token gets 401 on a v1 route, a
  v2 route, an A2A task route and a pprof route.
- Per-request consistency: a test `TokenSource` that returns T1 on its first call and T2 after.
  A `/run` request with bearer T1 and forwarded-origin headers signed with T1 passes both checks,
  which proves the handler doesn't call `Current()` a second time. A request with bearer T1 and
  headers signed with T2 fails the origin check.
- Forwarded origin after rotation: headers signed with the new token verify after the swap, and
  headers signed with the old token are rejected.
- Rotated token before the next poll: with no poller running, a request bearing the rewritten
  token gets 200 at once, the old token then gets 401, and a wrong token gets 401. The refreshed
  token is the request snapshot for the `/run` forwarded-origin HMAC. A source whose refresh
  yields "" never lets a mismatched bearer through.
- Fail-closed: `withRequiredAuthSource` with a source that turns empty after startup still returns
  401; `NewFileToken` with an empty initial token returns an error.
- Regression guards (must pass unchanged): `TestServeWithContextRefusesTCPWithoutToken`
  (`internal/api/serve_auth_test.go:13`), `TestAuthentication` (`server_test.go:1284`),
  `TestPprofEndpointsRequireAuthWhenTokenSet` (`:1541`), `TestV2MetricsRequiresAuth` (`:913`), and
  every existing caller of the string-token wrappers.
- `go vet ./...`, `go test ./...` and `go run hack/verify-specs.go` pass.

## Blast radius

- Every authenticated route of the daemon on every node that runs a build with this change. Unix
  socket and TCP listeners are both affected.
- Failure modes:
  1. A swap bug that rejects the current token gives a fleet-wide 401 for that node. It fails
     closed, and a restart doesn't fix it because the bug is in code, so recovery is a revert.
  2. A swap that never happens keeps the old token, which is today's behavior.
  3. A reader bug that writes the file would rotate tokens silently. §1 and the never-writes tests
     guard this.
- Behavior change operators will notice: rewriting or deleting `~/.axis/token` now takes effect
  within about 500 ms instead of at the next restart. Any operator habit or script that edits the
  file and expects the running daemon to keep the old token changes behavior. I don't know of any
  such script; I didn't audit the fleet.
- A2A tasks are bound to `sha256("a2a-principal:" + bearer)` (`internal/a2a/handler.go:199-208`,
  `store.go:64`). After a rotation, `GET /a2a/v1/tasks/{id}` returns 404 for a task created with
  the old token, while approve and reject (no principal check, `internal/api/server.go:605,681`)
  still work. That is the strict-mode contract applied to task ownership; it is documented in
  `docs/authority-secrets.md`, not changed here. Tasks live at most 30 min (`store.go:11`).
- Cost: one goroutine and one small file read plus sha256 every 500 ms per daemon, plus one
  `stat` per request with a mismatched bearer (a read only when the file changed). Not measured;
  the existing watchers do the same work.
- Under §8.4 this is likely a larger feature (changes in `cmd/axis`, `internal/auth` and
  `internal/api`, three directories), so it needs two independent diff reviews.
- Rolling a build to nodes is a fleet roll and needs the maintainer's explicit go.

## Rollback

Revert the PR and rebuild. A restarted daemon then reads the token once at startup, exactly as
today. Nothing is persisted and the file format is unchanged, so no data migration or cleanup is
needed. The docs row reverts with the PR.

## Alternatives

- **Option B: fixed grace window.** Keep accepting the previous token for N seconds after a swap.
  Smoother for in-flight multi-step clients. Rejected: a leaked token stays valid for N more
  seconds, it needs a config key (more §8 surface), and forwarded-origin verification would have
  to try two keys. On-box clients already re-read the file per call, so the main beneficiary is
  rare.
- **Option C: explicit overlap (`axis token rotate` / `--finish`).** Operator-controlled window.
  Rejected: new subcommand (largest surface), and an unfinished rotation leaves two valid tokens
  indefinitely.
- **fsnotify watcher.** Rejected: watching a single file breaks on atomic rename-replace, which is
  exactly how `SaveToken` writes. It has no NFS/SMB/FUSE support and would add a new dependency
  (fsnotify README, below). The daemon already uses 500 ms fingerprint polling for its other files.
- **SIGHUP reload.** Rejected: it needs an operator step after every rotation, no SIGHUP handling
  exists anywhere in the repo (`rg SIGHUP` gives 0 hits on e49d8ab), and supervisors would need
  `ExecReload`/launchd equivalents.
- **mtime polling (the Oct 27 spec).** Rejected in favor of a content fingerprint: mtime misses
  same-second rewrites on coarse-timestamp filesystems, and the repo's other watchers already
  fingerprint content.
- **Re-read the file on every request.** Simplest, but it costs a file read on the hot path of every
  authenticated request and still needs the keep-last-good logic. The poll bounds that cost.

## Research

- fsnotify README (https://github.com/fsnotify/fsnotify#readme): watching individual files is "not
  recommended" because editors and atomic writers replace the file by rename, which loses the
  watch. Notifications don't work on NFS, SMB, FUSE or /proc, and a polling backend is "Not yet"
  implemented. That supports reusing the existing poll.
- google/renameio README (https://github.com/google/renameio): with temp file plus rename, readers
  never see a half-written file. That is why the reader needs no lock against `SaveToken`'s atomic
  write.
- In-repo prior art: the daemon's sha256 fingerprint watchers (`internal/daemon/daemon.go:185-191`,
  `:417-441`), which already poll `nodes.yaml`, state and skills every 500 ms.
- Optimization: none chosen beyond the poll interval, which matches the existing watchers.
  Measurement: none taken. The cost claim above is by analogy, and the race test is the
  correctness measure.

## Review

Kind: spec
Reviewer: new session (Antigravity CLI, plan mode)
Model: Google Gemini (Gemini 4 Argon high)
Base: 01d97335
Head: 125ff4d1
Date: 2026-11-09
Verdict: approve-with-changes

1. Minor: Tests said the poll interval is a package var shortened in tests while Design said it
   is a const copied per instance, and the cross-package tests can't shorten it. Fixed: Tests now
   say `internal/auth` tests set the per-instance field before `Run`, and the `internal/api` and
   `cmd/axis` tests use the real 500 ms poll with a 3 s deadline.

Questions:
- After a rotation, A2A tasks created with the old token return 404 on GET because tasks are
  bound to the bearer hash; is that intended? Verified: `internal/a2a/handler.go:199-208` and
  `store.go:64` bind the task; it is the strict-mode contract, now listed under Blast radius and
  in `docs/authority-secrets.md`. The reviewer also assumed `GET /a2a/v1/tasks/pending` lists such
  tasks; no such route is registered (`handler.go:66`, `approval.go:126-134`), which is a
  separate runbook issue.

Note: the branch was rebased onto 3a2fe9c (#629, `internal/agent` only) after this review; the spec file is byte-identical at the rebased spec commit 8b6cbc8.

Kind: diff
Reviewer: new session (Antigravity CLI, plan mode)
Model: Google Gemini (Gemini 4 Argon high)
Base: 3a2fe9cf
Head: 85503a36
Date: 2026-11-09
Verdict: approve

Note: no findings and no questions; the reviewer ran vet, race tests, coverage and the repo gates in a clone at Head. CI then found staticcheck U1000 on two unused test wrappers, fixed in 9a2a5f6 (test-only removal); a second independent diff review is still pending.

Kind: diff
Reviewer: new session (Claude Code in a herdr pane on cranium, read-only; Review 2, 2026-11-09)
Model: Anthropic Claude (exact model not reported by the reviewer)
Base: 3a2fe9cf
Head: 696254f9
Date: 2026-11-09
Verdict: approve-with-changes

1. Should-fix: `parseToken` repeated the 64-hex check inside `LoadOrGenerateToken`, so the daemon
   and clients could drift into silent 401s. Fixed: `parseToken` is the single validator and
   `LoadOrGenerateToken` calls it.
2. Should-fix: `serveTokenSource` in `cmd/axis/serve.go` re-read `AXIS_API_TOKEN`, knew auth's
   precedence and the poller lifecycle, and ran after the listening banner; the poller goroutine
   was not drained. Fixed: `auth.NewDaemonTokenSource(ctx)` owns precedence, loading and the
   poller, returns a source with `WaitStopped`, and is called before the daemon starts;
   `serveTokenSource` and its `cmd/axis` tests are gone (the tests moved to `internal/auth`).
3. Should-fix: the poll read the file with `os.ReadFile`, unbounded, every 500 ms under the mutex;
   a FIFO would hang reloads and `/dev/zero` would allocate without limit. Fixed: non-blocking
   open, `fstat` must show a regular file (symlinks followed), 4096-byte `io.LimitReader` cap, both
   mapped to keep-last-good plus one warning, with tests for a directory, FIFO, `/dev/zero` symlink
   and oversized file.
4. Should-fix: after a rotation a client that already re-read the file got 401 for up to 500 ms.
   Fixed: on a mismatch `withAuthSource` calls `Refresh()` (at most one read per 250 ms) and
   compares again, never accepting an empty token; tested without a poller, and documented in §4
   and `docs/authority-secrets.md`.
5. Nit: `withRequiredAuthSource` decided the empty-token rule at registration and per request.
   Fixed: only the per-request check remains.
6. Nit: `go ft.Run(ctx)` was outside the daemon's drain. Fixed: `serve.go` calls
   `tokens.WaitStopped(drainCtx)` after `d.WaitStopped`.
7. Nit: `cmd/axis/serve_token_test.go` slept 700 ms and used the real poll interval. Fixed: the
   tests moved to `internal/auth`, inject the interval and assert the `StaticToken` type instead
   of sleeping.
8. Nit: the exported `FileToken` zero value panics in `Current()`. Fixed: a doc comment says to use
   `NewFileToken` and why `Current` must not return "" instead.

Note: the reviewer found no must-fix and no fail-open path. The fixes are in the commits after 696254f9 on this branch.

Kind: diff
Reviewer: new session (Claude Code in a herdr pane on cranium, read-only; Review 3, 2026-11-10)
Model: Anthropic Claude (exact model not reported by the reviewer)
Base: 3a2fe9cf
Head: ac356ced
Date: 2026-11-10
Verdict: approve-with-changes

1. Should-fix (S1): the startup loader still used unbounded `os.ReadFile` with no regular-file
   check while holding the token lock, so a FIFO hung serve and every client, `/dev/zero` grew
   memory, and a valid token plus over 4 KiB of whitespace passed the loader but failed the
   reloader. Fixed: `readTokenBytes` is the one bounded reader for both; non-regular, oversized
   and unreadable files are errors and are never regenerated or replaced; only ENOENT generates
   (§1, loader tests).
2. Should-fix (S2): the docs claimed no 401 window, but any wrong bearer used up the 250 ms refresh
   slot, so a stale client or an attacker could hold the window at up to 500 ms. Fixed: the time
   slot is gone; `Refresh` compares a stat stamp (device, inode, size, mtime) and re-reads only
   when it changed, so wrong bearers cost one stat and cannot delay a rotation; the exact remaining
   window (same-inode, same-size rewrite inside mtime granularity) is stated in §4, the code
   comments and `docs/authority-secrets.md`, with tests for a wrong bearer before a rotation and a
   flood against an unchanged file.
3. Nit (N1): bad-bearer requests could queue on the mutex behind a slow read. Fixed: the
   unchanged-file path never takes the mutex.
4. Nit (N2): `DaemonTokenSource` exported its embedded `TokenSource`, and the zero value's
   `WaitStopped` blocked until ctx ended. Fixed: unexported field with forwarding methods,
   `WaitStopped` returns at once with no poller, and the zero value is documented.
5. Nit (N3): the `Refresh` contract said it never returns an unvalidated value, which
   `AXIS_API_TOKEN` is. Fixed: the comment says it never returns a value its backing store did not
   supply; the env token stays unvalidated so existing non-hex values keep working.
6. Nit (N4): the `ServeWithTokenSource` comment said the daemon passes an `auth.FileToken`. Fixed:
   it names `*auth.DaemonTokenSource`.
7. Nit (N5): the invalid-file daemon test cancelled the poller without waiting. Fixed: it calls
   `WaitStopped`.
8. Nit (N6): no test covered concurrent `Refresh` calls. Fixed: 64 concurrent refreshes after one
   change read the file exactly once (counted reads).

Note: the reviewer resolved all eight Review 2 items (the `FileToken` zero-value panic is kept as the fail-closed choice) and found no fail-open path. Review 2 item 4 above describes the 250 ms rate limit that Review 3 replaced.
