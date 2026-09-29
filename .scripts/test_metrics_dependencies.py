#!/usr/bin/env python3
"""Unit tests for the narrow metrics dependency-regression policy."""
import copy
import json
import unittest

from check_metrics_dependencies import METRICS, REMOVED, check, objects


class MetricsDependencyTests(unittest.TestCase):
    def setUp(self):
        self.modules = [{"Path": "gofluentd", "Main": True},
                        {"Path": METRICS, "Version": "v1.0.3"}]
        self.packages = [{"ImportPath": "gofluentd/internal/controller"},
                         {"ImportPath": METRICS, "Module": self.modules[1]}]

    def test_accepts_complete_evidence(self):
        self.assertTrue(check(self.modules, self.packages)["passed"])

    def test_rejects_each_removed_module(self):
        for path in REMOVED:
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "reintroduced"):
                check(self.modules + [{"Path": path, "Version": "v4.5.2"}], self.packages)

    def test_rejects_hidden_removed_package(self):
        for path in REMOVED:
            with self.subTest(path=path), self.assertRaisesRegex(ValueError, "reintroduced"):
                check(self.modules, self.packages + [{"ImportPath": path + "/request"}])

    def test_rejects_replacements(self):
        modules = copy.deepcopy(self.modules)
        modules[1]["Replace"] = {"Path": "./local-copy"}
        with self.assertRaisesRegex(ValueError, "replaced"):
            check(modules, self.packages)
        packages = copy.deepcopy(self.packages)
        packages[1]["Module"]["Replace"] = {"Path": "./local-copy"}
        with self.assertRaisesRegex(ValueError, "replaced"):
            check(self.modules, packages)

    def test_rejects_empty_evidence(self):
        for modules, packages in [([], self.packages), (self.modules, [])]:
            with self.assertRaises(ValueError):
                check(modules, packages)
        with self.assertRaises(ValueError):
            objects(" \n")

    def test_rejects_partial_evidence(self):
        for field, value in [("Error", {"Err": "compile failure"}),
                             ("DepsErrors", [{"Err": "missing dependency"}]),
                             ("Incomplete", True)]:
            packages = copy.deepcopy(self.packages)
            packages[1][field] = value
            with self.subTest(field=field), self.assertRaises(ValueError):
                check(self.modules, packages)
        with self.assertRaises(ValueError):
            check(self.modules, self.packages[:1])
        with self.assertRaises(ValueError):
            check(self.modules, self.packages[1:])
        with self.assertRaises(ValueError):
            check(self.modules[1:], self.packages)

    def test_rejects_duplicate_modules(self):
        with self.assertRaises(ValueError):
            check(self.modules + [self.modules[1]], self.packages)

    def test_json_stream_requires_complete_objects(self):
        self.assertEqual(objects("\n".join(json.dumps(x) for x in self.modules)), self.modules)
        for text in ["{}\n{", "[]", "null", "{} trailing garbage"]:
            with self.subTest(text=text), self.assertRaises(ValueError):
                objects(text)


if __name__ == "__main__":
    unittest.main()
