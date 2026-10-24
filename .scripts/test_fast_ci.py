"""Negative controls for the gate's fail-closed event accounting."""
import unittest
import sys
import tempfile
from pathlib import Path
from fast_ci import run, validate


class TestReceipts(unittest.TestCase):
    def events(self):
        return [{"Package": "p", "Test": "TestRequired", "Action": action} for action in ("run", "pass")] + [{"Package": "p", "Action": "pass"}]

    def test_success(self):
        validate(self.events(), ["TestRequired"], 0)

    def test_empty_or_missing_tests(self):
        for events, expected in (([], ["TestRequired"]), (self.events(), []), (self.events(), ["TestMissing"])):
            with self.assertRaises(RuntimeError):
                validate(events, expected, 0)

    def test_failed_process_even_with_pass_events(self):
        with self.assertRaises(RuntimeError):
            validate(self.events(), ["TestRequired"], 1)

    def test_skipped_or_failed_subtest(self):
        for action in ("skip", "fail", "build-fail"):
            with self.assertRaises(RuntimeError):
                validate(self.events() + [{"Package": "p", "Test": "TestRequired/child", "Action": action}], ["TestRequired"], 0)

    def test_missing_package_completion(self):
        with self.assertRaises(RuntimeError):
            validate(self.events()[:-1], ["TestRequired"], 0)

    def test_duplicate_pass(self):
        with self.assertRaises(RuntimeError):
            validate(self.events() + self.events()[:2], ["TestRequired"], 0)

    def test_native_failure_is_retained(self):
        records = []
        with tempfile.TemporaryDirectory() as directory:
            evidence = Path(directory)
            with self.assertRaises(RuntimeError):
                run([sys.executable, "-c", "import sys; sys.exit(7)"], evidence, "failure", records)
            self.assertEqual(records[0]["exit_code"], 7)
            self.assertTrue((evidence / "failure.stderr").is_file())

    def test_timeout_does_not_report_success(self):
        records = []
        with tempfile.TemporaryDirectory() as directory:
            with self.assertRaises(RuntimeError):
                run([sys.executable, "-c", "import time; time.sleep(10)"], Path(directory), "timeout", records, timeout=0.1)
            self.assertIsNone(records[0]["exit_code"])
            self.assertIn("timeout", records[0]["error"])



class TestJournalHarness(unittest.TestCase):
    def sample(self, kind):
        from journal_harness import V2, NOP
        return 'package fixture\nimport journal "' + V2 + '"\n' + (NOP if kind == "performance" else "") + '\n// workload assertion stays exact\n'

    def test_performance_adapter_preserves_work(self):
        from journal_harness import adapt_harness, V1, V2, NOP, LEGACY_NOP
        original = self.sample("performance")
        adapted = adapt_harness(original, "performance", V1)
        self.assertIn(LEGACY_NOP, adapted)
        self.assertEqual(adapted.replace('"' + V1 + '"', '"' + V2 + '"').replace(LEGACY_NOP, NOP), original)

    def test_group_and_native_controls(self):
        from journal_harness import adapt_harness, V1, V2
        for kind in ("performance", "group"):
            original = self.sample(kind)
            self.assertEqual(adapt_harness(original, kind, V2), original)
        self.assertIn('"' + V1 + '"', adapt_harness(self.sample("group"), "group", V1))

    def test_quiet_level_adapter_preserves_filtering_contract(self):
        from journal_harness import adapt_harness, V1
        source = self.sample("group") + 'log.SetLevel("error")\n'
        adapted = adapt_harness(source, "group", V1, legacy_level=True)
        self.assertIn('log.Logger.ChangeLevel("error")', adapted)
        self.assertIn("// workload assertion stays exact", adapted)
        with self.assertRaises(ValueError):
            adapt_harness(self.sample("group"), "group", V1, legacy_level=True)

    def test_unexpected_imports_fail_closed(self):
        from journal_harness import adapt_harness, V1, V2
        original = self.sample("group")
        for value in (original.replace(V2, V1), original.replace(V2, "unknown"), original + 'import "' + V2 + '"'):
            with self.subTest(value=value), self.assertRaises(ValueError):
                adapt_harness(value, "group", V1)

    def test_unknown_logger_shape_fails_closed(self):
        from journal_harness import adapt_harness, V1, NOP
        with self.assertRaises(ValueError):
            adapt_harness(self.sample("performance").replace(NOP, "otherLogger"), "performance", V1)

    def test_provenance_requires_one_real_selection(self):
        from journal_harness import journal_requirement, V1, V2
        for path, version in ((V1, "v1.0.0"), (V2, "v2.0.0")):
            row = {"Path": path, "Version": version}
            self.assertEqual(journal_requirement([row]), row)
        for rows in ([], [{"Path": V1}], [{"Path": V1, "Version": "v1", "Replace": {}}], [{"Path": V1, "Version": "v1"}, {"Path": V2, "Version": "v2"}]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                journal_requirement(rows)


if __name__ == "__main__":
    unittest.main()
