"""Behavior tests for the B1 CI execution/evidence gate; no network or journal."""
import copy
import json
import unittest

from verify_b1_measure import TESTS, verify


def valid_rows(paced):
    phases = ["healthy_admission", "healthy_direct_replay", "home_down_growing_to_full",
              "home_down_pending_replay", "home_recovery_replay"]
    if paced:
        phases += ["home_down_full_hold", "home_down_remaining_snapshot"]
    rows = []
    for phase in phases:
        data = {"phase": phase, "fast_validate": True, "paced": paced,
                "storage_scan_timeout_ms": 1000, "storage_scan_max_entries": 1024,
                "scan_budget_rejected": 0}
        if phase == "home_down_growing_to_full":
            data.update(offered=96, admitted=39, refused_per_signal={"logs": 57},
                        byte_limit_refused_per_signal={"logs": 57}, scan_budget_refused_per_signal={})
        rows.append({"Action": "output", "Test": TESTS[0], "Output": "B1_SYNTHETIC " + json.dumps(data) + "\n"})
    rows.append({"Action": "output", "Test": TESTS[0], "Output":
                 "B1_SYNTHETIC_FINAL state=/synthetic attempted=108 admitted=51 delivered=51 "
                 f"preserved=true transport=in-process-no-network fast_validate=true paced={str(paced).lower()}\n"})
    rows += [{"Action": "pass", "Test": name} for name in TESTS]
    rows.append({"Action": "pass"})
    return rows


class EvidenceGateTests(unittest.TestCase):
    def test_valid_modes_and_repeats(self):
        for paced in (False, True):
            with self.subTest(paced=paced):
                rows = valid_rows(paced)
                verify(rows, 1, paced)
                verify(rows[:-1] * 3 + [rows[-1]], 3, paced)

    def test_empty_skipped_failed_missing_duplicate_or_wrong_mode(self):
        rows = valid_rows(True)
        cases = [[], rows + [{"Action": "skip", "Test": TESTS[0]}],
                 rows + [{"Action": "fail"}], rows[:-2] + rows[-1:],
                 rows + [{"Action": "pass", "Test": TESTS[0]}], valid_rows(False)]
        for index, damaged in enumerate(cases):
            with self.subTest(index=index), self.assertRaises(ValueError):
                verify(damaged, 1, True)

    def test_invalid_phase_evidence(self):
        for change in (
            {"fast_validate": False}, {"paced": False},
            {"storage_scan_timeout_ms": 10}, {"storage_scan_max_entries": 16},
            {"scan_budget_rejected": 1}, {"phase": "unexpected"},
        ):
            rows = copy.deepcopy(valid_rows(True))
            data = json.loads(rows[0]["Output"].split("B1_SYNTHETIC ")[1])
            data.update(change)
            rows[0]["Output"] = "B1_SYNTHETIC " + json.dumps(data)
            with self.subTest(change=change), self.assertRaises(ValueError):
                verify(rows, 1, True)

    def test_invalid_refusal_accounting(self):
        for change in (
            {"scan_budget_refused_per_signal": {"logs": 1}},
            {"byte_limit_refused_per_signal": {"logs": 56}},
            {"admitted": 0}, {"offered": 97},
        ):
            rows = copy.deepcopy(valid_rows(True))
            data = json.loads(rows[2]["Output"].split("B1_SYNTHETIC ")[1])
            data.update(change)
            rows[2]["Output"] = "B1_SYNTHETIC " + json.dumps(data)
            with self.subTest(change=change), self.assertRaises(ValueError):
                verify(rows, 1, True)

    def test_missing_or_invalid_final(self):
        for old, new in (("attempted=108", "attempted=0"), ("delivered=51", "delivered=50"),
                         ("fast_validate=true", "fast_validate=false"),
                         ("transport=in-process-no-network", "transport=http"),
                         ("preserved=true", "preserved=false"),
                         ("B1_SYNTHETIC_FINAL ", "MISSING ")):
            rows = copy.deepcopy(valid_rows(True))
            for row in rows:
                row["Output"] = row.get("Output", "").replace(old, new)
            with self.subTest(old=old), self.assertRaises(ValueError):
                verify(rows, 1, True)


if __name__ == "__main__":
    unittest.main()
