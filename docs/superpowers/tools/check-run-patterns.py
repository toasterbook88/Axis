#!/usr/bin/env python3
"""Verify that every `go test <package> -run <pattern>` matches a real test.

A `-run` pattern that matches nothing is silently green: `go test` prints
"no tests to run" and exits 0. A red/green stage built on such a pattern
verifies nothing. This checker cross-references every invocation in a plan
document against tests in the package actually invoked, including tests the
plan defines and tests already in the tree.

Usage:
    python3 docs/superpowers/tools/check-run-patterns.py <plan.md> [repo-root]

Exits non-zero if any invocation matches no test.
"""

import os
import re
import shlex
import sys
from collections import defaultdict
from dataclasses import dataclass


def collect_repo_tests(repo):
    tests = defaultdict(set)
    for root, dirs, files in os.walk(repo):
        dirs[:] = [d for d in dirs if d not in (".git", "vendor", "node_modules")]
        rel = os.path.relpath(root, repo)
        package = "." if rel == "." else "./" + rel.replace(os.sep, "/")
        for name in files:
            if not name.endswith("_test.go"):
                continue
            with open(os.path.join(root, name), encoding="utf-8") as fh:
                tests[package] |= set(re.findall(r"^func (Test\w+)\(", fh.read(), re.M))
    return dict(tests)


@dataclass(frozen=True)
class Invocation:
    package: str
    pattern: str
    line_number: int


def normalize_package(package):
    if package != ".":
        package = package.rstrip("/")
    return package


def invocations(src):
    """Return distinct package/pattern pairs from real shell invocations.

    Skips markdown table rows, where a plan may legitimately document a
    previously-broken pattern as prose. Lines containing an invocation that
    cannot be parsed are returned as errors instead of being silently ignored.
    """
    found = []
    errors = []
    seen = set()
    for line_number, line in enumerate(src.splitlines(), 1):
        if "go test" not in line or "-run" not in line:
            continue
        if line.strip().startswith("|"):
            continue

        command = line[line.index("go test"):].strip().strip("`")
        try:
            tokens = shlex.split(command)
        except ValueError as exc:
            errors.append((line_number, str(exc)))
            continue

        pattern = None
        for i, token in enumerate(tokens):
            if token == "-run":
                if i + 1 < len(tokens):
                    pattern = tokens[i + 1]
                break
            if token.startswith("-run="):
                pattern = token.split("=", 1)[1]
                break
        packages = [
            normalize_package(token)
            for token in tokens[2:]
            if token == "." or token.startswith("./")
        ]
        if not pattern or not packages:
            errors.append((line_number, "could not parse package and -run pattern"))
            continue

        for package in packages:
            key = (package, pattern)
            if key in seen:
                continue
            seen.add(key)
            found.append(Invocation(package, pattern, line_number))
    return found, errors


def tests_for_package(index, package):
    if package.endswith("/..."):
        prefix = package[:-4].rstrip("/")
        return set().union(*(
            tests
            for path, tests in index.items()
            if path == prefix or path.startswith(prefix + "/")
        )) if index else set()
    return set(index.get(package, set()))


def index_plan_tests(plan_tests, calls, repo_tests):
    """Associate plan-defined tests with packages without global-name leakage.

    Existing tree locations are authoritative. A not-yet-written plan test is
    attributed only when every invocation pattern matching its name points to
    one package. Ambiguous tests are deliberately not attributed, causing the
    affected invocation to fail rather than silently green.
    """
    out = defaultdict(set)
    for test in plan_tests:
        existing = {
            package
            for package, tests in repo_tests.items()
            if test in tests
        }
        if existing:
            for package in existing:
                out[package].add(test)
            continue

        candidates = set()
        for call in calls:
            try:
                matches = re.search(call.pattern, test)
            except re.error:
                continue
            if matches:
                candidates.add(call.package)
        if len(candidates) == 1:
            out[next(iter(candidates))].add(test)
    return dict(out)


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return 2

    plan = sys.argv[1]
    repo = sys.argv[2] if len(sys.argv) > 2 else os.getcwd()

    with open(plan, encoding="utf-8") as fh:
        src = fh.read()

    calls, parse_errors = invocations(src)
    plan_tests = set(re.findall(r"^func (Test\w+)\(", src, re.M))
    repo_tests = collect_repo_tests(repo)
    plan_index = index_plan_tests(plan_tests, calls, repo_tests)
    repo_count = sum(len(tests) for tests in repo_tests.values())

    print("tests defined in plan: %d   already in tree: %d" % (len(plan_tests), repo_count))
    print()

    bad = len(parse_errors)
    for line_number, message in parse_errors:
        print("  !!  line %-4d -> %s" % (line_number, message))

    for call in calls:
        try:
            rx = re.compile(call.pattern)
        except re.error as exc:
            bad += 1
            print("  !!  %-24s %-32r -> INVALID REGEX: %s"
                  % (call.package, call.pattern, exc))
            continue

        plan_known = tests_for_package(plan_index, call.package)
        repo_known = tests_for_package(repo_tests, call.package)
        hits = sorted(t for t in plan_known | repo_known if rx.search(t))
        if hits:
            in_plan = sum(1 for t in hits if t in plan_known)
            tree_only = sum(1 for t in hits if t in repo_known and t not in plan_known)
            print("  OK  %-24s %-32r -> %d  [plan:%d tree:%d]"
                  % (call.package, call.pattern, len(hits), in_plan, tree_only))
        else:
            bad += 1
            print("  !!  %-24s %-32r -> NO MATCH" % (call.package, call.pattern))
            print("      this invocation prints 'no tests to run' and exits 0")

    print()
    print("invocations matching nothing: %d" % bad)
    return 1 if bad else 0


if __name__ == "__main__":
    sys.exit(main())
