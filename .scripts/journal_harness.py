"""Adapt copied benchmark fixtures to a selected journal ABI without changing pins."""
import argparse
import json
import subprocess
import re
from pathlib import Path

V1 = "github.com/Laisky/go-journal"
V2 = V1 + "/v2"
NOP = "quietLogger := zap.NewNop()"
LEGACY_NOP = "quietLogger := &utils.LoggerType{Logger: zap.NewNop()}"

def adapt_harness(text, kind, module, legacy_level=False):
    if module not in (V1, V2) or kind not in ("performance", "group"):
        raise ValueError("unsupported journal fixture")
    if text.count('"' + V2 + '"') != 1 or ('"' + V1 + '"') in text:
        raise ValueError("expected exactly one native v2 import")
    if kind == "performance" and text.count(NOP) != 1:
        raise ValueError("unexpected recovery logger fixture")
    if kind == "group" and "journal.NewLegacyLoader(" in text:
        raise ValueError("unexpected group recovery fixture")
    adapted = text
    if module == V1:
        adapted = adapted.replace('"' + V2 + '"', '"' + V1 + '"')
        if kind == "performance":
            adapted = adapted.replace(NOP, LEGACY_NOP)
    level = 'log.SetLevel("error")'
    legacy = 'log.Logger.ChangeLevel("error")'
    if legacy_level:
        if text.count(level) != 1:
            raise ValueError("unexpected quiet logger level fixture")
        adapted = adapted.replace(level, legacy)
    restored = adapted.replace('"' + V1 + '"', '"' + V2 + '"').replace(LEGACY_NOP, NOP)
    if legacy_level:
        restored = restored.replace(legacy, level)
    if restored != text:
        raise ValueError("fixture changed outside journal/logger ABI adaptation")
    return adapted

def journal_requirement(records):
    matches = [m for m in records if m.get("Path") in (V1, V2)]
    if len(matches) != 1 or matches[0].get("Replace") is not None or not matches[0].get("Version"):
        raise ValueError("require one immutable journal selection without replacement")
    return matches[0]

def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("source", type=Path)
    parser.add_argument("destination", type=Path)
    parser.add_argument("--root", required=True, type=Path)
    parser.add_argument("--kind", required=True, choices=("performance", "group"))
    parser.add_argument("--evidence", required=True, type=Path)
    args = parser.parse_args()
    if args.source.resolve() == args.destination.resolve():
        parser.error("copy into the comparison side; do not rewrite the native fixture")
    if not args.destination.resolve().is_relative_to(args.root.resolve()):
        parser.error("destination must be inside the comparison checkout")
    raw = subprocess.check_output(["go", "list", "-mod=readonly", "-m", "-json", "all"], cwd=args.root, text=True)
    decoder, records, index = json.JSONDecoder(), [], 0
    while index < len(raw):
        while index < len(raw) and raw[index].isspace():
            index += 1
        if index == len(raw):
            break
        record, index = decoder.raw_decode(raw, index)
        records.append(record)
    module = journal_requirement(records)
    logger_sources = list((args.root / "library/log").glob("*.go"))
    if not logger_sources:
        parser.error("comparison logger source is missing")
    legacy_level = not any(re.search(r"(?m)^func SetLevel\(", p.read_text()) for p in logger_sources if not p.name.endswith("_test.go"))
    adapted = adapt_harness(args.source.read_text(), args.kind, module["Path"], legacy_level)
    args.destination.parent.mkdir(parents=True, exist_ok=True)
    args.destination.write_text(adapted)
    evidence = {"commit": subprocess.check_output(["git", "rev-parse", "HEAD"], cwd=args.root, text=True).strip(),
                "module": module["Path"], "version": module["Version"], "kind": args.kind,
                "legacy_abi_adapter": module["Path"] == V1, "legacy_level_adapter": legacy_level, "workload_and_assertions_preserved": True}
    args.evidence.parent.mkdir(parents=True, exist_ok=True)
    args.evidence.write_text(json.dumps(evidence, indent=2) + "\n")
    print(json.dumps(evidence))

if __name__ == "__main__":
    main()
