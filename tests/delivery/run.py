#!/usr/bin/env python3
"""Run independent executable-level delivery contracts and retain exact results."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import platform
import random
import time
import unittest


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--artifacts', type=Path, required=True)
    parser.add_argument('--seed', type=int, default=127)
    parser.add_argument('--group-max-messages', type=int, choices=(1,64), default=None)
    parser.add_argument('--case', action='append', dest='cases')
    args = parser.parse_args()
    binary = args.binary.resolve(strict=True)
    args.artifacts.mkdir(parents=True, exist_ok=True)
    os.environ['GO_FLUENTD_BINARY'] = str(binary)
    os.environ['DELIVERY_ARTIFACTS'] = str(args.artifacts.resolve())
    os.environ['DELIVERY_SEED'] = str(args.seed)
    if args.group_max_messages is not None:
        os.environ['DELIVERY_GROUP_MAX_MESSAGES'] = str(args.group_max_messages)
    else:
        os.environ.pop('DELIVERY_GROUP_MAX_MESSAGES', None)
    from test_delivery import DeliveryTests
    names = args.cases or unittest.defaultTestLoader.getTestCaseNames(DeliveryTests)
    for name in names:
        if name not in unittest.defaultTestLoader.getTestCaseNames(DeliveryTests):
            parser.error('unknown case: ' + name)
    random.Random(args.seed).shuffle(names)
    print(f'Delivery seed={args.seed}; cases={len(names)}; binary={binary}', flush=True)
    start = time.monotonic()
    result = unittest.TextTestRunner(verbosity=2).run(
        unittest.TestSuite(DeliveryTests(name) for name in names))
    report = {
        'binary_sha256': hashlib.sha256(binary.read_bytes()).hexdigest(),
        'platform': platform.platform(), 'seed': args.seed, 'order': names,
        'group_commit_max_messages': args.group_max_messages,
        'expected': len(names), 'run': result.testsRun, 'seconds': time.monotonic() - start,
        'failures': [(test.id(), detail) for test, detail in result.failures],
        'errors': [(test.id(), detail) for test, detail in result.errors],
        'skips': [(test.id(), detail) for test, detail in result.skipped],
        'passed': result.wasSuccessful() and not result.skipped and result.testsRun == len(names),
    }
    (args.artifacts / 'results.json').write_text(json.dumps(report, indent=2))
    return 0 if report['passed'] else 1


if __name__ == '__main__':
    raise SystemExit(main())
