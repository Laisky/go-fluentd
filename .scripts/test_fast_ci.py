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


if __name__ == "__main__":
    unittest.main()
