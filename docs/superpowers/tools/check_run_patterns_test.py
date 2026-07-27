import importlib.util
import pathlib
import sys
import tempfile
import unittest


SCRIPT = pathlib.Path(__file__).with_name("check-run-patterns.py")
SPEC = importlib.util.spec_from_file_location("check_run_patterns", SCRIPT)
CHECKER = importlib.util.module_from_spec(SPEC)
sys.modules[SPEC.name] = CHECKER
SPEC.loader.exec_module(CHECKER)


class CheckRunPatternsTests(unittest.TestCase):
    def write_test(self, repo, package, test_name):
        directory = pathlib.Path(repo, package)
        directory.mkdir(parents=True, exist_ok=True)
        directory.joinpath("fixture_test.go").write_text(
            "package fixture\n\nfunc %s(t *testing.T) {}\n" % test_name,
            encoding="utf-8",
        )

    def test_repo_test_in_another_package_does_not_match(self):
        with tempfile.TemporaryDirectory() as repo:
            self.write_test(repo, "internal/state", "TestPruneNodes")
            src = """
func TestPruneNodes(t *testing.T) {}
go test ./cmd/axis -run TestPruneNodes
"""
            calls, errors = CHECKER.invocations(src)
            self.assertEqual(errors, [])
            repo_tests = CHECKER.collect_repo_tests(repo)
            plan_index = CHECKER.index_plan_tests(
                {"TestPruneNodes"}, calls, repo_tests
            )

            self.assertEqual(
                CHECKER.tests_for_package(repo_tests, "./cmd/axis"), set()
            )
            self.assertEqual(
                CHECKER.tests_for_package(plan_index, "./cmd/axis"), set()
            )

    def test_not_yet_written_plan_test_is_attributed_to_unique_package(self):
        src = """
func TestFutureGuard(t *testing.T) {}
go test ./cmd/axis/ -run "TestFutureGuard"
"""
        calls, errors = CHECKER.invocations(src)
        self.assertEqual(errors, [])
        plan_index = CHECKER.index_plan_tests(
            {"TestFutureGuard"}, calls, repo_tests={}
        )

        self.assertEqual(calls[0].package, "./cmd/axis")
        self.assertEqual(calls[0].pattern, "TestFutureGuard")
        self.assertEqual(
            CHECKER.tests_for_package(plan_index, "./cmd/axis"),
            {"TestFutureGuard"},
        )

    def test_unparseable_invocation_is_an_error(self):
        calls, errors = CHECKER.invocations("go test -run TestMissingPackage")

        self.assertEqual(calls, [])
        self.assertEqual(len(errors), 1)


if __name__ == "__main__":
    unittest.main()
