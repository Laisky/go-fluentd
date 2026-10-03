#!/usr/bin/env python3
"""Reject missing/skipped B1 harness runs and mislabeled fast-mode evidence."""
import argparse
import collections
import json
from pathlib import Path

TESTS = (
    "TestOTLPB1IsolatedCapacity",
    "TestRegressionB1AdmissionClassification",
    "TestRegressionB1ByteCapacityEvidenceGate",
    "TestRegressionB1RealAdmissionScanBudget",
    "TestRegressionB1DeliverySet",
)


def verify(rows, repeats, paced):
    if repeats < 1:
        raise ValueError("repeats must be positive")
    passes = collections.Counter()
    phases = collections.Counter()
    finals = 0
    package_passes = 0
    for row in rows:
        if row.get("Action") in ("fail", "skip"):
            raise ValueError(f"failed or skipped test: {row.get('Test', '<package>')}")
        name = row.get("Test", "")
        if row.get("Action") == "pass":
            if name in TESTS:
                passes[name] += 1
            elif not name:
                package_passes += 1
        if name != TESTS[0] or row.get("Action") != "output":
            continue
        output = row.get("Output", "")
        if "B1_SYNTHETIC " in output:
            data = json.loads(output.split("B1_SYNTHETIC ", 1)[1])
            if data.get("fast_validate") is not True or data.get("paced") is not paced:
                raise ValueError("harness ran in the wrong mode")
            if data.get("storage_scan_timeout_ms") != 1000 or data.get("storage_scan_max_entries") != 1024:
                raise ValueError("unexpected fast-mode scan bounds")
            if data.get("scan_budget_rejected") != 0:
                raise ValueError("scan refusal invalidates byte-capacity evidence")
            phase = data["phase"]
            phases[phase] += 1
            if "offered" in data:
                byte_refused = sum(data["byte_limit_refused_per_signal"].values())
                scan_refused = sum(data["scan_budget_refused_per_signal"].values())
                refused = sum(data["refused_per_signal"].values())
                if scan_refused != 0 or refused != byte_refused:
                    raise ValueError("refusals are not exclusively byte-limit refusals")
                if data["admitted"] + refused != data["offered"]:
                    raise ValueError("admission accounting does not reconcile")
                if phase == "home_down_growing_to_full" and not (data["admitted"] > 0 and byte_refused > 0):
                    raise ValueError("offline run did not exercise growth and byte refusal")
        if "B1_SYNTHETIC_FINAL " in output:
            fields = dict(part.split("=", 1) for part in output.split("B1_SYNTHETIC_FINAL ", 1)[1].split())
            if fields.get("fast_validate") != "true" or fields.get("paced") != str(paced).lower():
                raise ValueError("incorrect final mode")
            if fields.get("transport") != "in-process-no-network" or fields.get("preserved") != "true":
                raise ValueError("unexpected harness transport or cleanup")
            if int(fields["attempted"]) != 108 or not (0 < int(fields["admitted"]) < 108):
                raise ValueError("unexpected fixture counts")
            if fields["admitted"] != fields["delivered"]:
                raise ValueError("final delivery count does not reconcile")
            finals += 1
    expected = collections.Counter({name: repeats for name in TESTS})
    if passes != expected or finals != repeats or package_passes != 1:
        raise ValueError(f"incomplete execution: passes={passes}, finals={finals}, package_passes={package_passes}")
    phase_names = {
        "healthy_admission", "healthy_direct_replay", "home_down_growing_to_full",
        "home_down_pending_replay", "home_recovery_replay",
    }
    if paced:
        phase_names.update({"home_down_full_hold", "home_down_remaining_snapshot"})
    if phases != collections.Counter({name: repeats for name in phase_names}):
        raise ValueError(f"missing or unexpected phases: {phases}")


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("log", type=Path)
    parser.add_argument("--repeats", type=int, default=3)
    parser.add_argument("--paced", choices=("0", "1"), required=True)
    args = parser.parse_args()
    with args.log.open(encoding="utf-8") as source:
        verify((json.loads(line) for line in source if line.strip()), args.repeats, args.paced == "1")
    print(f"B1 fast harness: all {len(TESTS)} tests passed {args.repeats} times; paced={args.paced}; evidence reconciled")


if __name__ == "__main__":
    main()
