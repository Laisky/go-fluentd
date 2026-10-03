#!/usr/bin/env python3
"""Run the unchanged MessagePack GC gate and report failures in the job log."""
from __future__ import annotations

import collections
import json
import os
from pathlib import Path
import subprocess
import sys
import time

PACKAGE = "gofluentd/internal/senders"
TESTS = (
    "TestRegressionFluentConcurrentTagRouting",
    "TestRegressionFluentWireOracleRejectsWrongRoute",
)
REPETITIONS = 1000


def inspect_results(path: Path, repetitions: int = REPETITIONS) -> tuple[collections.Counter, list[str], list[str]]:
    """Stream events with bounded diagnostics; never accept a partial test run."""
    counts = collections.Counter()
    problems = set()
    tail = collections.deque(maxlen=100)
    with path.open(encoding="utf-8") as stream:
        for line in stream:
            try:
                row = json.loads(line)
                if not isinstance(row, dict):
                    raise ValueError("event is not an object")
            except (ValueError, TypeError):
                problems.add("non-JSON or malformed event output")
                tail.append(line[:4096])
                continue
            action = row.get("Action")
            test = row.get("Test", "")
            text = row.get("Output", "")
            if isinstance(text, str) and text:
                tail.append(text[:4096])
            # Go interleaves BuildEvents (ImportPath) with TestEvents (Package).
            # Build output is diagnostic, but any build failure fails the gate.
            if action in ("build-output", "build-fail"):
                if action == "build-fail":
                    problems.add("build failure event")
                continue
            if row.get("Package") != PACKAGE:
                problems.add("unexpected or missing package")
            if test and test not in TESTS:
                problems.add("unexpected test")
            if action in ("fail", "skip"):
                problems.add("failed or skipped test/package event")
            if action in ("run", "pass") and test in (*TESTS, ""):
                counts[(test, action)] += 1
    for test in TESTS:
        if counts[(test, "run")] != repetitions or counts[(test, "pass")] != repetitions:
            problems.add(f"{test}: expected {repetitions} starts and passes")
    if counts[("", "pass")] != 1:
        problems.add("missing or duplicate package completion")
    return counts, sorted(problems), list(tail)


def main(argv: list[str] | None = None) -> int:
    args = sys.argv[1:] if argv is None else argv
    if len(args) != 1:
        print("usage: run_msgpack_gc.py OUTPUT.jsonl", file=sys.stderr)
        return 2
    path = Path(args[0])
    command = [
        "go", "test", "-mod=readonly", "-race", f"-count={REPETITIONS}",
        "-timeout=180s", "-json", "-run", "^(" + "|".join(TESTS) + ")$",
        "./internal/senders",
    ]
    env = dict(os.environ, GOMAXPROCS="2", GOGC="10")
    started = time.monotonic()
    try:
        path.parent.mkdir(parents=True, exist_ok=True)
        with path.open("w", encoding="utf-8") as output:
            # Do not raise/exit on the subprocess failure: print the preserved
            # JSON diagnostics first, then propagate a failing gate result.
            result = subprocess.run(command, env=env, stdout=output,
                                    stderr=subprocess.STDOUT, check=False)
        counts, problems, tail = inspect_results(path)
    except (OSError, UnicodeError) as exc:
        print(f"MessagePack GC gate could not run/read its evidence: {exc}", file=sys.stderr)
        return 2
    print(f"MessagePack GC exit={result.returncode}, elapsed={time.monotonic() - started:.3f}s")
    for test in TESTS:
        print(f"{test}: starts={counts[(test, 'run')]} passes={counts[(test, 'pass')]}/{REPETITIONS}")
    if result.returncode != 0 or problems:
        for problem in problems:
            print(f"ERROR: {problem}")
        print("Last test output (full output retained in artifact):")
        print("".join(tail))
        return 1
    print("MessagePack GC gate passed; full package completion verified.")
    return 0


if __name__ == "__main__":
    sys.exit(main())
