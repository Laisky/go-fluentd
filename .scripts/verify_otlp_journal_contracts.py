#!/usr/bin/env python3
"""Reject unsafe journal lifecycle variants in disposable source copies.

The production checkout is never edited. Assertion-level failures are required;
build errors, panic, race warnings, timeout, skipped or missing tests fail the gate.
"""
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile


def run(root, package, expression, target, log, must_fail=False):
    cmd = ["go", "test", "-mod=readonly", "-count=1", "-timeout=45s", "-json",
           "-run", expression, package]
    p = subprocess.run(cmd, cwd=root, capture_output=True, timeout=120)
    raw = p.stdout + p.stderr
    log.write_bytes(raw)
    records = []
    for line in p.stdout.splitlines():
        try:
            records.append(json.loads(line))
        except (ValueError, UnicodeError):
            pass
    failed = {r["Test"] for r in records if r.get("Action") == "fail" and "Test" in r}
    passed = {r["Test"] for r in records if r.get("Action") == "pass" and "Test" in r}
    output = raw.decode(errors="replace")
    assert not any(r.get("Action") == "skip" for r in records), log
    assert not any(s in output for s in ("[build failed]", "panic:", "fatal error:",
                                        "DATA RACE", "test timed out")), log
    if must_fail:
        assert p.returncode == 1 and target in failed, (log, p.returncode, failed)
        assert any(r.get("Test") == target and ".go:" in r.get("Output", "")
                   for r in records), (log, "missing assertion diagnostic")
    else:
        assert p.returncode == 0 and target in passed and not failed, (log, p.returncode)
    return {"command": cmd, "exit": p.returncode, "failed": sorted(failed),
            "passed": sorted(passed), "sha256": hashlib.sha256(raw).hexdigest()}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, required=True)
    args = parser.parse_args()
    args.artifacts = args.artifacts.resolve()
    args.artifacts.mkdir(parents=True, exist_ok=True)
    source = Path(__file__).resolve().parents[1]
    lifecycle = "internal/controller/otlp_journal.go"
    control = "TestOTLPJournalInvalidConfigurationAndGeneration/legacy-data"
    variants = [
        ("early-admission", lifecycle,
         "if err = p.syncWAL(); err != nil {\n\t\treturn p.poison(err)\n\t}",
         "if err = nil; err != nil {\n\t\treturn p.poison(err)\n\t}",
         "TestOTLPJournalAdmissionWaitsForSync", control),
        ("omit-replay-copy", lifecycle,
         "if err = p.writeWAL(d); err == nil {\n\t\t\terr = p.syncWAL()\n\t\t}",
         "err = nil",
         "TestOTLPJournalBoundedReplayDoesNotRestartCursor", control),
        ("restart-cursor", lifecycle, "if !p.replayOpen {", "if true {",
         "TestOTLPJournalBoundedReplayDoesNotRestartCursor", control),
        ("ignore-namespace", lifecycle, "decodeErr != nil || r.Namespace != p.namespace",
         "decodeErr != nil || false",
         "TestOTLPJournalRefusesMissingGenerationAndForeignWrapper/foreign-wrapper", control),
        ("ignore-append-error", lifecycle,
         "if err = p.writeWAL(d); err != nil {\n\t\treturn p.poison(err)\n\t}",
         "if err = p.writeWAL(d); err != nil {\n\t\treturn nil\n\t}",
         "TestOTLPJournalStorageFailureStopsFurtherAdmission/append", control),
    ]
    original = {f: (source / f).read_text() for f in (lifecycle,)}
    summary = {"source_sha256": {f: hashlib.sha256(c.encode()).hexdigest()
                                  for f, c in original.items()}}
    with tempfile.TemporaryDirectory(prefix="otlp-lifecycle-controls-") as tmp:
        root = Path(tmp) / "repo"
        # Only sources and fixtures needed by these package tests are copied.
        root.mkdir()
        for d in ("internal", "library", "docs"):
            shutil.copytree(source / d, root / d)
        for f in ("go.mod", "go.sum"):
            shutil.copyfile(source / f, root / f)
        for name, file, old, new, target, control in variants:
            for f, c in original.items():
                (root / f).write_text(c)
            package = "./" + str(Path(file).parent)
            expression = "/".join("^" + x + "$" for x in target.split("/"))
            summary[name + "-safe"] = run(root, package, expression, target,
                                         args.artifacts / (name + "-safe.jsonl"))
            assert original[file].count(old) == 1, (name, "mutation target changed")
            (root / file).write_text(original[file].replace(old, new, 1))
            summary[name] = run(root, package, expression, target,
                                args.artifacts / (name + ".jsonl"), True)
            summary[name + "-control"] = run(root, package, "/".join("^" + x + "$" for x in control.split("/")), control,
                                             args.artifacts / (name + "-control.jsonl"))
    (args.artifacts / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print("PASS: five safe assertions, five unsafe-variant failures, five positive controls")


if __name__ == "__main__":
    main()
