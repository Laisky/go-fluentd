#!/usr/bin/env python3
"""Check immutable journal adoption against old and merged-regression controls.

Only controls use temporary modfiles; the candidate uses committed go.mod/go.sum
without replacements. No application or journal source is patched by this test.
"""
from __future__ import annotations
import argparse
import hashlib
import json
from pathlib import Path
import shutil
import subprocess
import tempfile

JOURNAL = 'github.com/Laisky/go-journal'
OLD = 'v1.1.7-0.20260924003054-612f5354f538'
MERGED_BAD = '8758f84c23d9ae29bf800f9e84d2ec64125008aa'
CORRECTED = 'v1.1.7-0.20260926040517-fc156a60fafc'
CORRECTED_SOURCE = '5e16d047cbd5f76791b0f5759bb46c3d26eb09d1'
TEST_NAME = 'TestRegressionSelectiveReplaySequenceCompatibility'
PATTERN = '^'+TEST_NAME+'$'
EXPECTED_CASES = frozenset(
    f'{TEST_NAME}/gzip={gzip}/split={split}/{field}'
    for gzip in ('false', 'true') for split in ('false', 'true')
    for field in ('missing-data', 'missing-id')
)


def require(condition, message):
    # Validation must also run when Python is invoked with -O.
    if not condition:
        raise RuntimeError(message)


def objects(text):
    decoder = json.JSONDecoder()
    values = []
    while text.strip():
        text = text.lstrip()
        value, end = decoder.raw_decode(text)
        values.append(value)
        text = text[end:]
    return values


def command(args, root, destination, expected=0):
    with destination.open('w') as out, destination.with_name(destination.name+'.stderr').open('w') as err:
        result = subprocess.run(args, cwd=root, stdout=out, stderr=err, timeout=600)
    if result.returncode != expected:
        raise RuntimeError(f'{args!r}: exit {result.returncode}, expected {expected}; see {destination}')
    return destination.read_text()


def check_cases(log, expected):
    require(expected in ('pass', 'fail'), 'invalid expected test outcome')
    rows = [json.loads(line) for line in log.splitlines() if line.startswith('{')]
    require(not any(r.get('Action') == 'build-fail' or r.get('FailedBuild') for r in rows),
            'control did not compile; build failure is not a behavior assertion')
    output = ''.join(r.get('Output', '') for r in rows)
    require(not any(marker in output for marker in ('panic:', 'WARNING: DATA RACE', 'test timed out')),
            'panic, timeout or race is not an expected behavior assertion')
    require(not any(r.get('Action') == 'skip' for r in rows), 'skipped cases are not acceptance')
    runs = [r.get('Test') for r in rows if r.get('Action') == 'run' and r.get('Test') != TEST_NAME]
    require(len(runs) == 8 and set(runs) == EXPECTED_CASES, 'different or repeated public cases executed')
    for name in EXPECTED_CASES:
        outcomes = [r.get('Action') for r in rows if r.get('Test') == name
                    and r.get('Action') in ('pass', 'fail', 'skip')]
        require(outcomes == [expected], f'{name}: expected exactly one {expected}, found {outcomes}')
        if expected == 'fail':
            marker = ('selective replay changed sequence semantics:' if name.endswith('/missing-data')
                      else 'missing ID changed ACK suppression:')
            case_output = ''.join(r.get('Output', '') for r in rows if r.get('Test') == name)
            require(marker in case_output, f'{name}: missing intended behavior assertion')
    failures = [r for r in rows if r.get('Action') == 'fail']
    require(expected == 'fail' or not failures, 'unexpected failed test')
    require(all(r.get('Test') in EXPECTED_CASES or r.get('Test') in (TEST_NAME, None)
                for r in failures), 'unrelated test failed')
    package_results = [r.get('Action') for r in rows if 'Test' not in r
                       and r.get('Action') in ('pass', 'fail')]
    require(package_results == [expected], 'missing or conflicting package result')
    return EXPECTED_CASES


def verify_control_download(metadata, version, sums):
    require(metadata.get('Path') == JOURNAL and metadata.get('Version') == version,
            'downloaded a different control module')
    require(not metadata.get('Error') and not metadata.get('Replace'), 'unresolved/replaced control')
    require(metadata.get('Sum') and metadata.get('GoModSum'), 'missing control content or go.mod checksum')
    entries = {tuple(line.split()) for line in sums.read_text().splitlines()}
    require((JOURNAL, version, metadata['Sum']) in entries, 'control go.sum lacks module content checksum')
    require((JOURNAL, version+'/go.mod', metadata['GoModSum']) in entries,
            'control go.sum lacks go.mod checksum')


