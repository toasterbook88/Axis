#!/usr/bin/env python3
"""Fail-closed regression checks for the write-capable Claude workflow."""

from pathlib import Path
import re
import unittest


WORKFLOW = Path(__file__).resolve().parents[1] / ".github/workflows/claude.yml"
TEXT = WORKFLOW.read_text(encoding="utf-8")


class ClaudeWorkflowSecurityTests(unittest.TestCase):
    def test_every_prompt_entry_point_rejects_untrusted_associations(self) -> None:
        association_guard = (
            "contains(fromJSON('[\"COLLABORATOR\",\"MEMBER\",\"OWNER\"]'), "
        )
        self.assertEqual(TEXT.count(association_guard), 4)
        for association in (
            "github.event.comment.author_association",
            "github.event.review.author_association",
            "github.event.issue.author_association",
        ):
            self.assertIn(association, TEXT)

    def test_issue_assignment_cannot_replay_an_untrusted_prompt(self) -> None:
        self.assertRegex(TEXT, r"issues:\s*\n\s+types: \[opened\]")
        self.assertNotIn("types: [opened, assigned]", TEXT)

    def test_dependencies_are_immutable_and_checkout_has_no_ambient_token(self) -> None:
        self.assertRegex(
            TEXT,
            r"uses: anthropics/claude-code-action@[0-9a-f]{40} # v1",
        )
        self.assertRegex(
            TEXT,
            r"uses: actions/checkout@[0-9a-f]{40} # v7\.0\.1",
        )
        self.assertRegex(TEXT, r"fetch-depth: 1\s*\n\s+persist-credentials: false")

    def test_privileged_job_is_bounded_and_does_not_enable_bypasses(self) -> None:
        self.assertRegex(TEXT, r"runs-on: ubuntu-latest\s*\n\s+timeout-minutes: 30")
        self.assertIn("group: claude-${{ github.event.issue.number || github.event.pull_request.number || github.run_id }}", TEXT)
        self.assertIn("cancel-in-progress: false", TEXT)
        self.assertNotIn("allowed_non_write_users", TEXT)
        self.assertNotRegex(TEXT, r"allowed_bots:\s*['\"]?\*['\"]?")

    def test_declared_permissions_match_the_supported_write_mode(self) -> None:
        for permission in (
            "contents: write",
            "pull-requests: write",
            "issues: write",
            "id-token: write",
            "actions: read",
        ):
            self.assertIn(permission, TEXT)


if __name__ == "__main__":
    unittest.main()
