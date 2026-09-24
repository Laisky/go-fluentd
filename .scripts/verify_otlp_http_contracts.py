#!/usr/bin/env python3
"""Require named assertion failures from unsafe HTTP transport variants."""
import argparse
import hashlib
import json
import pathlib
import shutil
import subprocess
import tempfile

ROOT = pathlib.Path(__file__).resolve().parents[1]
CASES = [
    ("ignore-admission-error", "receiver.go",
     "if err := h.admit(ctx, req); err != nil {",
     "if err := h.admit(ctx, req); err != nil && false {",
     "TestOTLPHTTPReceiverRejection", "admission rejection"),
    ("retry-partial", "exporter.go", "if !out.MayRetry() {",
     "if !out.MayRetry() && out.Disposition != otlpwire.PartiallyRejected {",
     "TestOTLPHTTPExporterResponsePolicy", "response policy"),
    ("partial-is-accepted", "exporter.go", "last.Kind = otlpstate.Partial",
     "last.Kind = otlpstate.Accepted",
     "TestOTLPHTTPExporterResponsePolicy", "response policy"),
    ("retain-oversized-response", "exporter.go", "over := int64(len(raw)) > e.cfg.ResponseBytes",
     "over := false",
     "TestOTLPHTTPExporterFailureBounds", "bounded invalid response"),
    ("follow-redirect", "exporter.go", "return http.ErrUseLastResponse", "return nil",
     "TestOTLPHTTPExporterTLSAndRedirect", "redirect followed"),
]


def run(root, target, artifact):
    proc = subprocess.run(
        ["go", "test", "-mod=readonly", "-count=1", "-timeout=30s", "-json",
         "-run", "^" + target + "$", "./internal/otlphttp"],
        cwd=root, stdout=subprocess.PIPE, stderr=subprocess.STDOUT, timeout=90,
    )
    text = proc.stdout.decode("utf-8", "replace")
    artifact.write_text(text)
    events = []
    for line in text.splitlines():
        try:
            events.append(json.loads(line))
        except json.JSONDecodeError:
            pass
    forbidden = ("panic:", "test timed out", "DATA RACE", "[build failed]")
    assert not any(word in text for word in forbidden), artifact
    assert not any(e.get("Action") == "skip" for e in events), artifact
    assert any(e.get("Action") == "run" and e.get("Test") == target for e in events), artifact
    return proc.returncode, text, events


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument("--artifacts", type=pathlib.Path, required=True)
    args = parser.parse_args()
    out = args.artifacts.resolve()
    out.mkdir(parents=True, exist_ok=True)
    summary = []
    for name, filename, old, new, target, assertion in CASES:
        rc, _, events = run(ROOT, target, out / (name + "-safe.jsonl"))
        assert rc == 0 and any(e.get("Action") == "pass" and e.get("Test") == target for e in events)
        with tempfile.TemporaryDirectory(prefix="otlp-http-mutant-") as tmp:
            root = pathlib.Path(tmp) / "source"
            shutil.copytree(ROOT, root, ignore=shutil.ignore_patterns(".git", "__pycache__", ".audit"))
            path = root / "internal/otlphttp" / filename
            source = path.read_text()
            assert source.count(old) == 1, (name, source.count(old))
            path.write_text(source.replace(old, new))
            rc, text, events = run(root, target, out / (name + ".jsonl"))
            assert rc != 0 and assertion in text, name
            assert any(e.get("Action") == "fail" and e.get("Test") == target for e in events), name
            control = "TestOTLPHTTPExporterRejectsInvalidBeforeNetwork"
            rc, _, events = run(root, control, out / (name + "-control.jsonl"))
            assert rc == 0 and any(e.get("Action") == "pass" and e.get("Test") == control for e in events), name
        summary.append({"mutant": name, "assertion": assertion, "safe_passed": True,
                        "mutant_rejected": True, "control_passed": True,
                        "source_sha256": hashlib.sha256(source.encode()).hexdigest()})
    (out / "summary.json").write_text(json.dumps(summary, indent=2) + "\n")
    print(f"Verified {len(summary)} assertion-level mutations and positive controls")


if __name__ == "__main__":
    main()
