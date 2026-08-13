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

Other commands with `--format json` retain their command-specific payloads.
They must still keep stdout parseable and send diagnostic prose to stderr; they
do not claim the `axis.output/v1` envelope until explicitly migrated.
