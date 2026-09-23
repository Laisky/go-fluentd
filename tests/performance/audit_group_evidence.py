#!/usr/bin/env python3
"""Independently audit persisted group-commit evidence, outside timed regions.

Does not import the application, benchmark runner, or its delivery oracle.
Checks complete cohorts, both disk ledgers, identities, request outcomes,
latency arithmetic and the declared number of before/after pairs.
"""
from __future__ import annotations

import argparse
import hashlib
import json
import math
from pathlib import Path
import statistics


def require(condition, message):
    if not condition:
        raise ValueError(message)


def load(path):
    def invalid(value):
        raise ValueError(f'non-finite JSON value: {value}')
    return json.loads(path.read_text(), parse_constant=invalid)


def lines(path):
    with path.open() as source:
        return [json.loads(line) for line in source]


def keyed(records, description):
    result = {}
    for record in records:
        key = record.get('event')
        require(isinstance(key, str) and key and key not in result,
                f'{description}: missing/duplicate event {key!r}')
        result[key] = record
    return result


def equivalent(actual, expected):
    # Preserve JSON types without repeatedly serializing large payload strings.
    if type(actual) is not type(expected):
        return False
    if isinstance(actual, dict):
        return actual.keys() == expected.keys() and all(equivalent(actual[k], expected[k]) for k in actual)
    if isinstance(actual, list):
        return len(actual) == len(expected) and all(equivalent(a, b) for a, b in zip(actual, expected))
    return actual == expected


def close(actual, expected, label):
    require(isinstance(actual, (int, float)) and not isinstance(actual, bool)
            and math.isfinite(actual) and math.isclose(actual, expected, rel_tol=1e-9, abs_tol=1e-9), label)


def audit_sample(root: Path, count: int):
    details = load(root/'sample.json')
    require(details['count'] == details['accepted'] == count
            and details['delivered_per_sink'] == [count, count], 'incomplete declared cohort')
    require(details['durable_ack'] is True, 'non-durable profile')
    config = load(root/'config.json')['settings']
    receiver = config['acceptor']['recvs']['plugins']['http']
    require(receiver['require_durable_ack'] is True, 'durable acceptance disabled in actual configuration')
    require(config['journal']['is_compress'] == details['gzip'], 'journal mode mismatch')
    require(config['journal'].get('group_commit_max_messages') == details.get('group_commit_max_messages'),
            'recorded group policy differs from configuration')
    require(len(config['producer']['plugins']) == 2, 'expected exactly two required sinks')
    for sender in config['producer']['plugins'].values():
        require(sender['is_discard_when_blocked'] is False, 'lossy output profile')
        require(sender['msg_batch_size'] == details['sink_batch'], 'output batch size mismatch')
    submitted = keyed(lines(root/'producer-manifest.jsonl'), 'producer')
    require(len(submitted) == count, 'producer manifest count mismatch')
    warm = keyed(lines(root/'wire-requests.jsonl'), 'warmup')
    require(len(warm) == 1 and not (warm.keys() & submitted.keys()), 'invalid warmup exclusion')
    expected = dict(warm, **submitted)
    responses = keyed(load(root/'requests.json'), 'responses')
    require(responses.keys() == submitted.keys(), 'response set differs from submitted cohort')
    latencies = []
    for response in responses.values():
        require(response['status'] == 200, 'unsuccessful request counted as accepted')
        latency = response['latency_ms']
        require(isinstance(latency, (int, float)) and math.isfinite(latency) and latency >= 0,
                'invalid request latency')
        latencies.append(latency)
    latencies.sort()
    for name, q in [('p50', .5), ('p95', .95), ('p99', .99), ('max', 1)]:
        close(details['latency_ms'][name], latencies[math.ceil(count*q)-1], 'incorrect '+name)
    accepted, delivered = details['accepted_seconds'], details['delivered_seconds']
    require(0 < accepted <= delivered and math.isfinite(delivered), 'invalid elapsed times')
    close(details['accepted_per_second'], count/accepted, 'incorrect accepted rate')
    close(details['delivered_per_second'], count/delivered, 'incorrect delivered rate')
    identity_by_event = {}
    for sink in ('sink-0.jsonl', 'sink-1.jsonl'):
        received = keyed(lines(root/sink), sink)
        require(received.keys() == expected.keys(), f'{sink}: missing or fabricated delivery')
        identities = {}
        for event, actual in received.items():
            wanted = dict(expected[event])
            nested = wanted.pop('nested')
            wanted['nested__origin'] = nested['origin']
            wanted['tag'] = 'events.prod'
            identity = actual.get('msgid')
            require(isinstance(identity, str) and identity and identity not in identities,
                    f'{sink}: missing/reused delivery identity')
            identities[identity] = event
            prior = identity_by_event.setdefault(event, identity)
            require(prior == identity, 'destinations disagree on delivery identity')
            wanted['msgid'] = identity
            require(equivalent(actual, wanted), f'{sink}: corrupted payload/metadata: {event}')
    for path in root.glob('process-*.log'):
        text = path.read_text(errors='replace')
        require(not any(marker in text for marker in ('panic:', 'WARNING: DATA RACE', 'fatal error:')),
                f'process failure: {path.name}')
    hashes = {name: hashlib.sha256((root/name).read_bytes()).hexdigest() for name in
              ('producer-manifest.jsonl', 'requests.json', 'sink-0.jsonl', 'sink-1.jsonl', 'config.json')}
    return {'accepted': count, 'sink_deliveries': 2*count, 'details': details, 'sha256': hashes}


