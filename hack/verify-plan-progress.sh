#!/usr/bin/env bash
set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
plan="$repo_root/EXECUTION-PLAN.md"

if [[ ! -f "$plan" ]]; then
  echo "verify-plan-progress: missing $plan" >&2
  exit 1
fi

python3 - "$plan" "$repo_root" <<'PY'
import re
import sys
from pathlib import Path

plan_path = Path(sys.argv[1])
repo_root = Path(sys.argv[2])
lines = plan_path.read_text().splitlines()
marker = "<!-- verify-plan-progress: matrix -->"
try:
    marker_index = lines.index(marker)
except ValueError:
    print("verify-plan-progress: missing matrix marker", file=sys.stderr)
    raise SystemExit(1)

rows = []
for line in lines[marker_index + 1:]:
    if not line.startswith("|"):
        if rows:
            break
        continue
    cells = [cell.strip() for cell in line.strip().strip("|").split("|")]
    if len(cells) != 4 or cells[0] in {"Item", "---"} or all(set(cell) <= {"-", ":"} for cell in cells):
        continue
    rows.append(cells)

if not rows:
    print("verify-plan-progress: matrix has no data rows", file=sys.stderr)
    raise SystemExit(1)

for item, _fix, status, tests in rows:
    if status not in {"[x]", "[ ]"}:
        print(f"verify-plan-progress: {item} has invalid status {status!r}", file=sys.stderr)
        raise SystemExit(1)
    refs = [ref.strip() for ref in tests.split(",") if ref.strip()]
    if not refs:
        print(f"verify-plan-progress: {item} has no test references", file=sys.stderr)
        raise SystemExit(1)
    for ref in refs:
        if "::" not in ref:
            print(f"verify-plan-progress: {item} invalid reference {ref!r}", file=sys.stderr)
            raise SystemExit(1)
        relpath, symbol = ref.split("::", 1)
        test_path = repo_root / relpath
        if not test_path.is_file():
            print(f"verify-plan-progress: {item} missing file for {ref}", file=sys.stderr)
            raise SystemExit(1)
        source = test_path.read_text()
        match = re.search(rf"(?m)^func\s+{re.escape(symbol)}\s*\(", source)
        if not match:
            print(f"verify-plan-progress: {item} missing symbol for {ref}", file=sys.stderr)
            raise SystemExit(1)
        if status == "[x]" and not symbol.startswith("Benchmark"):
            next_func = source.find("\nfunc ", match.end())
            body = source[match.start():] if next_func < 0 else source[match.start():next_func]
            if re.search(r"\bt\.Skip(?:f)?\s*\(", body):
                print(f"verify-plan-progress: {item} completed test is skipped: {ref}", file=sys.stderr)
                raise SystemExit(1)

print(f"verify-plan-progress: OK ({len(rows)} rows)")
PY
