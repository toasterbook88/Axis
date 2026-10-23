# Private agent session logs

`axis agent` writes a local evaluation corpus beneath `~/.axis/logs` (or the
configured AXIS state root). Embedded callers log only when `Config.SessionLog`
is explicitly supplied. Logging never publishes to Cortex, lifecycle events,
HTTP or MCP. Existing interactive resume/history behavior is unchanged.

Directories are owner-only (`0700`) and segment/lock files are owner-only
(`0600`). Directory identity is pinned at construction and verified before each write;
a later replacement cannot redirect output. Symlinked log directory leaves, nonregular segment/lock files and
hard-linked segment/lock files are rejected; parent aliases (including macOS `/var` and `/tmp`) are resolved,
and filesystem access stays inside an `os.Root`. This is access
control, not encryption. The account owner and privileged local users can read
logs. Keep the state directory on a suitably protected volume.

## Captured evidence

Every record has a random process session ID, turn number, timestamp, the local
start day of the turn, configured model, autonomy, platform, AXIS version,
build commit/date when available, and local context. The context includes the
working directory, hostname, project HEAD and dirty status if bounded git
probes succeed (run with `core.fsmonitor` disabled, so no repository-configured
command runs), and the selected provider/protocol/node/model. `captured_at`
identifies when project context was measured at session startup; later file or
Git changes do not rewrite it. A resolved `/model` choice updates target
attribution. Missing fields mean unknown. Provider selection is configured
routing provenance; a backend that internally reroutes a request does not expose
its per-request resolved model through this logger.

* `turn.started`: the prompt, stored once before inference.
* `assistant`: intermediate assistant text accompanying tool calls.
* `tool`: calls/arguments, approval decisions (`event: approval` with the
  tool-call `id` and `decision` approved, approved_for_session, denied or blocked; the result may
  come from an autonomy policy rather than the operator), approval skips, shell commands, success/failure,
  result size, timing, and a separate `output` event with result content.
  Nonempty partial output is retained with `ok: false` when a tool returns an error.
* `turn.finished`: the final answer once, elapsed time, token deltas from backend
  usage reports, and `answer`, `max_turns`, `error`, `canceled` or `timeout` outcome.
  `usage_reported` is always present on this record: `false` means no nonzero
  usage was reported for the turn, so absent token counts are unknown, not zero.
  Backends carry usage as counts where zero means "not reported", so an explicit
  0/0 report is also recorded as `false`.

Tool results are recorded before conversation compaction can remove them. The
logger never recopies the entire conversation or system prompt. Token counts
are backend reports; omitted/zero usage is not an independent measurement of
zero cost. Stream failures record the error/outcome; the current backend
interface does not return partially streamed answer text on failure.

Credential-shaped assignments (including dotted/underscored names and nested
JSON string fields, decoded recursively before masking), authorization/API-key/cookie headers, URL userinfo and PEM
private keys are masked before truncation/serialization. Structured decoding
is limited to 1 MiB, eight nesting levels and 10,000 values; oversized, malformed
or overcomplex structured payloads/subtrees use explicit redaction markers.
Existing egress
redaction is also applied. Pattern masking cannot identify an arbitrary secret
with no credential context. Logs therefore remain private even after masking;
never commit runtime transcripts or share them by default.

## Storage and failure policy

* Maximum **1 GiB** of this logger's `agent-*.jsonl` segments per directory.
* Retain the **current local calendar day plus the preceding 29 days**.
* Rotate before an append would exceed **8 MiB** per segment:
  `agent-YYYY-MM-DD.jsonl`, then `agent-YYYY-MM-DD-000001.jsonl`, and so on.
* Normal prompts/answers stay whole; individual fields are capped at **256 KiB**
  for prompt/answer and **16 KiB** for tool arguments, output and summary.
  Errors are capped at 4 KiB; provenance/identifier fields have smaller caps.
  UTF-8 boundaries are preserved. Each encoded JSONL record is at most **1 MiB**,
  including escaping expansion. `truncated_bytes` names each affected field and
  its exact omitted byte count after masking. Raw tool length is `result_bytes`.
* Cleanup runs on appends. Oldest segments are pruned to satisfy age and byte
  limits; unrelated task logs, history and files are untouched. No idle cleanup
  daemon or compression job is introduced. Records already pruned cannot be
  recovered; there is no claim that the corpus is complete after retention.

All participating processes use a shared advisory lock for selection, cleanup,
rotation and append. No segment handle survives a write, so an in-flight write
cannot race participating retention. A turn crossing midnight keeps its start
`day` for correlation while subsequent records go into the current day's
segment. Retention can split a long-lived turn; correlate by session ID/turn.

The lock wait is bounded to 250 ms; logger failures preserve the agent's return
value. The first omission warns on stderr. A subsequent successful record
includes `previous_records_omitted`, the cumulative count of failed record
attempts in that process. If storage never recovers, stderr is the only receipt
for those omissions. Tool events are appended/closed without per-event fsync;
`turn.finished` syncs the segment. Process/power failure can leave an incomplete
last JSONL line or lose recent unsynced events. Readers should report/skip an
incomplete last line. This corpus is for development analysis, not a transactional
audit ledger. A slow filesystem can still delay a synchronous write or fsync.

## Verification

Use the repository's hermetic runner so tests do not touch operator state:

```sh
./hack/hermetic-go-test.sh ./internal/agent ./cmd/axis -run 'TestDailySessionLog|TestBuildAgentSessionConfig' -count=1
./hack/hermetic-go-test.sh ./internal/agent ./cmd/axis -count=1 -timeout 180s
./hack/hermetic-go-test.sh ./internal/agent ./cmd/axis -race -count=1 -timeout 180s
./hack/hermetic-go-test.sh ./internal/agent -run '^$' -bench BenchmarkDailySessionLogToolAppend -benchtime=1s
```

Focused tests exercise age/byte retention, segment rotation, midnight correlation,
multiple writer processes, lock timeout, UTF-8 and JSON expansion, exact omitted
counts, credential masking, directory/file modes, symlink refusal, log failure
recovery, cancellation, observer preservation, nonduplicated trajectories after
conversation removal, model switching and CLI config wiring. Runtime logs and
receipts belong outside the public repository.

The console opens its logger in the background startup job after the first paint.
The same model-binding helper refreshes attribution for startup resolution and `/model`.
A permanent console regression records turns before and after a model switch;
plain and one-shot modes use the same private log policy.
