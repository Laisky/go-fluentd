#!/usr/bin/env python3
"""Validate Go statement coverage without excluding generated or startup code."""
from __future__ import annotations

import argparse
from collections import defaultdict
from pathlib import Path
from typing import Iterable

# Deliberately below observed results to allow small concurrent-path variation.
FLOORS = {
    "total": 74.0,
    "gofluentd/internal/acceptorfilters": 77.0,
    "gofluentd/internal/controller": 68.0,
    "gofluentd/internal/monitor": 99.0,
    "gofluentd/internal/postfilters": 93.0,
    "gofluentd/internal/recvs": 73.0,
    "gofluentd/internal/senders": 82.0,
    "gofluentd/internal/tagfilters": 85.0,
    "gofluentd/library": 71.0,
}


def read_profile(lines: Iterable[str]) -> dict[str, tuple[int, int]]:
    lines = iter(lines)
    if next(lines, "").strip() not in {"mode: set", "mode: count", "mode: atomic"}:
        raise ValueError("missing or invalid Go coverage mode")
    counts: dict[str, list[int]] = defaultdict(lambda: [0, 0])
    for number, line in enumerate(lines, 2):
        if not line.strip():
            continue
        try:
            location, statements, executions = line.split()
            filename, coordinates = location.rsplit(":", 1)
            if "," not in coordinates:
                raise ValueError("missing coverage coordinates")
            package = filename.rsplit("/", 1)[0]
            n, hits = int(statements), int(executions)
            if n < 0 or hits < 0:
                raise ValueError("negative coverage count")
        except (ValueError, IndexError) as error:
            raise ValueError(f"invalid profile line {number}: {line.rstrip()}") from error
        for key in (package, "total"):
            counts[key][0] += n if hits else 0
            counts[key][1] += n
    if counts["total"][1] == 0:
        raise ValueError("coverage profile has no statements")
    return {key: (value[0], value[1]) for key, value in counts.items()}


def check_floors(counts: dict[str, tuple[int, int]], floors: dict[str, float]) -> list[str]:
    errors = []
    for package, minimum in floors.items():
        covered, total = counts.get(package, (0, 0))
        if not total:
            errors.append(f"{package}: missing coverage")
        elif covered * 100 / total < minimum:
            errors.append(f"{package}: {covered * 100 / total:.2f}% < {minimum:.2f}%")
    return errors


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    args = parser.parse_args()
    try:
        with args.profile.open(encoding="utf-8") as stream:
            counts = read_profile(stream)
    except (OSError, ValueError) as error:
        parser.error(str(error))
    for package, (covered, total) in sorted(counts.items()):
        if total:
            print(f"{package}: {covered}/{total} statements ({covered * 100 / total:.2f}%)")
    failures = check_floors(counts, FLOORS)
    for failure in failures:
        print(f"FAIL: {failure}")
    return int(bool(failures))


if __name__ == "__main__":
    raise SystemExit(main())