def audit(root: Path, repeats: int, count: int):
    manifest = load(root/'manifest.json')
    require(manifest['repeats'] == repeats and manifest['pipeline_count'] == count,
            'unexpected repetition/message budget')
    summary = load(root/'summary.json')
    require(summary['manifest'] == manifest, 'summary/manifest mismatch')
    require(manifest['mode'] == 'pipeline', 'audit expects a pipeline shard')
    profiles = set(manifest['profiles'])
    require(len(profiles) == 1, 'exactly one pipeline profile per audit shard')
    profile = next(iter(profiles))
    fields = {'delivered_per_second', 'accepted_per_second', 'p99_ms', 'app_cpu_seconds', 'app_peak_rss_kib'}
    require(set(summary['pipeline']) == profiles and set(summary['pipeline'][profile]) == fields,
            'incomplete or extra summary metrics')
    rows = load(root/'pipeline-samples.json')
    require(len(rows) == repeats*2, 'missing or extra sample')
    seen = set()
    audited = []
    for row in rows:
        key = (row['repeat'], row['variant'])
        require(key not in seen and key[0] in range(repeats) and key[1] in ('before', 'after'),
                'duplicate/unknown pair')
        seen.add(key)
        require(row['profile'] == profile, 'mixed profile')
        require(set(row['metrics']) == fields, 'incomplete or extra sample metrics')
        details = row['details']
        require(profile == f"gzip={str(details['gzip']).lower()}/clients={details['concurrency']}",
                'workload identity mismatch')
        folder = f"pipeline-{key[0]}-{details['gzip']}-{details['concurrency']}-{key[1]}"
        checked = audit_sample(root/folder, count)
        require(checked['details'] == details, 'sample/aggregate details mismatch')
        for field, value in row['metrics'].items():
            actual = details['latency_ms']['p99'] if field == 'p99_ms' else details[field]
            close(value, actual, 'sample/aggregate metric mismatch')
        audited.append(dict(folder=folder, **checked))
    for field, reported in summary['pipeline'][profile].items():
        variants = {}
        for variant in ('before', 'after'):
            values = [r['metrics'][field] for r in sorted(rows, key=lambda r:r['repeat']) if r['variant']==variant]
            variants[variant] = values
            require(reported[variant]['samples'] == values, 'summary lost or reordered samples')
            for stat, fn in [('median', statistics.median), ('min', min), ('max', max)]:
                close(reported[variant][stat], fn(values), 'incorrect '+stat)
        ratios = [a/b for b,a in zip(variants['before'], variants['after'])]
        require(reported['paired_ratio']['samples'] == ratios, 'incorrect paired ratios')
        close(reported['median_change_percent'], (statistics.median(variants['after'])/
              statistics.median(variants['before'])-1)*100, 'incorrect improvement claim')
    controls = load(root/'single-client-controls.json') if manifest['single_client_controls'] else []
    require(len(controls) == (repeats*2 if manifest['single_client_controls'] else 0), 'missing control samples')
    seen = set()
    for control in controls:
        key = (control['repeat'], control['maximum'])
        require(key not in seen and key[0] in range(repeats) and key[1] in (1,64), 'duplicate/unknown control')
        seen.add(key)
        require(control['result']['concurrency'] == 1 and control['result']['group_commit_max_messages'] == key[1],
                'invalid same-binary control policy')
        require(profile == f"gzip={str(control['gzip']).lower()}/clients=1", 'wrong control profile')
        folder = f"single-control-{key[0]}-{control['gzip']}-{key[1]}"
        checked = audit_sample(root/folder, count)
        require(checked['details'] == control['result'], 'control/sample mismatch')
        audited.append(dict(folder=folder, **checked))
    expected_folders = {entry['folder'] for entry in audited}
    actual_folders = {p.name for p in root.iterdir() if p.is_dir() and
                      (p.name.startswith('pipeline-') or p.name.startswith('single-control-'))}
    require(expected_folders == actual_folders, 'unreported or missing sample directories')
    return {'passed': True, 'profile': profile, 'pairs': repeats, 'samples': len(audited),
            'accepted': sum(r['accepted'] for r in audited),
            'sink_deliveries': sum(r['sink_deliveries'] for r in audited), 'audited': audited}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--input', type=Path, required=True)
    parser.add_argument('--output', type=Path, required=True)
    parser.add_argument('--repeats', type=int, default=6)
    parser.add_argument('--count', type=int, default=8192)
    args = parser.parse_args()
    result = audit(args.input, args.repeats, args.count)
    args.output.write_text(json.dumps(result, indent=2))
    print(json.dumps({k:v for k,v in result.items() if k!='audited'}))


if __name__ == '__main__':
    main()
