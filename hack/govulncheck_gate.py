#!/usr/bin/env python3
"""govulncheck gate with a reviewed allow-list.

Runs `govulncheck -format json ./...` (or reads a saved JSON stream) and fails
when a vulnerability that AXIS code calls (a symbol-level finding, the same
set plain `govulncheck ./...` fails on) is not in hack/govulncheck-allowlist.txt,
or is listed past its recheck date. Module- and package-level findings do not
fail, as with plain govulncheck. A listed vulnerability that is no longer
found prints a notice so its entry can be removed.

Exit status: 0 pass, 1 gate failure, 2 usage or govulncheck error.
"""

import argparse
import datetime
import json
import os
import subprocess
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
DEFAULT_ALLOWLIST = os.path.join(HERE, "govulncheck-allowlist.txt")


def parse_stream(text):
    """Return the decoded objects of a govulncheck JSON stream."""
    dec = json.JSONDecoder()
    out, i = [], 0
    while True:
        while i < len(text) and text[i].isspace():
            i += 1
        if i >= len(text):
            return out
        obj, i = dec.raw_decode(text, i)
        out.append(obj)


def called_ids(objects):
    """OSV ids with at least one finding whose trace reaches a function."""
    ids = set()
    for obj in objects:
        finding = obj.get("finding")
        if not finding:
            continue
        trace = finding.get("trace") or []
        if trace and trace[0].get("function"):
            ids.add(finding["osv"])
    return ids


def parse_allowlist(text):
    """Map OSV id -> (recheck date, reason). Raises ValueError on a bad line."""
    entries = {}
    for n, line in enumerate(text.splitlines(), 1):
        line = line.strip()
        if not line or line.startswith("#"):
            continue
        parts = line.split(None, 2)
        if len(parts) < 3:
            raise ValueError(f"allowlist line {n}: want '<id> <YYYY-MM-DD> <reason>'")
        osv, date, reason = parts
        try:
            recheck = datetime.date.fromisoformat(date)
        except ValueError as exc:
            raise ValueError(f"allowlist line {n}: bad date {date!r}") from exc
        if osv in entries:
            raise ValueError(f"allowlist line {n}: {osv} listed twice")
        entries[osv] = (recheck, reason)
    return entries


def evaluate(called, allow, today):
    """Return (failures, notices) as lists of messages."""
    failures, notices = [], []
    for osv in sorted(called):
        if osv not in allow:
            failures.append(f"{osv}: called by AXIS code and not in the allowlist")
        elif allow[osv][0] < today:
            failures.append(f"{osv}: allowlist entry expired on {allow[osv][0]}; recheck for an upstream fix")
        else:
            notices.append(f"{osv}: accepted until {allow[osv][0]}: {allow[osv][1]}")
    for osv in sorted(set(allow) - called):
        notices.append(f"{osv}: listed but no longer reported; remove its allowlist entry")
    return failures, notices


def main(argv=None):
    p = argparse.ArgumentParser(description=__doc__.splitlines()[0])
    p.add_argument("--json", help="read a saved govulncheck JSON stream instead of running govulncheck")
    p.add_argument("--allowlist", default=DEFAULT_ALLOWLIST)
    p.add_argument("--today", help="YYYY-MM-DD; defaults to the current UTC date")
    args = p.parse_args(argv)

    if args.json:
        with open(args.json, encoding="utf-8") as f:
            stream = f.read()
    else:
        run = subprocess.run(["govulncheck", "-format", "json", "./..."], capture_output=True, text=True)
        if run.returncode != 0:
            sys.stderr.write(run.stderr)
            print(f"govulncheck failed with exit status {run.returncode}", file=sys.stderr)
            return 2
        stream = run.stdout

    try:
        with open(args.allowlist, encoding="utf-8") as f:
            allow = parse_allowlist(f.read())
        objects = parse_stream(stream)
    except (OSError, ValueError) as exc:
        print(f"govulncheck gate: {exc}", file=sys.stderr)
        return 2

    today = datetime.date.fromisoformat(args.today) if args.today else datetime.datetime.now(datetime.timezone.utc).date()
    failures, notices = evaluate(called_ids(objects), allow, today)
    for msg in notices:
        print(f"note: {msg}")
    for msg in failures:
        print(f"FAIL: {msg}", file=sys.stderr)
    if failures:
        print("govulncheck gate failed; see https://pkg.go.dev/vuln/ for each id", file=sys.stderr)
        return 1
    print("govulncheck gate passed")
    return 0


if __name__ == "__main__":
    sys.exit(main())
