#!/usr/bin/env python3
"""Require semantic failures for deliberately unsafe disposition-store variants.

Each variant receives exactly the same tests. Only disposable package copies
change. Build failures, panics, timeouts and missing tests are NOT reproductions.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile


def run(root: Path, expression: str, path: Path, expected_failure: str | None) -> dict:
    cmd = ["go", "test", "-mod=readonly", "-count=1", "-timeout=30s", "-json",
           "-run", expression, "./internal/otlpstate"]
    result = subprocess.run(cmd, cwd=root, capture_output=True, timeout=60)
    path.write_bytes(result.stdout + result.stderr)
    records = []
    for line in result.stdout.splitlines():
        try:
            records.append(json.loads(line))
        except (ValueError, UnicodeError):
            pass
    failures = {r["Test"] for r in records if r.get("Action") == "fail" and "Test" in r}
    passes = {r["Test"] for r in records if r.get("Action") == "pass" and "Test" in r}
    assert not any(r.get("Action") == "skip" for r in records), path
    output = (result.stdout + result.stderr).decode(errors="replace")
    assert not any(term in output for term in ["[build failed]", "panic:", "fatal error:", "DATA RACE", "test timed out"]), path
    if expected_failure:
        assert result.returncode == 1 and expected_failure in failures, (path, result.returncode, failures)
    else:
        assert result.returncode == 0 and passes and not failures, (path, result.returncode)
    return {"command": cmd, "exit": result.returncode, "failed": sorted(failures),
            "passed": sorted(passes), "sha256": hashlib.sha256(path.read_bytes()).hexdigest()}


def main() -> None:
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifacts", type=Path, required=True)
    args = parser.parse_args()
    args.artifacts.mkdir(parents=True, exist_ok=True)
    source = Path(__file__).resolve().parents[1]
    code = (source / "internal/otlpstate/store.go").read_text()
    variants = [
        ("omit-file-sync", "err = s.syncRecord(f)", "err = nil",
         "TestDurabilityBarriersBeforeQuarantineReceipt/file-sync"),
        ("omit-directory-sync", "if err = s.syncDirectory(s.dirFile); err != nil {",
         "if err = error(nil); err != nil {",
         "TestDurabilityBarriersBeforeQuarantineReceipt/directory-sync"),
        ("ignore-persistence-error",
         "if err := s.persist(name, entry{1, k, e, o, time.Now().UTC().Format(time.RFC3339Nano)}); err != nil {",
         "if err := s.persist(name, entry{1, k, e, o, time.Now().UTC().Format(time.RFC3339Nano)}); false && err != nil {",
         "TestPersistenceFailureNeverAcknowledgesOrReexports/file-sync"),
        ("retry-known-terminal", "if old != nil {", "if false && old != nil {",
         "TestTerminalSurvivesReopenWithoutCallingDestination/metrics/application/json/partially_rejected"),
    ]
    summary = {}
    with tempfile.TemporaryDirectory(prefix="otlp-state-controls-") as tmp:
        root = Path(tmp)
        shutil.copytree(source / "internal/otlpstate", root / "internal/otlpstate")
        for file in ("go.mod", "go.sum"):
            shutil.copyfile(source / file, root / file)
        summary["baseline"] = run(root, "Test", args.artifacts / "baseline.jsonl", None)
        for name, old, new, failure in variants:
            assert code.count(old) == 1, (name, "mutation target changed")
            (root / "internal/otlpstate/store.go").write_text(code.replace(old, new, 1))
            # Split Go -run segments explicitly; the slash in application/json
            # creates nested matcher segments but is part of the original name.
            expression = "/".join("^" + segment + "$" for segment in failure.split("/"))
            summary[name] = run(root, expression, args.artifacts / f"{name}.jsonl", failure)
            summary[name + "-control"] = run(root, "^TestTransportFailuresRemainRetryable$",
                                                   args.artifacts / f"{name}-control.jsonl", None)
        (root / "internal/otlpstate/store.go").write_text(code)
    summary["source_sha256"] = hashlib.sha256(code.encode()).hexdigest()
    (args.artifacts / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print("PASS: safe baseline, four semantic mutation failures, four passing transport controls")


if __name__ == "__main__":
    main()
