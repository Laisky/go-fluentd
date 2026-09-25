#!/usr/bin/env python3
"""Re-audit saved wire/receipt evidence in a separate process, with negative controls."""
import argparse
import base64
import copy
import json
from pathlib import Path
import shutil
import tempfile

from run import audit, canonical


def verify(root):
    report = json.loads((root / "results.json").read_text())
    assert report["expected"] == len(report["results"]) and report["results"], "incomplete run"
    totals = {"accepted": 0, "required_deliveries": 0, "rows": 0, "identical_retries": 0}
    for record in report["results"]:
        assert record["passed"], record
        case = root / record["case"]
        assert case.parent == root and case.is_dir()
        config = json.loads((case / "config-1.json").read_text())["settings"]
        assert config["journal"]["group_commit_max_messages"] == report["group"]
        assert config["acceptor"]["recvs"]["plugins"]["events"]["type"] == "http_events"
        assert all(c["type"] == "http_events" for c in config["producer"]["plugins"].values())
        if record["case"].endswith("storage-refused"):
            assert record["storage_status"] == 503 and record["accepted"] == 0
            assert not any((case / f"sink{i}.jsonl").exists() for i in range(2))
            continue
        actual = audit(case, record["case"].split("-")[0], report["seed"], 13)
        for key, value in actual.items():
            assert value == record[key], f"incorrect summary: {key}"
            totals[key] += value
    return totals


def negative_controls(root):
    report = json.loads((root / "results.json").read_text())
    case_name = next(r["case"] for r in report["results"] if r["case"].startswith("structured-")
                     and not r["case"].endswith("storage-refused"))
    source = root / case_name
    modes = ("missing", "changed", "receipt", "identity", "joint-forgery")
    for corruption in modes:
        with tempfile.TemporaryDirectory(prefix="event-evidence-control-") as directory:
            case = Path(directory) / "case"
            case.mkdir()
            for name in ("manifest.json", "receipts.json", "sink0.jsonl", "sink1.jsonl"):
                shutil.copyfile(source / name, case / name)
            sink = case / "sink0.jsonl"
            rows = [json.loads(line) for line in sink.read_text().splitlines()]
            if corruption == "missing":
                sink.write_text("")
            elif corruption == "receipt":
                values = json.loads((case / "receipts.json").read_text())
                values[0] = 503
                (case / "receipts.json").write_text(canonical(values))
            elif corruption == "joint-forgery":
                manifest = json.loads((case / "manifest.json").read_text())
                target = manifest[0]["id"]
                manifest[0]["data"]["text"] = "forged together"
                (case / "manifest.json").write_text(canonical(manifest))
                for name in ("sink0.jsonl", "sink1.jsonl"):
                    path = case / name
                    content = [json.loads(line) for line in path.read_text().splitlines()]
                    for row in content:
                        if row["records"][0]["id"] == target:
                            row["records"][0]["data"]["text"] = "forged together"
                            row["body"] = base64.b64encode(canonical(row["records"][0]).encode()).decode()
                    path.write_text("".join(canonical(r) + "\n" for r in content))
            else:
                if corruption == "changed":
                    rows[0]["records"][0]["data"]["nested"]["on"] = 1  # bool != integer
                    rows[0]["body"] = base64.b64encode(canonical(rows[0]["records"][0]).encode()).decode()
                else:
                    rows[0]["headers"]["x-go-fluentd-id"] = "wrong-id"
                sink.write_text("".join(canonical(r) + "\n" for r in rows))
            try:
                audit(case, "structured", report["seed"], 13)
            except (AssertionError, ValueError):
                continue
            raise AssertionError(f"auditor accepted {corruption}")
    return list(modes)


def main():
    if not __debug__:
        raise RuntimeError("do not run acceptance auditing with Python -O")
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--artifacts", type=Path, required=True)
    args = parser.parse_args()
    root = args.artifacts.resolve(strict=True)
    result = {"passed": True, "totals": verify(root), "rejected_controls": negative_controls(root)}
    (root / "audited.json").write_text(json.dumps(result, indent=2))
    print(json.dumps(result))


if __name__ == "__main__":
    main()
