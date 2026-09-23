#!/usr/bin/env python3
"""Compare external contracts against two executable versions, not mocked code."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import sys

CONTROLS = ['test_healthy_fanout_exact_content', 'test_invalid_signature_is_not_delivered']
FAILURES = {
    'test_storage_refusal_cannot_return_success': 'storage rejected record but producer received success',
    'test_restart_does_not_reuse_unacknowledged_ids': 'delivery identity reused for different events',
    'test_crash_replay_plain': 'missing sink deliveries',
}


def run(binary, names, directory):
    command = [sys.executable, 'tests/delivery/run.py', '--binary', str(binary.resolve()),
               '--artifacts', str(directory), '--seed', '127']
    for name in names:
        command.extend(['--case', name])
    directory.mkdir(parents=True, exist_ok=True)
    with (directory / 'runner.log').open('w') as log:
        completed = subprocess.run(command, stdout=log, stderr=subprocess.STDOUT,
                                   timeout=180, env=os.environ.copy())
    result = json.loads((directory / 'results.json').read_text())
    assert result['run'] == result['expected'] == len(names), result
    assert not result['errors'] and not result['skips'], result
    return completed.returncode, result


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--before', type=Path, required=True)
    parser.add_argument('--after', type=Path, required=True)
    parser.add_argument('--artifacts', type=Path, required=True)
    args = parser.parse_args()
    names = CONTROLS + list(FAILURES)
    code, current = run(args.after, names, args.artifacts / 'after')
    assert code == 0 and current['passed'], current
    code, previous = run(args.before, names, args.artifacts / 'before')
    assert code == 1 and not previous['passed'], previous
    failed = {name.rsplit('.', 1)[1] for name, _ in previous['failures']}
    assert failed == set(FAILURES), failed
    for case, diagnostic in FAILURES.items():
        details = '\n'.join(text for name, text in previous['failures'] if name.endswith('.' + case))
        assert diagnostic in details, (case, details)
    print('Confirmed: three named old-behavior failures; two controls passed in both versions.')
    print('A build error, skipped test, fixture error, or runner timeout is not a reproduction.')


if __name__ == '__main__':
    main()
