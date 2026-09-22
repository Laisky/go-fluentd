#!/usr/bin/env python3
"""Revert isolated implementations in a temporary checkout and require named failures.

Current tests must pass first. A build error or timeout is not accepted as a
behavioral reproduction. No file in the caller's working tree is modified.
"""
import argparse
import json
import subprocess
import tempfile
from pathlib import Path

CASES = [
    ("dispatcher lock", ["internal/controller/dispacher.go"], "internal/controller",
     ["TestComponentDispatcherSpawnFailureDoesNotDeadlock"],
     ["TestComponentDispatcherRoutesAndCachesByTag"]),
    ("parser invariants", ["internal/tagfilters/parser_f.go"], "internal/tagfilters",
     ["TestComponentParserUnsupportedTagIsUntouchedOnce", "TestComponentParserAddWithoutTimeConversion",
      "TestComponentParserTimeStringAndBytesAgree", "TestComponentParserInvalidJSONDoesNotEraseOrPartiallyMutate"],
     ["TestComponentParserBehaviorMatrix"]),
    ("concatenation ownership", ["internal/tagfilters/concator_f.go"], "internal/tagfilters",
     ["TestComponentConcatorTailIsNotAcknowledgedBeforeHead", "TestComponentConcatorDrainsClosedInput",
      "TestComponentConcatorSeparatesWorkerState"], ["TestComponentConcatorMaximumLengthAndBypass"]),
    ("field normalization", ["internal/postfilters/default_f.go"], "internal/postfilters",
     ["TestComponentPostDefaultNormalization"], ["TestComponentFieldsSelectionAndTemplate"]),
    ("monitor HTTP contract", ["internal/monitor/monitor.go"], "internal/monitor",
     ["TestComponentMonitorJSONAndReplacement", "TestComponentMonitorSerializationFailure"], []),
    ("template substitutions", ["library/add.go", "library/utils.go"], "library",
     ["TestComponentAddAdjacentModifiersAndBytes", "TestComponentTemplateMissingValueDoesNotReusePrevious",
      "TestComponentTemplateCustomRegexp"], ["TestComponentUtilityContracts", "TestComponentTimerBackoffAndReset"]),
    ("HTTP/ES lifecycle", ["internal/senders/httpforward.go", "internal/senders/elasticsearch.go"], "internal/senders",
     ["TestComponentHTTPAndESClosedInputFlushesPending", "TestComponentHTTPAndESRequestCancellation"],
     ["TestRegressionHTTPCompleteGzipAndCloseBody", "TestRegressionHTTPFailureIsReturned"]),
    ("journal provenance", ["internal/controller/journal.go"], "internal/controller",
     ["TestComponentJournalAcknowledgesOriginalTagAfterRetag", "TestComponentJournalReplayRestoresAcknowledgementOwner"],
     ["TestRegressionReplaySkipsAcknowledgedRecord"]),
]


def run_tests(work: Path, package: str, names: list[str]) -> tuple[int, set[str], set[str]]:
    result = subprocess.run(
        ["go", "test", "-mod=readonly", "-json", "-count=1", "-timeout=40s",
         "-run", "^(" + "|".join(names) + ")$", "./" + package],
        cwd=work, text=True, capture_output=True, timeout=90, check=False,
    )
    events = [json.loads(line) for line in result.stdout.splitlines() if line.startswith("{")]
    failed = {event.get("Test") for event in events if event.get("Action") == "fail"}
    passed = {event.get("Test") for event in events if event.get("Action") == "pass"}
    for event in events:
        text = event.get("Output", "")
        if "_test.go:" in text or "panic:" in text:
            print(text.rstrip(), flush=True)
    if result.stderr:
        print(result.stderr, flush=True)
    return result.returncode, failed, passed


def main() -> None:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--baseline", default="d97a5175e3287bbd9282be06425b5c6ea2285525")
    args = parser.parse_args()
    root = Path(subprocess.check_output(["git", "rev-parse", "--show-toplevel"], text=True).strip())
    with tempfile.TemporaryDirectory(prefix="component-reversion-") as temp:
        work = Path(temp) / "work"
        subprocess.run(["git", "worktree", "add", "--detach", str(work), "HEAD"], cwd=root, check=True)
        try:
            for label, paths, package, failures, controls in CASES:
                print("CASE:", label, flush=True)
                status, _, passed = run_tests(work, package, failures + controls)
                if status != 0 or not set(failures + controls) <= passed:
                    raise RuntimeError(f"{label}: current implementation did not pass all selected tests")
                originals = {path: (work / path).read_bytes() for path in paths}
                try:
                    for path in paths:
                        source = subprocess.check_output(["git", "show", f"{args.baseline}:{path}"], cwd=root)
                        (work / path).write_bytes(source)
                    status, failed, passed = run_tests(work, package, failures + controls)
                    if status != 1 or not set(failures) <= failed or not set(controls) <= passed:
                        raise RuntimeError(f"{label}: expected named failures {failures} and passing controls {controls}; "
                                           f"got status={status}, failed={failed}, passed={passed}")
                    print(f"CONFIRMED: {len(failures)} expected failures; {len(controls)} passing controls", flush=True)
                finally:
                    for path, content in originals.items():
                        (work / path).write_bytes(content)
        finally:
            subprocess.run(["git", "worktree", "remove", "--force", str(work)], cwd=root, check=True)


if __name__ == "__main__":
    main()
