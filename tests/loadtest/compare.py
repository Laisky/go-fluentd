#!/usr/bin/env python3
"""Serial, alternating paired black-box trials. Never mix profiled/race binaries.

A failed trial is retained and is not silently retried or counted as capacity.
Raw requests, application-only resources, settings, and summaries stay in out/.
"""
from __future__ import annotations
import argparse
import json
import math
from pathlib import Path
import statistics
import subprocess


def metric(sample, name):
    value = sample
    for part in name.split('.'):
        value = value[part]
    return float(value)


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--driver', type=Path, required=True)
    p.add_argument('--baseline', type=Path, required=True)
    p.add_argument('--candidate', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--pairs', type=int, default=6)
    p.add_argument('--cases', type=Path, required=True,
                   help='JSON array of {name, args: [driver flags]}')
    a = p.parse_args()
    if a.pairs < 1:
        p.error('pairs must be positive')
    a.out.mkdir(parents=True, exist_ok=False)
    cases = json.loads(a.cases.read_text())
    manifest = {'pairs': a.pairs, 'cases': cases, 'samples': [], 'passed': False}
    try:
        for case in cases:
            for pair in range(a.pairs):
                order = ('baseline', 'candidate') if pair % 2 == 0 else ('candidate', 'baseline')
                for label in order:
                    target = a.out / f'{case["name"]}-{pair}-{label}'
                    cmd = [str(a.driver.resolve()), '--binary', str(getattr(a, label).resolve()),
                           '--out', str(target), *case['args']]
                    with (a.out / (target.name + '.log')).open('w') as log:
                        result = subprocess.run(cmd, stdout=log, stderr=subprocess.STDOUT, timeout=240)
                    row = {'case': case['name'], 'pair': pair, 'label': label,
                           'directory': target.name, 'returncode': result.returncode, 'command': cmd}
                    if (target / 'summary.json').exists():
                        row['summary'] = json.loads((target / 'summary.json').read_text())
                    manifest['samples'].append(row)
                    (a.out / 'campaign.json').write_text(json.dumps(manifest, indent=2))
                    if result.returncode or not row.get('summary', {}).get('passed'):
                        raise RuntimeError(f'{target.name}: failed; retained, not counted as capacity')
                    if row['summary'].get('profiling_enabled'):
                        raise RuntimeError('profiled runs cannot be paired capacity evidence')
                    subprocess.run([str(a.driver.resolve()), '--audit-only', str(target)], check=True,
                                   stdout=subprocess.DEVNULL, timeout=30)
                    s = row['summary']
                    print(target.name, 'rps', round(s['delivered_rps'], 1),
                          'p99', round(s['e2e_ms']['p99'], 2),
                          'cpu_us', round(s['app_cpu_us_per_delivered'], 1), flush=True)
        report = {}
        for case in cases:
            rows = [s for s in manifest['samples'] if s['case'] == case['name']]
            by = {label: sorted([r for r in rows if r['label'] == label], key=lambda x:x['pair'])
                  for label in ('baseline', 'candidate')}
            metrics = ('delivered_rps', 'ack_ms.p99', 'e2e_ms.p99', 'scheduled_e2e_ms.p99',
                       'app_cpu_us_per_delivered', 'app_peak_rss_bytes', 'runtime_deltas.TotalAlloc')
            report[case['name']] = {}
            for name in metrics:
                old = [metric(r['summary'], name) for r in by['baseline']]
                new = [metric(r['summary'], name) for r in by['candidate']]
                ratios = [n / o for o, n in zip(old, new) if o > 0]
                report[case['name']][name] = {
                    'baseline_median': statistics.median(old), 'candidate_median': statistics.median(new),
                    'paired_ratio_median': statistics.median(ratios),
                    'paired_ratio_min': min(ratios), 'paired_ratio_max': max(ratios),
                    'all_pairs': ratios}
        manifest['passed'] = True
        (a.out / 'comparison.json').write_text(json.dumps(report, indent=2))
    finally:
        (a.out / 'campaign.json').write_text(json.dumps(manifest, indent=2))


if __name__ == '__main__':
    main()
