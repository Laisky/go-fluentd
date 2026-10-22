"""Regression controls for the historical campaign's delivery oracle."""
import copy
import importlib.util
import json
import io
import subprocess
import sys
import tempfile
from pathlib import Path
import unittest
from contextlib import redirect_stderr
from unittest import mock

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


class RestartBarrierTests(unittest.TestCase):
    def setUp(self):
        self.owner = campaign.Campaign(Path("/synthetic"))
        self.rows = []
        self.controls = []
        for signal in campaign.SIGNALS:
            self.owner.expected[("earlier", 1, signal)] = "old-" + signal
            self.rows.append(dict(case="earlier", sequence=1, signal=signal, sha256="old-" + signal))
        self.owner.control = lambda **values: self.controls.append(values)
        self.owner.ledger = lambda: list(self.rows)
        self.owner.settled = lambda worker: True
        self.owner.emit = self.emit

    def emit(self, worker, case, sequence, accepted=True):
        self.assertEqual((worker, case, sequence, accepted), ("live", "post-restart-barrier", 5, True))
        self.owner.accepted[worker] += len(campaign.SIGNALS)
        for signal in campaign.SIGNALS:
            self.owner.expected[(case, sequence, signal)] = "new-" + signal
            self.rows.append(dict(case=case, sequence=sequence, signal=signal, sha256="new-" + signal))

    def check(self):
        # No-sleep fake polling: a missing fence is a failure, not a timeout.
        def immediate(predicate, message, seconds=40):
            self.assertTrue(predicate(), message)
        with mock.patch.object(campaign, "until", side_effect=immediate):
            return self.owner.verify_after_restart("live", 5)

    def test_fresh_sentinel_is_delivered_and_checkpointed(self):
        counts = self.check()
        self.assertEqual(len(counts), 6)
        self.assertEqual(self.owner.accepted["live"], 3)
        self.assertEqual(self.controls, [dict(block_all=False, block_old=False)])

    def test_late_duplicate_after_old_checkpoint_is_rejected(self):
        # The old immediate snapshot passes. An erroneous retained replay
        # before the new sentinel must cause a duplicate verdict.
        self.assertEqual(len(campaign.verify_delivery(self.owner.expected, self.rows)), 3)
        def delayed_emit(*args, **kwargs):
            self.rows.append(copy.deepcopy(self.rows[0]))
            self.emit(*args, **kwargs)
        self.owner.emit = delayed_emit
        with self.assertRaisesRegex(AssertionError, "duplicated"):
            self.check()

    def test_missing_postrestart_sentinel_cannot_pass(self):
        def dropped_emit(*args, **kwargs):
            self.emit(*args, **kwargs)
            del self.rows[-len(campaign.SIGNALS):]
        self.owner.emit = dropped_emit
        with self.assertRaisesRegex(AssertionError, "post-restart sentinel"):
            self.check()

    def test_unsettled_postrestart_sentinel_cannot_pass(self):
        self.owner.settled = lambda worker: False
        with self.assertRaisesRegex(AssertionError, "post-restart sentinel lacks durable checkpoint"):
            self.check()


class CleanupTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.owner = campaign.Campaign(Path(tmp.name))
        self.owner.label = "synthetic-owner"
        self.owner.names = ["sink", "live"]
        self.commands = []
        self.fail_logs = None
        self.foreign = None

        def command(args, **kwargs):
            self.commands.append(list(args))
            kind, name = args[1], args[-1]
            if kind == "inspect":
                return "foreign-owner" if name == self.foreign else self.owner.label
            if kind == "logs":
                if name == self.fail_logs:
                    raise subprocess.CalledProcessError(7, args)
                return b"synthetic-only-log"
            if kind == "rm":
                return b""
            raise AssertionError("unexpected Docker command")
        self.owner.command = command

    def removed(self):
        return [args[-1] for args in self.commands if args[1] == "rm"]

    def test_log_failure_still_removes_every_owned_container(self):
        self.fail_logs = "live"
        with self.assertRaisesRegex(RuntimeError, "log capture failed"):
            self.owner.cleanup()
        self.assertEqual(self.removed(), ["live", "sink"])
        self.assertEqual((self.owner.root / "sink.log").read_bytes(), b"synthetic-only-log")

    def test_foreign_container_is_not_removed(self):
        self.foreign = "sink"
        with self.assertRaisesRegex(RuntimeError, "ownership label mismatch"):
            self.owner.cleanup()
        self.assertEqual(self.removed(), ["live"])

    def test_clean_success(self):
        self.owner.cleanup()
        self.assertEqual(self.removed(), ["live", "sink"])


