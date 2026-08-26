#!/usr/bin/env python3
"""Fail when a GitHub workflow uses a non-local Action without a full SHA."""

from pathlib import Path
import re
import sys


USES = re.compile(r"^\s*(?:-\s*)?uses:\s*([^#\s]+)")
FULL_SHA = re.compile(r"^[0-9a-f]{40}$")


def main() -> int:
    failures: list[str] = []
    for workflow in sorted(Path(".github/workflows").glob("*.yml")):
        for number, line in enumerate(workflow.read_text().splitlines(), start=1):
            match = USES.match(line)
            if not match:
                continue
            target = match.group(1)
            if target.startswith("./"):
                continue
            if "@" not in target:
                failures.append(f"{workflow}:{number}: missing Action ref: {target}")
                continue
            ref = target.rsplit("@", 1)[1]
            if not FULL_SHA.fullmatch(ref):
                failures.append(
                    f"{workflow}:{number}: Action must use a full commit SHA: {target}"
                )

    if failures:
        print("\n".join(failures), file=sys.stderr)
        return 1
    print("workflow Action pin guardrails passed")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
