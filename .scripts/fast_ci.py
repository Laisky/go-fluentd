"""Small, explicit Go unit gate. Missing, skipped, failed tests fail closed."""
import argparse
import json
import re
import subprocess
import sys
import time
from pathlib import Path


def validate(events, expected, returncode):
    if returncode != 0:
        raise RuntimeError(f"go test exited {returncode}")
    if not expected or len(expected) != len(set(expected)):
        raise RuntimeError("expected tests must be nonempty and unique")
    for event in events:
        if event.get("Action") in ("skip", "fail", "build-fail"):
            raise RuntimeError(f"unsuccessful test event: {event}")
    packages = {e.get("Package") for e in events if e.get("Test") in expected}
    if len(packages) != 1:
        raise RuntimeError(f"expected exactly one test package, got {packages}")
    package = next(iter(packages))
    for name in expected:
        actions = [e.get("Action") for e in events if e.get("Test") == name and e.get("Package") == package]
        if actions.count("run") != 1 or actions.count("pass") != 1:
            raise RuntimeError(f"{name}: expected one run and pass, got {actions}")
    if not any(e.get("Package") == package and not e.get("Test") and e.get("Action") == "pass" for e in events):
        raise RuntimeError("missing successful package completion")


def run(command, evidence, label, records, timeout=240):
    start = time.perf_counter()
    record = {"label": label, "command": command, "exit_code": None}
    records.append(record)
    try:
        result = subprocess.run(command, stdout=subprocess.PIPE, stderr=subprocess.PIPE, timeout=timeout)
        record["exit_code"] = result.returncode
        (evidence / f"{label}.stdout").write_bytes(result.stdout)
        (evidence / f"{label}.stderr").write_bytes(result.stderr)
        sys.stderr.write(result.stderr.decode("utf-8", errors="replace"))
        if result.returncode:
            sys.stderr.write(result.stdout.decode("utf-8", errors="replace"))
            raise RuntimeError(f"{label} exited {result.returncode}")
        return result.stdout.decode("utf-8")
    except subprocess.TimeoutExpired as exc:
        record["error"] = "timeout (no successful exit reported)"
        (evidence / f"{label}.stdout").write_bytes(exc.stdout or b"")
        (evidence / f"{label}.stderr").write_bytes(exc.stderr or b"")
        raise RuntimeError(f"{label} exceeded {timeout}s") from exc
    finally:
        record["seconds"] = round(time.perf_counter() - start, 3)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    args.evidence.mkdir(parents=True, exist_ok=True)
    records, started = [], time.perf_counter()
    receipt = {"passed": False, "commands": records}
    try:
        receipt["source"] = run(["git", "rev-parse", "HEAD"], args.evidence, "source", records).strip()
        receipt["toolchain"] = run(["go", "version"], args.evidence, "toolchain", records).strip()
        paths = run(["git", "ls-files", "-z", "--", "*.go"], args.evidence, "tracked-go", records).split("\0")
        paths = [p for p in paths if p]
        if not paths:
            raise RuntimeError("no tracked Go source discovered")
        for index in range(0, len(paths), 50):
            bad = run(["gofmt", "-l", *paths[index:index + 50]], args.evidence, f"format-{index // 50}", records)
            if bad.strip():
                raise RuntimeError(f"gofmt required:\n{bad}")
        suites = json.loads(Path(".scripts/fast_ci_tests.json").read_text(encoding="utf-8"))
        if not suites:
            raise RuntimeError("no essential test suites configured")
        for index, suite in enumerate(suites):
            expected = suite["tests"]
            if not expected or len(expected) != len(set(expected)) or not all(re.fullmatch(r"Test\w+", n) for n in expected):
                raise RuntimeError("test allowlist must contain unique top-level Test names")
            pattern = "^(" + "|".join(expected) + ")$"
            discovered = run(["go", "test", "-mod=readonly", "-list", pattern, suite["package"]], args.evidence, f"discover-{index}", records)
            names = {line for line in discovered.splitlines() if re.fullmatch(r"Test\w+", line)}
            if names != set(expected):
                raise RuntimeError(f"test discovery mismatch: expected {expected}, discovered {sorted(names)}")
            output = run(["go", "test", "-mod=readonly", "-count=1", "-timeout=60s", "-json", "-run", pattern, suite["package"]], args.evidence, f"tests-{index}", records)
            events = [json.loads(line) for line in output.splitlines() if line.strip()]
            validate(events, expected, records[-1]["exit_code"])
            print(f"{suite['package']}: {len(expected)} named unit tests passed", flush=True)
        run(["git", "diff", "--exit-code", "--", "go.mod", "go.sum"], args.evidence, "module-immutability", records)
        receipt["passed"] = True
    except Exception as exc:
        receipt["error"] = str(exc)
        print(str(exc), file=sys.stderr)
    finally:
        receipt["seconds"] = round(time.perf_counter() - started, 3)
        (args.evidence / "receipt.json").write_text(json.dumps(receipt, indent=2) + "\n", encoding="utf-8")
        print(json.dumps({key: receipt[key] for key in ("passed", "seconds", "error") if key in receipt}), flush=True)
    return 0 if receipt["passed"] else 1


if __name__ == "__main__":
    sys.exit(main())
