#!/usr/bin/env python3
"""Behavior tests for the GC gate's success, failure and evidence handling."""
import contextlib
import io
import json
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock

import run_msgpack_gc as gate


def events(repetitions=2):
    rows = [{"Action": "start", "Package": gate.PACKAGE}]
    for _ in range(repetitions):
        for test in gate.TESTS:
            rows.extend({"Action": action, "Package": gate.PACKAGE, "Test": test}
                        for action in ("run", "pass"))
    rows.append({"Action": "pass", "Package": gate.PACKAGE})
    return rows


class GateTests(unittest.TestCase):
    def setUp(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        self.path = Path(directory.name) / "tests.jsonl"

    def inspect(self, rows, suffix=""):
        self.path.write_text("".join(json.dumps(row) + "\n" for row in rows) + suffix,
                             encoding="utf-8")
        return gate.inspect_results(self.path, repetitions=2)

    def test_complete_success(self):
        counts, problems, _ = self.inspect(events())
        self.assertEqual(problems, [])
        self.assertEqual(counts[(gate.TESTS[0], "pass")], 2)

    def test_incomplete_duplicate_or_skipped_run_fails(self):
        for rows in ([], events(1), events(3), events()[:-1], events() + events()):
            with self.subTest(rows=len(rows)):
                self.assertTrue(self.inspect(rows)[1])
        for action in ("fail", "skip"):
            for test in ("", gate.TESTS[0]):
                with self.subTest(action=action, test=test):
                    row = {"Package": gate.PACKAGE, "Action": action, "Test": test}
                    self.assertTrue(self.inspect(events() + [row])[1])

    def test_pass_counts_cannot_replace_start_events(self):
        rows = [row for row in events() if row["Action"] != "run"]
        self.assertTrue(self.inspect(rows)[1])

    def test_malformed_unknown_and_wrong_package_fail(self):
        for extra in ("panic: test timed out after 3m0s\n", "{", "null\n", "[]\n"):
            with self.subTest(extra=extra):
                self.assertTrue(self.inspect(events(), extra)[1])
        for row in ({"Action": "pass", "Package": "other"},
                    {"Action": "pass", "Package": gate.PACKAGE, "Test": "other"}):
            self.assertTrue(self.inspect(events() + [row])[1])

    def test_interleaved_build_events(self):
        build = {"Action": "build-output", "ImportPath": "dependency",
                 "Output": "compiler diagnostic\n"}
        self.assertEqual(self.inspect([build] + events())[1], [])
        build["Action"] = "build-fail"
        self.assertTrue(self.inspect([build] + events())[1])

    def test_diagnostics_are_bounded(self):
        rows = events() + [{"Action": "output", "Package": gate.PACKAGE,
                            "Output": "x" * 8192}] * 200
        _, _, tail = self.inspect(rows)
        self.assertEqual(len(tail), 100)
        self.assertTrue(all(len(text) <= 4096 for text in tail))

    def run_main(self, rows, returncode):
        def run(command, **kwargs):
            self.assertIn("-count=1000", command)
            self.assertIn("-timeout=180s", command)
            self.assertIn("-race", command)
            self.assertEqual(kwargs["env"]["GOMAXPROCS"], "2")
            self.assertEqual(kwargs["env"]["GOGC"], "10")
            self.assertFalse(kwargs["check"])
            self.assertEqual(kwargs["stderr"], subprocess.STDOUT)
            kwargs["stdout"].write("".join(json.dumps(row) + "\n" for row in rows))
            return subprocess.CompletedProcess(command, returncode)
        capture = io.StringIO()
        with mock.patch.object(gate.subprocess, "run", side_effect=run):
            with contextlib.redirect_stdout(capture):
                code = gate.main([str(self.path)])
        return code, capture.getvalue()

    def test_runner_accepts_only_complete_success(self):
        code, output = self.run_main(events(1000), 0)
        self.assertEqual(code, 0)
        self.assertIn("full package completion verified", output)

    def test_nonzero_exit_is_not_masked_by_complete_json(self):
        code, output = self.run_main(events(1000), 1)
        self.assertEqual(code, 1)
        self.assertIn("exit=1", output)

    def test_zero_exit_cannot_mask_truncated_tests(self):
        self.assertEqual(self.run_main(events(1), 0)[0], 1)

    def test_failed_command_still_prints_timeout_diagnostic(self):
        rows = [{"Action": "output", "Package": gate.PACKAGE,
                 "Output": "panic: test timed out after 3m0s\n"}]
        code, output = self.run_main(rows, 1)
        self.assertEqual(code, 1)
        self.assertIn("panic: test timed out", output)
        self.assertIn("panic: test timed out", self.path.read_text())

    def test_missing_go_is_an_error(self):
        with mock.patch.object(gate.subprocess, "run", side_effect=FileNotFoundError("go")):
            with contextlib.redirect_stderr(io.StringIO()):
                self.assertEqual(gate.main([str(self.path)]), 2)


if __name__ == "__main__":
    unittest.main()
