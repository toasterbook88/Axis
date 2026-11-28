# Machine Output Contract

`axis daemon status` writes exactly one JSON document to stdout on a successful
daemon query. Human prose is never appended after the document. Diagnostics for
a failed query go to stderr and the process exits non-zero.

The v1 envelope is:

```json
{
  "schema_version": "axis.output/v1",
  "command": "daemon status",
  "ok": true,
  "status": "fresh",
  "data": {},
  "warnings": []
}
```

- `schema_version` identifies the outer contract independently of the AXIS
  release version.
- `command` identifies the producing command.
- `ok` reports whether the daemon and cache are healthy enough for normal use.
- `status` is one of `fresh`, `stale`, `degraded`, `unavailable`, or
  `incompatible`.
- `data` is the complete daemon metadata payload.
- `warnings` is always an array of `{kind,message}` objects, including when it
  is empty.

The daemon metadata inside `data` includes `publication_id` whenever the cache
is ready. It identifies the exact snapshot publication described by the
snapshot-bound metadata fields and matches `publication.id` from `/snapshot`.

## `axis agent -p --format json`

A piped agent run writes one `axis.output/v1` document to stdout when it
finishes, including when it fails before the agent starts. The streamed
assistant text, the model picker, tool badges, denial notices and the token
summary go to stderr. `command` is `agent`, `status` is the
run's outcome, and `ok` is true only for `answered`.

```json
{
  "schema_version": "axis.output/v1",
  "command": "agent",
  "ok": false,
  "status": "turn_ceiling",
  "data": {
    "outcome": "turn_ceiling",
    "exit_code": 8,
    "turns": 2,
    "max_turns": 2,
    "final_text": "",
    "pending_approvals": [],
    "usage": {"tokens_in": 200, "tokens_out": 10, "estimated": false},
    "elapsed_ms": 9
  },
  "warnings": []
}
```

| `outcome` | exit code | meaning |
|---|---|---|
| `answered` | 0 | the model ended a round with no tool calls |
| `turn_ceiling` | 8 | the loop used every `--max-turns` round without a final answer |
| `needs_input` | 9 | at least one step needed an approval a piped run cannot give, or a tool the `--autonomy` mode hides |
| `failed` | 4 | the run returned an error, or the deadline or a signal cut its last round short; `data.error` says which |
| `failed` | 1 or 2 | the agent never started (bad flags, unknown role, config or backend startup); `turns` is 0 |

- An error wins over the other outcomes, and `needs_input` wins over
  `turn_ceiling` and `answered`: the model may have answered around the denied
  step, and a rerun with approval may do more.
- `final_text` is the newest assistant message's text. At the ceiling it is
  what the model said beside its last tool calls, often empty. It is empty on
  `failed`.
- `pending_approvals` is always an array. Each entry has `tool`, `request`
  (the approval text, up to 400 characters), `risk_score` and `clears`, what
  lets a rerun through: `--autonomy full`; `auto_approve_tools` for an
  external MCP tool, which asks in every mode unless `nodes.yaml` lists it
  under `mcp_servers.<server>.auto_approve_tools`; `interactive approval`
  for consult_model and any command scoring 75 or more (a safety rule's ask or
  a safety-blocked override), which no flag clears; or `none` for a `run_shell`
  or `run_on_node` command a safety rule denies outright (score 100), which
  guarded execution blocks again even after an interactive override.
- A call to a tool the `--autonomy` mode hides (for example `run_shell` in the
  default observe mode) also lands in `pending_approvals`, with `request`
  `hidden by the current --autonomy mode` and `clears` `--autonomy full`.
- A request the model retries is listed once.
- `--max-turns` must be at least 1 with `-p`, and `--format json` cannot be
  combined with `--desk-lease`.
- A flag that does not parse (a malformed value such as `--max-turns nope`, or
  an unknown flag) still gets a `failed` document with exit code 1, when the
  arguments before `--` ask for `-p` and `--format json`.
- With no `--model` and no `agent.default_model`, the model picker opens only
  when stdin and stderr are both terminals; otherwise the model resolves as it
  does for a non-interactive run. `--select` always opens the picker.
- `usage.estimated` is true when the backend reported no usage; `tokens_in` is
  then the conversation estimate.

Other commands with `--format json` retain their command-specific payloads.
They must still keep stdout parseable and send diagnostic prose to stderr; they
do not claim the `axis.output/v1` envelope until explicitly migrated.
