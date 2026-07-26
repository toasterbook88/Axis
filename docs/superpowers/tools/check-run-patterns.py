#!/usr/bin/env python3
"""Verify that every `go test -run <pattern>` in a plan matches a real test.

A `-run` pattern that matches nothing is silently green: `go test` prints
"no tests to run" and exits 0. A red/green stage built on such a pattern
verifies nothing. This checker cross-references every invocation in a plan
document against the tests the plan defines plus every test already in the
tree.

Usage:
    python3 docs/superpowers/tools/check-run-patterns.py <plan.md> [repo-root]

Exits non-zero if any invocation matches no test.
"""

import os
import re
import sys


def collect_repo_tests(repo):
    tests = set()
    for root, dirs, files in os.walk(repo):
        dirs[:] = [d for d in dirs if d not in (".git", "vendor", "node_modules")]
        for name in files:
            if not name.endswith("_test.go"):
                continue
            with open(os.path.join(root, name), encoding="utf-8") as fh:
                tests |= set(re.findall(r"^func (Test\w+)\(", fh.read(), re.M))
    return tests


def invocations(src):
    """Yield each distinct -run pattern from a real shell invocation.

    Skips markdown table rows, where a plan may legitimately document a
    previously-broken pattern as prose.
    """
    seen = []
    for line in src.splitlines():
        if "go test" not in line or "-run" not in line:
            continue
        if line.strip().startswith("|"):
            continue
        m = re.search(r"-run\s+'([^']+)'|-run\s+([A-Za-z0-9_|]+)", line)
        if not m:
            continue
        pattern = m.group(1) or m.group(2)
        if pattern not in seen:
            seen.append(pattern)
            yield pattern


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2

    plan = sys.argv[1]
    repo = sys.argv[2] if len(sys.argv) > 2 else os.getcwd()

    with open(plan, encoding="utf-8") as fh:
        src = fh.read()

    plan_tests = set(re.findall(r"^func (Test\w+)\(", src, re.M))
    repo_tests = collect_repo_tests(repo)
    known = plan_tests | repo_tests

    print("tests defined in plan: %d   already in tree: %d" % (len(plan_tests), len(repo_tests)))
    print()

    bad = 0
    for pattern in invocations(src):
        rx = re.compile(pattern)
        hits = sorted(t for t in known if rx.search(t))
        if hits:
            in_plan = sum(1 for t in hits if t in plan_tests)
            print("  OK  %-40r -> %d  [plan:%d tree:%d]"
                  % (pattern, len(hits), in_plan, len(hits) - in_plan))
        else:
            bad += 1
            print("  !!  %-40r -> NO MATCH" % pattern)
            print("      this invocation prints 'no tests to run' and exits 0")

    print()
    print("invocations matching nothing: %d" % bad)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
