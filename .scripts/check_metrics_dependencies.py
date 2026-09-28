#!/usr/bin/env python3
"""Keep the metrics-only integration free of the removed JWT wrapper.

This is a narrow dependency-regression policy, not a vulnerability scanner.
The patched jwt/v4 version is not itself declared vulnerable by this check.
"""
from __future__ import annotations

import argparse
import json
from pathlib import Path
import subprocess

METRICS = "github.com/zsais/go-gin-prometheus"
REMOVED = frozenset({
    "github.com/Laisky/gin-middlewares",
    "github.com/golang-jwt/jwt",
    "github.com/golang-jwt/jwt/v4",
})


def objects(text):
    decoder = json.JSONDecoder()
    result = []
    while text.strip():
        text = text.lstrip()
        item, end = decoder.raw_decode(text)
        if not isinstance(item, dict):
            raise ValueError("expected JSON objects from go list")
        result.append(item)
        text = text[end:]
    if not result:
        raise ValueError("empty dependency evidence")
    return result


def check(modules, packages):
    if not modules or not packages:
        raise ValueError("empty dependency evidence")
    selected = {}
    for module in modules:
        path = module.get("Path")
        if not path or path in selected or module.get("Error") or module.get("Replace"):
            raise ValueError("incomplete, duplicate or replaced module evidence")
        selected[path] = module.get("Version", "")
    if not any(m.get("Main") and m.get("Path") == "gofluentd" for m in modules):
        raise ValueError("evidence is not for gofluentd")
    forbidden = REMOVED.intersection(selected)
    if forbidden:
        raise ValueError("removed metrics/JWT modules reintroduced: " + ", ".join(sorted(forbidden)))
    if METRICS not in selected or not selected[METRICS]:
        raise ValueError("metrics dependency missing from selected graph")
    imports = set()
    for package in packages:
        path = package.get("ImportPath")
        if not path or package.get("Error") or package.get("DepsErrors") or package.get("Incomplete"):
            raise ValueError("incomplete package evidence")
        imports.add(path)
        module = package.get("Module", {})
        if module.get("Error") or module.get("Replace"):
            raise ValueError("replaced or unresolved package module")
        if module.get("Path") in REMOVED or any(path == name or path.startswith(name + "/") for name in REMOVED):
            raise ValueError("removed metrics/JWT package reintroduced: " + path)
    if METRICS not in imports or "gofluentd/internal/controller" not in imports:
        raise ValueError("controller or metrics missing from package evidence")
    return {"passed": True, "metrics_module": METRICS, "metrics_version": selected[METRICS],
            "module_count": len(modules), "package_count": len(packages),
            "removed_modules": sorted(REMOVED)}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("out", type=Path)
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = Path(__file__).resolve().parents[1]
    data = {}
    for label, command in {
        "modules": ["go", "list", "-mod=readonly", "-m", "-json", "all"],
        "packages": ["go", "list", "-mod=readonly", "-deps", "-test", "-json", "./..."],
    }.items():
        result = subprocess.run(command, cwd=root, text=True, capture_output=True, timeout=180)
        (out / (label + ".json")).write_text(result.stdout)
        (out / (label + ".stderr")).write_text(result.stderr)
        result.check_returncode()
        data[label] = objects(result.stdout)
    result = check(data["modules"], data["packages"])
    report = json.dumps(result, indent=2) + "\n"
    (out / "metrics-dependencies.json").write_text(report)
    print(report, end="")


if __name__ == "__main__":
    main()
