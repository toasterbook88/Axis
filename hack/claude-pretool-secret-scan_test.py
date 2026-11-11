import io
import json
import runpy
import subprocess
import sys
import unittest
from pathlib import Path


SCRIPT = Path(__file__).with_name("claude-pretool-secret-scan.py")


def invoke(payload):
    namespace = runpy.run_path(str(SCRIPT), run_name="secret_scan_test")
    old_stdin, old_stderr = sys.stdin, sys.stderr
    sys.stdin = io.StringIO(payload)
    sys.stderr = io.StringIO()
    try:
        return namespace["main"](), sys.stderr.getvalue()
    finally:
        sys.stdin, sys.stderr = old_stdin, old_stderr


class SecretScanTests(unittest.TestCase):
    def test_allows_regular_tool_input(self):
        payload = json.dumps({"tool_input": {"content": "ordinary text"}})
        self.assertEqual(invoke(payload)[0], 0)

    def test_blocks_github_token_without_echoing_it(self):
        candidate = "ghp_" + "A" * 36
        payload = json.dumps({"tool_input": {"content": candidate}})
        code, message = invoke(payload)
        self.assertEqual(code, 2)
        self.assertNotIn(candidate, message)

    def test_blocks_sourcegraph_token(self):
        candidate = "sgp_" + "b" * 32
        payload = json.dumps({"tool_input": {"command": "printf " + candidate}})
        self.assertEqual(invoke(payload)[0], 2)

    def test_blocks_authorization_header(self):
        payload = json.dumps(
            {"tool_input": {"command": "Authorization: Bearer " + "c" * 32}}
        )
        self.assertEqual(invoke(payload)[0], 2)

    def test_allows_placeholders(self):
        payload = json.dumps(
            {"tool_input": {"content": "api_key=your-placeholder-value-123456"}}
        )
        self.assertEqual(invoke(payload)[0], 0)

    def test_blocks_unparseable_input(self):
        code, message = invoke("not-json")
        self.assertEqual(code, 2)
        self.assertIn("could not parse", message)

    def test_allows_identifier_valued_secret(self):
        payload = json.dumps(
            {"tool_input": {"content": "secret = cfg.Discovery.Secret"}}
        )
        self.assertEqual(invoke(payload)[0], 0)

    def test_allows_documented_placeholders(self):
        for line in (
            "secret: shared-cluster-secret",
            'secret: "replace-with-a-long-random-string"',
            "#   secret: shared-cluster-secret",
        ):
            payload = json.dumps({"tool_input": {"content": line}})
            self.assertEqual(invoke(payload)[0], 0, line)

    def test_allows_elided_basic_auth_fixture(self):
        payload = json.dumps(
            {
                "tool_input": {
                    "content": '"Authorization: Basic c2Vuc2...nary text",'
                }
            }
        )
        self.assertEqual(invoke(payload)[0], 0)

    @staticmethod
    def _credential_shaped_value():
        # Joined at runtime so the fixture is not itself a secret-shaped
        # literal that the repository's own scanners flag.
        return "".join(("A1b2C3d4", "E5f6G7h8", "I9j0K1l2"))

    def test_blocks_json_api_key(self):
        payload = json.dumps(
            {
                "tool_input": {
                    "content": '{"api_key": "%s"}'
                    % self._credential_shaped_value()
                }
            }
        )
        self.assertEqual(invoke(payload)[0], 2)

    def test_blocks_yaml_api_key(self):
        payload = json.dumps(
            {
                "tool_input": {
                    "content": "api_key: %s" % self._credential_shaped_value()
                }
            }
        )
        self.assertEqual(invoke(payload)[0], 2)

    def test_blocks_provider_tokens(self):
        candidates = (
            "sk-ant-api03-" + "a" * 24,
            "sk-proj-" + "b" * 24,
            "glpat-" + "c" * 24,
            "hvs." + "d" * 24,
            "eyJ" + "e" * 12 + "." + "f" * 12 + "." + "g" * 12,
            "https://hooks.slack.com/services/" + "h" * 24,
        )
        for candidate in candidates:
            payload = json.dumps({"tool_input": {"content": candidate}})
            self.assertEqual(invoke(payload)[0], 2, candidate[:12])

    def test_allows_prose_that_names_a_key(self):
        payload = json.dumps(
            {"tool_input": {"content": "update the api_key documentation"}}
        )
        self.assertEqual(invoke(payload)[0], 0)

    def test_blocks_every_match_not_only_the_first(self):
        credential = self._credential_shaped_value()
        for content in (
            "ghp_" + "EXAMPLE" + "A" * 28 + " then ghp_" + "B" * 36,
            "password: example_password_value_1234\napi_key: " + credential,
        ):
            payload = json.dumps({"tool_input": {"content": content}})
            self.assertEqual(invoke(payload)[0], 2, content[:20])

    def test_blocks_prefixed_environment_keys(self):
        credential = self._credential_shaped_value()
        for content in ("OPENAI_API_KEY=" + credential, "MY_SECRET=" + credential):
            payload = json.dumps({"tool_input": {"content": content}})
            self.assertEqual(invoke(payload)[0], 2, content[:16])

    def test_blocks_bare_token_assignment(self):
        credential = self._credential_shaped_value()
        for content in ("token: " + credential, "token=" + credential):
            payload = json.dumps({"tool_input": {"content": content}})
            self.assertEqual(invoke(payload)[0], 2, content[:8])

    def test_allows_whole_file_write_of_tracked_fixtures(self):
        if (
            subprocess.run(
                ["git", "rev-parse", "--is-inside-work-tree"],
                capture_output=True,
            ).returncode
            != 0
        ):
            self.skipTest("not inside a git work tree")
        root = Path(__file__).resolve().parent.parent
        for relative in (
            "internal/agent/observer_test.go",
            "internal/agent/sessionlog_store_test.go",
        ):
            path = root / relative
            if not path.exists():
                continue
            payload = json.dumps(
                {
                    "tool_name": "Write",
                    "tool_input": {
                        "file_path": relative,
                        "content": path.read_text(encoding="utf-8"),
                    },
                }
            )
            self.assertEqual(invoke(payload)[0], 0, relative)

    def test_allows_a_value_that_is_already_tracked(self):
        # A value that exists verbatim in a tracked file is already public;
        # re-using it in a file edit is not a new leak, and blocking it would
        # make the repository's own fixtures uneditable.
        credential = "".join(("sk-", "abcdefghijklmnopqrstuvwxyz", "0123456789"))
        payload = json.dumps(
            {"tool_name": "Write", "tool_input": {"file_path": "new.go", "content": credential}}
        )
        self.assertEqual(invoke(payload)[0], 0)

    def test_blocks_tracked_value_in_a_shell_command(self):
        # The tracked-value allowance covers file edits only; a committed
        # token echoed in a shell command still blocks.
        credential = "".join(("sk-", "abcdefghijklmnopqrstuvwxyz", "0123456789"))
        payload = json.dumps(
            {"tool_name": "Bash", "tool_input": {"command": "echo " + credential}}
        )
        self.assertEqual(invoke(payload)[0], 2)


if __name__ == "__main__":
    unittest.main()