class CampaignExecutionTests(unittest.TestCase):
    def test_primary_failure_survives_cleanup_failure(self):
        class BothFail:
            def run(self):
                raise ValueError("primary qualification failed")
            def cleanup(self):
                raise RuntimeError("cleanup failed")
        stderr = io.StringIO()
        with redirect_stderr(stderr):
            with self.assertRaisesRegex(ValueError, "primary qualification failed"):
                campaign.execute_campaign(BothFail())
        self.assertIn("cleanup failed", stderr.getvalue())

    def test_cleanup_failure_blocks_success(self):
        class CleanupFail:
            def run(self):
                return {"passed": True}
            def cleanup(self):
                raise RuntimeError("cleanup failed")
        with self.assertRaisesRegex(RuntimeError, "cleanup failed"):
            campaign.execute_campaign(CleanupFail())



class AdmissionEvidenceTests(unittest.TestCase):
    """RED controls: HTTP 503 alone does not identify storage-capacity refusal."""

    def setUp(self):
        self.owner = campaign.Campaign(Path("/synthetic"))
        self.owner.workers["live"] = {"listen": 4320}
        self.rejections = 0
        self.owner.monitor = lambda worker: {
            "otlpStorage": {"admissionRejected": self.rejections}
        }

    def test_503_without_storage_rejection_must_fail_capacity_qualification(self):
        self.owner.http = lambda *args, **kwargs: (503, b"")
        with self.assertRaisesRegex(AssertionError, "storage admission rejection"):
            self.owner.emit("live", "pressure-refused", 0, False)

    def test_503_with_three_storage_rejections_is_qualified(self):
        def refused(*args, **kwargs):
            self.rejections += 1
            return 503, b""
        self.owner.http = refused
        self.owner.emit("live", "pressure-refused", 0, False)
        self.assertEqual(self.rejections, len(campaign.SIGNALS))
        self.assertFalse(self.owner.expected)


class SuccessfulCampaignEvidenceTests(unittest.TestCase):
    """RED control: success is durable only after cleanup succeeds."""

    def test_summary_is_published_after_successful_cleanup(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            order = []

            class SyntheticSuccess:
                def __init__(self):
                    self.root = root

                def run(self):
                    order.append("run")
                    return {"passed": True, "checks": [{"check": "synthetic"}]}

                def cleanup(self):
                    order.append("cleanup")

            result = campaign.execute_campaign(SyntheticSuccess())
            self.assertEqual(order, ["run", "cleanup"])
            self.assertTrue((root / "summary.json").is_file(), "cleaned successful campaign lacks summary")
            self.assertEqual(json.loads((root / "summary.json").read_text()), result)

    def test_failed_cleanup_does_not_publish_passed_summary(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)

            class SyntheticCleanupFailure:
                def __init__(self):
                    self.root = root

                def run(self):
                    return {"passed": True}

                def cleanup(self):
                    raise RuntimeError("cleanup-failed")

            with self.assertRaisesRegex(RuntimeError, "cleanup-failed"):
                campaign.execute_campaign(SyntheticCleanupFailure())
            self.assertFalse((root / "summary.json").exists())



class HistoricalCheckpointEvidenceTests(unittest.TestCase):
    def test_complete_historical_frontier_is_accepted(self):
        original = {"namespace": "historical"}
        current = {"namespace": "historical", "released_through": 36}
        self.assertEqual(campaign.verify_historical_checkpoint(current, original, 36), current)

    def test_stale_historical_frontier_rejected_despite_zero_pending(self):
        old_capacity = {"recovery_ready": True, "pending_upper_bound": 0}
        self.assertTrue(old_capacity["recovery_ready"] and old_capacity["pending_upper_bound"] == 0)
        with self.assertRaisesRegex(AssertionError, "historical checkpoint"):
            campaign.verify_historical_checkpoint(
                {"namespace": "historical", "released_through": 35},
                {"namespace": "historical"},
                36,
            )

    def test_foreign_historical_namespace_is_rejected(self):
        with self.assertRaisesRegex(AssertionError, "historical WAL namespace"):
            campaign.verify_historical_checkpoint(
                {"namespace": "wrong", "released_through": 36},
                {"namespace": "historical"},
                36,
            )


class OptimizedInterpreterTests(unittest.TestCase):
    def test_optimized_interpreter_cannot_skip_campaign_assertions(self):
        process = subprocess.run(
            [sys.executable, "-O", str(Path(__file__).with_name("run.py")), "--help"],
            capture_output=True, text=True, check=False,
        )
        self.assertNotEqual(process.returncode, 0)
        self.assertIn("requires Python assertions", process.stderr)


if __name__ == "__main__":
    unittest.main()