def download_control(root, out, label, mod, version):
    command(['go', 'mod', 'download', '-modfile='+str(mod)], root, out/(label+'-download.txt'))
    # A cached module downloaded in the resolver does not guarantee its ZIP hash
    # was written to this temporary modfile's sum. Select it explicitly here,
    # then verify both sums before starting a readonly negative control.
    metadata = objects(command(['go', 'mod', 'download', '-modfile='+str(mod), '-json', JOURNAL],
                               root, out/(label+'-journal-download.json')))
    require(len(metadata) == 1, 'expected one selected control module')
    verify_control_download(metadata[0], version, mod.with_suffix('.sum'))


def run_control(root, out, temp, snapshots, native, label, version, status, outcome):
    mod = temp/(label+'.mod')
    mod.write_bytes(snapshots['go.mod'])
    mod.with_suffix('.sum').write_bytes(snapshots['go.sum'])
    try:
        command(['go', 'mod', 'edit', '-modfile='+str(mod), '-require='+JOURNAL+'@'+version],
                root, out/(label+'-edit.txt'))
        download_control(root, out, label, mod, version)
        control_graph = graph(root, out/(label+'-modules.json'), mod)
        require(control_graph.keys() == native.keys(), 'unexpected graph membership change')
        differences = sorted(name for name in native if native[name] != control_graph[name])
        require(differences == [JOURNAL], f'unrelated dependency changes: {differences}')
        log = command(['go', 'test', '-mod=readonly', '-modfile='+str(mod), '-count=1', '-json',
                       '-run', PATTERN, './internal/controller'], root, out/(label+'-sequence.jsonl'), status)
        return check_cases(log, outcome)
    finally:
        # Keep the failed control's actual manifests too, not only successful ones.
        for source in (mod, mod.with_suffix('.sum')):
            if source.exists():
                shutil.copyfile(source, out/source.name)


def graph(root, out, modfile=None):
    args = ['go', 'list', '-mod=readonly']
    if modfile:
        args += ['-modfile='+str(modfile)]
    args += ['-m', '-json', 'all']
    modules = objects(command(args, root, out))
    require(not any(m.get('Replace') or m.get('Error') for m in modules), 'replacement/incomplete module graph')
    return {m['Path']: m.get('Version', '') for m in modules}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('out', type=Path)
    args = parser.parse_args()
    out = args.out.resolve()
    out.mkdir(parents=True, exist_ok=True)
    root = Path(__file__).resolve().parents[2]
    snapshots = {name: (root/name).read_bytes() for name in ('go.mod', 'go.sum')}
    require(not (root/'go.work').exists(), 'workspace override is not native adoption')
    native = graph(root, out/'candidate-modules.json')
    require(native[JOURNAL] == CORRECTED, 'unexpected corrected dependency')
    metadata = objects(command(['go', 'list', '-mod=readonly', '-m', '-json', JOURNAL], root, out/'candidate-journal.json'))[0]
    source = (Path(metadata['Dir'])/'selective.go').read_bytes()
    require(hashlib.sha1(b'blob '+str(len(source)).encode()+b'\0'+source).hexdigest() == CORRECTED_SOURCE,
            'unexpected corrected source blob')
    command(['go', 'mod', 'verify'], root, out/'module-verify.txt')
    expected_cases = check_cases(command(['go', 'test', '-mod=readonly', '-count=1', '-json', '-run', PATTERN,
                                          './internal/controller'], root, out/'candidate-sequence.jsonl'), 'pass')
    with tempfile.TemporaryDirectory(prefix='journal-adoption-') as temp:
        temp = Path(temp)
        resolver = temp/'resolver'
        resolver.mkdir()
        command(['go', 'mod', 'init', 'example.invalid/journal-adoption'], resolver, out/'resolver-init.txt')
        bad = objects(command(['go', 'mod', 'download', '-json', JOURNAL+'@'+MERGED_BAD], resolver, out/'merged-module.json'))[0]
        require(not bad.get('Error') and bad.get('Sum') and bad.get('GoModSum'), 'unresolved merged control')
        for label, version, status, outcome in [('old', OLD, 0, 'pass'), ('merged', bad['Version'], 1, 'fail')]:
            require(run_control(root, out, temp, snapshots, native, label, version, status, outcome)
                    == expected_cases, 'different public cases executed')
    require(all((root/name).read_bytes() == data for name, data in snapshots.items()), 'candidate manifests mutated')
    result = {'candidate_version': CORRECTED, 'changed_module': JOURNAL, 'candidate_replacements': False,
              'old_pass': 8, 'merged_expected_failures': 8, 'corrected_pass': 8,
              'candidate_source_blob': CORRECTED_SOURCE, 'native_module_count': len(native), 'passed': True}
    (out/'adoption.json').write_text(json.dumps(result, indent=2)+'\n')
    print(json.dumps(result, indent=2))


if __name__ == '__main__':
    main()
