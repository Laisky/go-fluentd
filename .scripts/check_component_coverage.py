#!/usr/bin/env python3
"""Check statement coverage from an unmodified `go test -coverprofile` file."""
import argparse
import sys
from collections import defaultdict
from pathlib import Path, PurePosixPath

# Floors leave room for nondeterministic error/logging branches. They include
# all production statements, including generated codecs and configuration code.
FLOORS = {
    "internal/acceptorfilters": 75.0,
    "internal/controller": 45.0,
    "internal/monitor": 95.0,
    "internal/postfilters": 90.0,
    "internal/recvs": 60.0,
    "internal/senders": 78.0,
    "internal/tagfilters": 78.0,
    "library": 68.0,
}


def check(profile: Path) -> bool:
    counts = defaultdict(lambda: [0, 0])  # covered, total
    with profile.open(encoding="utf-8") as source:
        if not source.readline().startswith("mode: "):
            raise ValueError("not a Go coverage profile")
        for line_number, line in enumerate(source, 2):
            if not line.strip():
                continue
            try:
                location, statements, executions = line.split()
                statements, executions = int(statements), int(executions)
                file_name = location.rsplit(":", 1)[0]
                package = str(PurePosixPath(file_name).parent)
                prefix = "gofluentd/"
                if package == "gofluentd":
                    package = "."
                elif package.startswith(prefix):
                    package = package[len(prefix):]
                if statements < 0 or executions < 0:
                    raise ValueError("negative coverage value")
            except ValueError as exc:
                raise ValueError(f"invalid coverage record at line {line_number}") from exc
            counts[package][1] += statements
            if executions:
                counts[package][0] += statements
    failed = False
    for package, floor in FLOORS.items():
        covered, total = counts[package]
        percentage = 100.0 * covered / total if total else 0.0
        print(f"{package:28s} {percentage:5.1f}% (minimum {floor:.1f}%)")
        if not total or percentage < floor:
            failed = True
    covered = sum(value[0] for value in counts.values())
    total = sum(value[1] for value in counts.values())
    percentage = 100.0 * covered / total if total else 0.0
    print(f"{'TOTAL (all packages)':28s} {percentage:5.1f}% (minimum 64.0%)")
    return not failed and bool(total) and percentage >= 64.0


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile", type=Path)
    args = parser.parse_args()
    try:
        return 0 if check(args.profile) else 1
    except (OSError, ValueError) as exc:
        print(f"coverage check failed: {exc}", file=sys.stderr)
        return 2


if __name__ == "__main__":
    sys.exit(main())
