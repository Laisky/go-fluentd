"""Regression controls for the historical campaign's delivery oracle."""
import copy
import importlib.util
from pathlib import Path
import unittest

spec = importlib.util.spec_from_file_location("recovery_campaign", Path(__file__).with_name("run.py"))
campaign = importlib.util.module_from_spec(spec)
spec.loader.exec_module(campaign)


class DeliveryOracleTests(unittest.TestCase):
    def setUp(self):
        self.expected = {}
        self.rows = []
        for index in range(16):
            case = "fresh-after-convergence" if index == 15 else "earlier"
            for signal in campaign.SIGNALS:
                key = (case, index, signal)
                value = str(index) + signal
                self.expected[key] = value
                self.rows.append(dict(case=case, sequence=index, signal=signal, sha256=value))

    def test_all_48_exact_payloads(self):
        self.assertEqual(len(campaign.verify_delivery(self.expected, self.rows)), 48)

    def test_final_payload_corruption_is_not_hidden_by_matching_keys(self):
        rows = copy.deepcopy(self.rows)
        rows[-1]["sha256"] = "changed-with-identical-routing-keys"
        old_gate = campaign.Campaign(Path("/synthetic"))
        old_gate.expected = self.expected
        old_gate.ledger = lambda: rows
        self.assertTrue(old_gate.all_delivered())
        with self.assertRaisesRegex(AssertionError, "payload identity changed"):
            campaign.verify_delivery(self.expected, rows)

    def test_duplicate_is_rejected(self):
        with self.assertRaisesRegex(AssertionError, "duplicated"):
            campaign.verify_delivery(self.expected, self.rows + self.rows[-1:])

    def test_missing_is_rejected(self):
        with self.assertRaisesRegex(AssertionError, "missing or unexpected"):
            campaign.verify_delivery(self.expected, self.rows[:-1])

    def test_unexpected_is_rejected(self):
        extra = dict(case="unexpected", sequence=99, signal="logs", sha256="extra")
        with self.assertRaisesRegex(AssertionError, "missing or unexpected"):
            campaign.verify_delivery(self.expected, self.rows + [extra])


class SettledOutcomeTests(unittest.TestCase):
    def setUp(self):
        self.owner = campaign.Campaign(Path("/synthetic"))
        self.owner.workers["live"] = dict(state=Path("/synthetic/state"))
        self.owner.accepted["live"] = 3
        self.capacity = dict(recovery_ready=True, pending_upper_bound=0)
        self.generation = dict(released_through=3)
        self.owner.monitor = lambda worker: dict(otlpStorage=dict(capacity=self.capacity))
        self.owner.generation = lambda root: self.generation

    def test_settled_checkpoint_is_ready(self):
        self.assertTrue(self.owner.settled("live"))

    def test_sink_presence_does_not_prove_checkpoint(self):
        self.owner.expected = {("final", 1, "logs"): "hash"}
        self.owner.ledger = lambda: [dict(case="final", sequence=1, signal="logs", sha256="hash")]
        self.generation["released_through"] = 2
        self.assertTrue(self.owner.all_delivered())
        self.assertFalse(self.owner.settled("live"))
        self.generation["released_through"] = 3
        self.assertTrue(self.owner.settled("live"))

    def test_pending_outcome_refuses_known_outcome_crash(self):
        self.capacity["pending_upper_bound"] = 1
        self.assertFalse(self.owner.settled("live"))

    def test_unrecovered_refuses_known_outcome_crash(self):
        self.capacity["recovery_ready"] = False
        self.assertFalse(self.owner.settled("live"))

    def test_unknown_capacity_is_not_zero(self):
        del self.capacity["pending_upper_bound"]
        with self.assertRaises(KeyError):
            self.owner.settled("live")


if __name__ == "__main__":
    unittest.main()
