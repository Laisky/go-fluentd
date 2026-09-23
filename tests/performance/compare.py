#!/usr/bin/env python3
"""Counterbalanced same-host before/after measurements, preserving raw samples.

Absolute numbers describe this runner and workload only. Ranges describe observed
variation; this program does not infer statistical significance or gate CI on
noisy timing thresholds. Correctness failures, missing workloads and inconsistent
work units do fail the run.
"""
from __future__ import annotations
import argparse
import hashlib
import json
import math
import os
from pathlib import Path
import platform
import re
import statistics
import subprocess
import time

GROUPS = [
    ('components', r'^BenchmarkPerf(AcceptorFilter|PostFilter|Parser|Concatenator|FluentEncoder|Dispatcher|Producer|Monitor|PendingMonitor|HTTPReceive|HTTPSender|JournalTTL)$', '100ms'),
    ('journal-write', r'^BenchmarkPerf(JournalAppend|JournalIDs)$', '4096x'),
    ('journal-recovery', r'^BenchmarkPerf(JournalRecovery|JournalReplay)$', '20x'),
]
EXPECTED_CASES = 35


def parse(text):
    result = {}
    for line in text.splitlines():
        if not line.startswith('BenchmarkPerf'):
            continue
        tokens = line.split()
        if len(tokens) < 4 or len(tokens) % 2 or not tokens[1].isdigit():
            raise ValueError('malformed benchmark row: ' + line)
        name = re.sub(r'-\d+$', '', tokens[0])
        metrics = {tokens[i + 1]: float(tokens[i]) for i in range(2, len(tokens), 2)}
        units = [(op, rate, kind) for op, rate, kind in
                 [('msgs/op', 'msgs/s', 'message'), ('scrapes/op', 'scrapes/s', 'scrape')]
                 if op in metrics and rate in metrics]
        if len(units) != 1 or not {'ns/op', 'B/op', 'allocs/op'} <= metrics.keys():
            raise ValueError('missing metrics for ' + name)
        if (not all(math.isfinite(v) and v >= 0 for v in metrics.values()) or
                metrics['ns/op'] <= 0 or metrics[units[0][0]] <= 0 or int(tokens[1]) < 1):
            raise ValueError('invalid work denominator: ' + name)
        metrics['units/op'] = metrics[units[0][0]]
        metrics['work_unit'] = units[0][2]
        metrics['iterations'] = int(tokens[1])
        result.setdefault(name, []).append(metrics)
    if not result:
        raise ValueError('no benchmark samples')
    return result


def summarize(before, after, repeats):
    if before.keys() != after.keys():
        raise ValueError('before/after benchmark sets differ')
    result = {}
    for name in sorted(before):
        a, b = before[name], after[name]
        if len(a) != repeats or len(b) != repeats:
            raise ValueError('missing or duplicate repetition: ' + name)
        units = {v['units/op'] for v in a + b}
        kinds = {v['work_unit'] for v in a + b}
        if len(units) != 1 or len(kinds) != 1:
            raise ValueError('work unit changed: ' + name)
        unit = next(iter(units))
        row = {'work_units_per_operation': unit, 'work_unit': next(iter(kinds)), 'samples': {'before': a, 'after': b}}
        for label, samples in [('before', a), ('after', b)]:
            times = [v['ns/op'] for v in samples]
            med = statistics.median(times)
            row[label] = {'ns_per_op': med, 'ns_range': [min(times), max(times)],
                          'bytes_per_op': statistics.median(v['B/op'] for v in samples),
                          'allocs_per_op': statistics.median(v['allocs/op'] for v in samples),
                          'work_units_per_second': unit * 1e9 / med}
        row['time_change_percent'] = (row['after']['ns_per_op'] / row['before']['ns_per_op'] - 1) * 100
        row['observed_time_ranges_overlap'] = not (
            row['before']['ns_range'][1] < row['after']['ns_range'][0] or
            row['after']['ns_range'][1] < row['before']['ns_range'][0])
        result[name] = row
    return result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--before', required=True, type=Path)
    parser.add_argument('--after', required=True, type=Path)
    parser.add_argument('--output', required=True, type=Path)
    parser.add_argument('--repeats', type=int, default=5)
    args = parser.parse_args()
    if args.repeats < 3:
        parser.error('at least three repetitions are required')
    binaries = {'before': args.before.resolve(strict=True), 'after': args.after.resolve(strict=True)}
    args.output.mkdir(parents=True, exist_ok=False)
    data = {'before': {}, 'after': {}}
    manifest = {'method': 'alternating before/after; uninstrumented binaries; identical work units',
                'platform': platform.platform(), 'gomaxprocs': os.environ.get('GOMAXPROCS'),
                'binaries': {k: {'path': str(p), 'sha256': hashlib.sha256(p.read_bytes()).hexdigest()}
                             for k, p in binaries.items()}, 'execution': []}
    for repeat in range(args.repeats):
        order = ('before', 'after') if repeat % 2 == 0 else ('after', 'before')
        for label in order:
            for group, pattern, duration in GROUPS:
                command = [str(binaries[label]), '-test.run=^$', '-test.bench=' + pattern,
                           '-test.benchtime=' + duration, '-test.count=1', '-test.timeout=180s']
                started = time.monotonic()
                completed = subprocess.run(command, capture_output=True, text=True, timeout=240)
                stem = f'{repeat}-{label}-{group}'
                (args.output / (stem + '.txt')).write_text(completed.stdout + completed.stderr)
                manifest['execution'].append({'sample': stem, 'command': command,
                                               'seconds': time.monotonic() - started,
                                               'returncode': completed.returncode})
                (args.output / 'manifest.json').write_text(json.dumps(manifest, indent=2))
                if completed.returncode or not re.search(r'^PASS$', completed.stdout, re.M):
                    raise RuntimeError('benchmark did not pass: ' + stem)
                for name, samples in parse(completed.stdout).items():
                    data[label].setdefault(name, []).extend(samples)
                print(stem, 'passed', flush=True)
    summary = summarize(data['before'], data['after'], args.repeats)
    if len(summary) != EXPECTED_CASES:
        raise ValueError(f'expected {EXPECTED_CASES} workloads, got {len(summary)}')
    (args.output / 'summary.json').write_text(json.dumps(summary, indent=2))
    rows = ['# Paired performance results', '',
            'Medians of same-host alternating runs. Raw samples/ranges are retained; timing changes are not significance claims.', '',
            '| Workload | Before ns/op | After ns/op | Change | B/op before → after | allocs/op before → after |',
            '|---|---:|---:|---:|---:|---:|']
    for name, row in summary.items():
        a, b = row['before'], row['after']
        rows.append(f'| `{name}` | {a["ns_per_op"]:,.1f} | {b["ns_per_op"]:,.1f} | {row["time_change_percent"]:+.1f}% | {a["bytes_per_op"]:,.0f} → {b["bytes_per_op"]:,.0f} | {a["allocs_per_op"]:g} → {b["allocs_per_op"]:g} |')
    (args.output / 'summary.md').write_text('\n'.join(rows) + '\n')


if __name__ == '__main__':
    main()
