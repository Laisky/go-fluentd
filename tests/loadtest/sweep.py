#!/usr/bin/env python3
"""Retain all fixed-arrival-rate trials; never equate queue admission with capacity."""
from __future__ import annotations
import argparse
import json
from pathlib import Path
import subprocess


def main():
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--driver', type=Path, required=True)
    p.add_argument('--baseline', type=Path, required=True)
    p.add_argument('--candidate', type=Path, required=True)
    p.add_argument('--out', type=Path, required=True)
    p.add_argument('--rates', default='100,300,600,1000,1500')
    p.add_argument('--repeats', type=int, default=2)
    p.add_argument('--requests', type=int, default=1024)
    p.add_argument('--seconds', type=float, default=0, help='positive: use rate * seconds requests instead')
    p.add_argument('--p99-ms', type=float, default=100)
    a = p.parse_args()
    rates = [int(x) for x in a.rates.split(',')]
    if not rates or min(rates) < 1 or a.repeats < 1 or a.requests < 1 or a.seconds < 0 or a.p99_ms <= 0:
        p.error('positive rates, requests, repeats and SLO required')
    a.out.mkdir(parents=True, exist_ok=False)
    report = {'completed': False, 'p99_limit_ms': a.p99_ms, 'minimum_rate_fraction': .95,
              'rates': rates, 'repeats': a.repeats, 'samples': []}
    try:
        for rate in rates:
            for repeat in range(a.repeats):
                order = ('baseline', 'candidate') if repeat % 2 == 0 else ('candidate', 'baseline')
                for label in order:
                    root = a.out / f'rate-{rate}-{repeat}-{label}'
                    count = max(1, int(rate * a.seconds)) if a.seconds else a.requests
                    cmd = [str(a.driver.resolve()), '--binary', str(getattr(a, label).resolve()),
                           '--out', str(root), '--protocol', 'logs', '--requests', str(count),
                           '--concurrency', '64', '--payload', '512', '--rate', str(rate)]
                    with (a.out / (root.name + '.log')).open('w') as log:
                        r = subprocess.run(cmd, stdout=log, stderr=subprocess.STDOUT, timeout=max(240, a.seconds + 180))
                    row = {'rate': rate, 'repeat': repeat, 'label': label, 'command': cmd,
                           'directory': root.name, 'returncode': r.returncode, 'meets_slo': False}
                    if (root / 'summary.json').exists():
                        s = row['summary'] = json.loads((root / 'summary.json').read_text())
                        if r.returncode == 0 and s.get('passed'):
                            subprocess.run([str(a.driver.resolve()), '--audit-only', str(root)], check=True,
                                           stdout=subprocess.DEVNULL, timeout=30)
                            row['meets_slo'] = (s['scheduled_e2e_ms']['p99'] <= a.p99_ms and
                                                s['delivered_rps'] >= .95 * rate)
                        print(root.name, 'valid', bool(s.get('passed')), 'SLO', row['meets_slo'],
                              'rps', round(s['delivered_rps'], 1), 'p99', round(s['scheduled_e2e_ms']['p99'], 1), flush=True)
                    report['samples'].append(row)
                    (a.out / 'sweep.json').write_text(json.dumps(report, indent=2))
        report['completed'] = True
        report['largest_tested_passing_rate'] = {
            label: max([0] + [rate for rate in rates if all(r['meets_slo'] for r in report['samples']
                         if r['label'] == label and r['rate'] == rate)])
            for label in ('baseline', 'candidate')}
    finally:
        (a.out / 'sweep.json').write_text(json.dumps(report, indent=2))


if __name__ == '__main__':
    main()
