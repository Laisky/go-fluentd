import unittest

from check_coverage import check_floors, read_profile


class CoverageContracts(unittest.TestCase):
    def test_weighted_statement_coverage_includes_unexecuted_packages(self):
        counts = read_profile([
            "mode: atomic", "app/pkg/a.go:1.1,2.1 2 10",
            "app/pkg/b.go:1.1,2.1 8 0", "app/main.go:1.1,2.1 10 0",
        ])
        self.assertEqual(counts["app/pkg"], (2, 10))
        self.assertEqual(counts["total"], (2, 20))
        self.assertEqual(check_floors(counts, {"total": 10}), [])
        self.assertTrue(check_floors(counts, {"total": 10.01}))

    def test_missing_package_never_passes_a_floor(self):
        self.assertEqual(check_floors({}, {"required": 0}), ["required: missing coverage"])

    def test_malformed_or_empty_profile_is_rejected(self):
        for rows in ([], ["mode: atomic"], ["mode: invalid"],
                     ["mode: atomic", "pkg/a.go:1.1,2.1 -1 2"],
                     ["mode: atomic", "pkg/a.go:1.1,2.1 1 -2"],
                     ["mode: atomic", "corrupt"]):
            with self.subTest(rows=rows), self.assertRaises(ValueError):
                read_profile(rows)


if __name__ == "__main__":
    unittest.main()
