#!/usr/bin/env python3
"""Regression tests for hack/govulncheck_gate.py (run: python3 hack/govulncheck-gate-tests.py)."""

import datetime
import json
import os
import sys
import tempfile
import unittest

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
import govulncheck_gate as gate  # noqa: E402

TODAY = datetime.date(2026, 11, 6)


def stream(*findings):
    """A govulncheck JSON stream with a config object and the given findings."""
    objs = [{"config": {"protocol_version": "v1.0.0"}}]
    for osv, level in findings:
        frame = {"module": "example.com/m", "version": "v1.0.0"}
        if level in ("package", "symbol"):
            frame["package"] = "example.com/m/p"
        if level == "symbol":
            frame["function"] = "F"
        objs.append({"finding": {"osv": osv, "trace": [frame]}})
    return "\n".join(json.dumps(o, indent=2) for o in objs)


class GateTest(unittest.TestCase):
    def test_only_symbol_level_findings_count(self):
        objs = gate.parse_stream(stream(("GO-1", "module"), ("GO-2", "package"), ("GO-3", "symbol")))
        self.assertEqual(gate.called_ids(objs), {"GO-3"})

    def test_unlisted_called_vulnerability_fails(self):
        failures, _ = gate.evaluate({"GO-3"}, {}, TODAY)
        self.assertEqual(len(failures), 1)
        self.assertIn("not in the allowlist", failures[0])

    def test_listed_vulnerability_passes_until_its_date(self):
        allow = gate.parse_allowlist("GO-3 2026-11-06 mitigated in x\n")
        self.assertEqual(gate.evaluate({"GO-3"}, allow, TODAY)[0], [])
        failures, _ = gate.evaluate({"GO-3"}, allow, TODAY + datetime.timedelta(days=1))
        self.assertIn("expired", failures[0])

    def test_stale_entry_is_a_notice_not_a_failure(self):
        allow = gate.parse_allowlist("GO-9 2026-12-01 fixed upstream soon\n")
        failures, notices = gate.evaluate(set(), allow, TODAY)
        self.assertEqual(failures, [])
        self.assertIn("no longer reported", notices[0])

    def test_bad_allowlist_lines_are_rejected(self):
        for text in ("GO-1 2026-12-01\n", "GO-1 tomorrow reason\n", "GO-1 2026-12-01 a\nGO-1 2026-12-02 b\n"):
            with self.assertRaises(ValueError):
                gate.parse_allowlist(text)

    def test_comments_and_blank_lines_are_ignored(self):
        self.assertEqual(gate.parse_allowlist("# header\n\n   \n"), {})

    def test_main_exit_status(self):
        with tempfile.TemporaryDirectory() as d:
            j = os.path.join(d, "v.json")
            a = os.path.join(d, "allow.txt")
            with open(j, "w") as f:
                f.write(stream(("GO-3", "symbol"), ("GO-4", "module")))
            with open(a, "w") as f:
                f.write("")
            self.assertEqual(gate.main(["--json", j, "--allowlist", a, "--today", "2026-11-06"]), 1)
            with open(a, "w") as f:
                f.write("GO-3 2026-12-01 mitigated\n")
            self.assertEqual(gate.main(["--json", j, "--allowlist", a, "--today", "2026-11-06"]), 0)
            with open(a, "w") as f:
                f.write("GO-3 not-a-date mitigated\n")
            self.assertEqual(gate.main(["--json", j, "--allowlist", a, "--today", "2026-11-06"]), 2)

    def test_repo_allowlist_parses(self):
        with open(gate.DEFAULT_ALLOWLIST, encoding="utf-8") as f:
            gate.parse_allowlist(f.read())


if __name__ == "__main__":
    unittest.main(verbosity=1)
